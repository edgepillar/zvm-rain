// Package rain is the bot: it watches who speaks in each group, splits a
// sender's /rain evenly between the linked people among them, and links
// Telegram accounts to addresses in direct messages.
package rain

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/Zenoglyphs-VM/zvm-rain/internal/zns"
	"html"
	"log/slog"
	"math/big"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/Zenoglyphs-VM/zvm-rain/internal/chain"
	"github.com/Zenoglyphs-VM/zvm-rain/internal/store"
)

// Out is where messages go: Telegram in the bot, a recorder in tests.
type Out interface {
	Send(ctx context.Context, chat int64, html string) error
}

type Config struct {
	Chain   Chain
	Out     Out
	Store   *store.Store
	Tracker *Tracker
	Links   Links

	// Tokens are the ones /rain, /fund and /wallet know by name, besides ZNN;
	// any other ERC-20 works by its 0x address.
	Tokens []Named
	// DefaultMinutes is the window when /rain names none; MaxMinutes the
	// largest it accepts, which must not exceed what the Tracker keeps.
	DefaultMinutes, MaxMinutes int
	// MaxRecipients caps a rain at the most recent speakers. At most the
	// contract's 100.
	MaxRecipients int
	// SecretTTL is how long a /link secret stays good.
	SecretTTL time.Duration
	// Exclude are addresses a rain never pays: the bot's own and the
	// contract's.
	Exclude map[common.Address]bool
	// WZNN is wrapped ZNN, which a ZNN rain is approved and drawn from.
	WZNN common.Address
	// StartBlock is where the link watcher starts on an empty database: the
	// block the contract was deployed in.
	StartBlock uint64
	// Poll is how often the link watcher looks for new links.
	Poll time.Duration

	Now func() time.Time
	Log *slog.Logger
}

type Bot struct {
	Config
	wg sync.WaitGroup
}

// Person is a Telegram user as the bot addresses them.
type Person struct {
	ID   int64
	Name string
}

func New(cfg Config) *Bot {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.SecretTTL == 0 {
		cfg.SecretTTL = time.Hour
	}
	if cfg.Poll == 0 {
		cfg.Poll = 4 * time.Second
	}
	return &Bot{Config: cfg}
}

// Wait returns once every rain in flight has finished.
func (b *Bot) Wait() { b.wg.Wait() }

func (b *Bot) say(ctx context.Context, chat int64, msg string) {
	if msg == "" {
		return
	}
	if err := b.Out.Send(ctx, chat, msg); err != nil {
		b.Log.Warn("send", "chat", chat, "err", err)
	}
}

// --- groups ---------------------------------------------------------------

// Seen records a message in a group; every message counts, commands too.
func (b *Bot) Seen(chat int64, p Person, at time.Time) {
	b.Tracker.Saw(chat, p.ID, p.Name, at)
}

// Rain handles /rain. It returns at once; the rain is read, sent and reported
// in the background so the update loop keeps counting who is talking.
func (b *Bot) Rain(ctx context.Context, chat int64, p Person, args []string) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		reply := b.rain(ctx, chat, p, args)
		// Every /rain is logged with what it answered, paid or not, so "why
		// did it not rain" is answered by the log rather than by guessing.
		b.Log.Info("/rain", "chat", chat, "from", p.ID, "args", strings.Join(args, " "),
			"seen", len(b.Tracker.Active(chat, b.Now().Add(-time.Duration(b.MaxMinutes)*time.Minute))), "reply", plainText(reply, 160))
		b.say(ctx, chat, reply)
	}()
}

var tag = regexp.MustCompile(`<[^>]*>`)

// plainText is a reply without its HTML, shortened, for the log.
func plainText(s string, n int) string {
	s = html.UnescapeString(tag.ReplaceAllString(s, ""))
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return s
}

// pick is one recipient.
type pick struct {
	Seen
	addr common.Address
}

func (b *Bot) rain(ctx context.Context, chat int64, p Person, args []string) string {
	req, err := ParseRequest(args)
	if err != nil {
		return err.Error()
	}
	mins := req.Minutes
	if mins == 0 {
		mins = b.DefaultMinutes
	}
	if mins > b.MaxMinutes {
		return fmt.Sprintf("The window is at most %d minutes.", b.MaxMinutes)
	}
	u, err := b.Store.User(ctx, p.ID)
	if err != nil || u.Address == "" {
		return fmt.Sprintf("%s, link a wallet first: send me /link in a direct message.", Mention(p.ID, p.Name))
	}
	sender := common.HexToAddress(u.Address)
	if ok, err := b.Chain.Linked(ctx, sender); err != nil {
		return "Could not read the contract: " + esc(err.Error())
	} else if !ok {
		return fmt.Sprintf("%s, %s has unlinked on the contract. Send me /link to link again.", Mention(p.ID, p.Name), b.Links.Address(sender))
	}
	tok, err := b.resolve(ctx, req.Token)
	if err != nil {
		return err.Error()
	}
	total, err := tok.Parse(req.Amount)
	if err != nil {
		return esc(err.Error())
	}

	now := b.Now()
	active := b.Tracker.Active(chat, now.Add(-time.Duration(mins)*time.Minute))
	ids := make([]int64, 0, len(active))
	for _, s := range active {
		ids = append(ids, s.ID)
	}
	addrs, err := b.Store.Addresses(ctx, ids)
	if err != nil {
		return "Could not read who is linked: " + esc(err.Error())
	}
	var picks []pick
	var unlinked []Seen
	over := 0
	for _, s := range active {
		if s.ID == p.ID {
			continue
		}
		a, ok := addrs[s.ID]
		if !ok {
			unlinked = append(unlinked, s)
			continue
		}
		addr := common.HexToAddress(a)
		if addr == sender || b.Exclude[addr] {
			continue
		}
		if len(picks) == b.MaxRecipients {
			over++
			continue
		}
		// Someone can unlink on the contract without telling the bot, and
		// the contract would refuse the whole rain for them.
		if ok, err := b.Chain.Linked(ctx, addr); err != nil {
			return "Could not read the contract: " + esc(err.Error())
		} else if !ok {
			unlinked = append(unlinked, s)
			continue
		}
		picks = append(picks, pick{Seen: s, addr: addr})
	}
	window := plural(mins, "minute", "minutes")
	if mins == 1 {
		window = "minute"
	}
	// Nobody to pay: say which of the two it is, and that nothing was taken.
	// Nothing has been recorded or sent at this point.
	if len(picks) == 0 {
		if len(unlinked) == 0 {
			return fmt.Sprintf("Nobody else has spoken here in the last %s, so there is no one to rain on. Nothing was spent; try a longer window, as in <code>/rain %s %d</code>.",
				window, esc(strings.TrimSpace(req.Amount+" "+req.Token)), min(b.MaxMinutes, max(mins*5, 5)))
		}
		return fmt.Sprintf("No one who spoke here in the last %s has linked a wallet yet, so there is no one to rain on. Nothing was spent.%s", window, b.nudge(unlinked))
	}

	n := big.NewInt(int64(len(picks)))
	each := new(big.Int).Quo(total, n)
	if each.Sign() == 0 {
		return fmt.Sprintf("%s %s is too little to split %d ways.", tok.Format(total), esc(tok.Symbol), len(picks))
	}
	need := new(big.Int).Mul(each, n)
	if short := b.short(ctx, p, tok, sender, need); short != "" {
		return short
	}

	// The contract wants recipients in ascending order; the message keeps
	// the order people spoke in.
	sorted := make([]common.Address, len(picks))
	rec := &store.Rain{ChatID: chat, SenderTg: p.ID, Sender: sender.Hex(), Token: tok.Address.Hex(), Symbol: tok.Symbol, Each: each.String(), Minutes: mins}
	for i, pk := range picks {
		sorted[i] = pk.addr
		rec.To = append(rec.To, store.Recipient{TgID: pk.ID, Address: pk.addr.Hex()})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Cmp(sorted[j]) < 0 })
	id, err := b.Store.NewRain(ctx, rec, now)
	if err != nil {
		return "Could not record the rain: " + esc(err.Error())
	}
	b.Log.Info("rain", "id", id, "chat", chat, "from", sender, "token", tok.Symbol, "each", each, "recipients", len(picks))

	paid, err := b.Chain.Rain(ctx, uint64(id), sender, tok, each, sorted)
	if err != nil {
		return b.failed(ctx, id, err)
	}
	if err := b.Store.FinishRain(ctx, id, "paid", paid.Tx.Hex(), ""); err != nil {
		b.Log.Warn("recording a paid rain", "id", id, "err", err)
	}

	var s strings.Builder
	fmt.Fprintf(&s, "☔ %s rained %s %s on %d %s who spoke in the last %s: <b>%s %s</b> each.\n\n",
		Mention(p.ID, p.Name), tok.Format(need), esc(tok.Symbol), len(picks), noun(len(picks), "person", "people"), window,
		tok.Format(each), esc(tok.Symbol))
	names := make([]string, len(picks))
	for i, pk := range picks {
		names[i] = Mention(pk.ID, pk.Name)
	}
	s.WriteString(strings.Join(names, ", "))
	s.WriteString("\n\n" + b.Links.Tx(paid.Tx))
	if dust := new(big.Int).Sub(total, need); dust.Sign() > 0 {
		fmt.Fprintf(&s, "\n%s %s did not split evenly and stays with the sender.", tok.Format(dust), esc(tok.Symbol))
	}
	if len(paid.Wrapped) > 0 {
		var who []string
		for _, a := range paid.Wrapped {
			who = append(who, b.Links.Address(a))
		}
		fmt.Fprintf(&s, "\n%s would not take plain ZNN, so got their share as WZNN.", strings.Join(who, ", "))
	}
	if over > 0 {
		fmt.Fprintf(&s, "\n%d more linked %s spoke; a rain pays the %d most recent.", over, noun(over, "person", "people"), b.MaxRecipients)
	}
	s.WriteString(b.nudge(unlinked))
	return s.String()
}

// failed records and explains a rain that did not come back paid.
func (b *Bot) failed(ctx context.Context, id int64, err error) string {
	var rev *chain.RevertError
	if errors.As(err, &rev) {
		// Refused at estimation (nothing sent) or reverted on chain (nothing
		// paid, and the id is free again): either way nobody was paid.
		_ = b.Store.FinishRain(ctx, id, "failed", hashOrEmpty(rev.Tx), rev.Reason)
		b.Log.Warn("rain refused", "id", id, "reason", rev.Reason, "tx", rev.Tx)
		return "The contract refused the rain, and nothing was paid: " + esc(rev.Reason) + "."
	}
	// Sent, or perhaps sent, and no receipt: ask the contract.
	if paid, rerr := b.Chain.Rained(ctx, uint64(id)); rerr == nil && paid {
		_ = b.Store.FinishRain(ctx, id, "paid", "", "receipt lost: "+err.Error())
		return "The rain was paid, but I lost track of its transaction. " + b.Links.Contract()
	}
	_ = b.Store.FinishRain(ctx, id, "lost", "", err.Error())
	b.Log.Warn("rain lost", "id", id, "err", err)
	return fmt.Sprintf("I could not confirm the rain: %s. It has not been paid yet and may still land; it can never be paid twice.", esc(err.Error()))
}

// nudge names the people who spoke but could not be paid.
func (b *Bot) nudge(unlinked []Seen) string {
	if len(unlinked) == 0 {
		return ""
	}
	const show = 10
	var names []string
	for i, s := range unlinked {
		if i == show {
			names = append(names, fmt.Sprintf("%d more", len(unlinked)-show))
			break
		}
		names = append(names, esc(s.Name))
	}
	return fmt.Sprintf("\n\nNot linked, so not paid: %s. Send me /link in a direct message to catch the next one.", strings.Join(names, ", "))
}

// --- direct messages ------------------------------------------------------

var txHash = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)

// Direct handles a direct message and returns the reply.
func (b *Bot) Direct(ctx context.Context, p Person, cmd string, args []string, text string) string {
	switch cmd {
	case "start", "help":
		return b.help()
	case "link", "connect":
		return b.link(ctx, p)
	case "wallet", "balance":
		return b.wallet(ctx, p)
	case "fund", "approve":
		return b.fund(ctx, p, args)
	case "revoke":
		return b.revoke(ctx, args)
	case "tokens":
		return b.tokens()
	case "unlink":
		return b.unlink(ctx, p)
	case "rain":
		return "Rain happens in groups: in a group I am in, <code>/rain 100</code> splits 100 ZNN between everyone linked who spoke in the last minute."
	case "":
		if txHash.MatchString(text) {
			return b.hash(ctx, p, common.HexToHash(text))
		}
		return ""
	default:
		return fmt.Sprintf("Here: /link, /wallet, /revoke, /tokens, /unlink, /help. To let Rain spend a token: %s.", b.fundLink("the Rain page", nil, nil))
	}
}

func (b *Bot) help() string {
	example := Token{Symbol: "wQSR", Decimals: 8}
	if len(b.Tokens) > 0 {
		example = Token{Address: b.Tokens[0].Address, Symbol: b.Tokens[0].Name, Decimals: 18}
	}
	hundred, fiveHundred := big.NewInt(100), big.NewInt(500)
	hundred.Mul(hundred, pow10(ZNN.Decimals))
	fiveHundred.Mul(fiveHundred, pow10(example.Decimals))
	return fmt.Sprintf(`<b>Rain</b> splits tokens evenly between everyone who spoke in a Telegram group in the last few minutes, on the ZVM.

<b>To receive</b>, link a wallet: send /link and follow it. That is all.

<b>To send</b>, link too, then approve Rain for exactly what you choose, one token at a time, on %[4]s. Nothing leaves your wallet until you rain:
%[5]s  (ZNN is approved as WZNN)
%[6]s  and nothing else
<code>/revoke %[2]s</code>  take an approval back

Then, in a group:
<code>/rain 100</code>  100 ZNN over whoever spoke here in the last minute
<code>/rain 100 5</code>  over the last 5 minutes
<code>/rain 50 %[2]s</code>  in %[3]s; /tokens lists the names, and any ERC-20 works by its 0x address

Only people who have linked are paid, and only for the group the rain is in, never the sender. /wallet shows your link and what Rain may spend.

You link on the <a href="%[1]s">Rain contract</a>; approvals are made on each token's own contract, and the Rain page makes them for you.`,
		b.Links.Contract(), esc(strings.ToLower(example.Symbol)), esc(example.Symbol), b.fundLink("the Rain page", nil, nil),
		b.fundLink("Approve 100 ZNN", &ZNN, hundred), b.fundLink("Approve 500 "+esc(example.Symbol), &example, fiveHundred))
}

// link issues a fresh secret.
func (b *Bot) link(ctx context.Context, p Person) string {
	var secret int64
	var err error
	for range 5 {
		var raw [8]byte
		if _, err = rand.Read(raw[:]); err != nil {
			break
		}
		// 63 bits: SQLite's integers are signed, and the contract's uint64
		// holds every one of them.
		secret = int64(binary.BigEndian.Uint64(raw[:]) >> 1)
		if secret == 0 {
			continue
		}
		if err = b.Store.SetSecret(ctx, p.ID, p.Name, secret, b.Now()); !errors.Is(err, store.ErrSecretTaken) {
			break
		}
	}
	if err != nil {
		return "Could not issue a secret: " + esc(err.Error())
	}
	q := url.Values{"link": {strconv.FormatInt(secret, 10)}}
	return fmt.Sprintf(`Your secret is <code>%d</code>. It is good for %s.

%s

By hand instead: on the <a href="%s">Rain contract</a>, open <b>Write</b>, connect the wallet you want to be paid in, and call <code>link</code> with <code>%d</code>.

Linking is all it takes to be paid; to send rains too, approve Rain for a token afterwards on %s. I will notice the transaction by myself. If I have not said so within a minute, send me its hash (0x…).`,
		secret, humanDuration(b.SecretTTL), b.Links.Tap("Link your wallet", q), b.Links.Contract(), secret, b.fundLink("the Rain page", nil, nil))
}

// hash links from a transaction hash the user sent. It is the authoritative
// path: the watcher links the first address it sees send a secret, and a
// hash from the user's own chat settles which address was really theirs.
func (b *Bot) hash(ctx context.Context, p Person, tx common.Hash) string {
	secret, issued, err := b.Store.Secret(ctx, p.ID)
	if errors.Is(err, store.ErrNotFound) {
		return "Send /link first: I need to give you a secret to put in the transaction."
	}
	if err != nil {
		return "Could not read your secret: " + esc(err.Error())
	}
	if b.Now().Sub(issued) > b.SecretTTL {
		return "Your secret has expired. Send /link for a new one."
	}
	ev, err := b.Chain.ReadLink(ctx, tx)
	if err != nil {
		return esc(err.Error()) + "."
	}
	if int64(ev.Secret) != secret {
		return fmt.Sprintf("That transaction carries the secret %d, and yours is %d.", ev.Secret, secret)
	}
	return b.bind(ctx, p.ID, p.Name, ev)
}

func (b *Bot) bind(ctx context.Context, tgID int64, name string, ev *LinkEvent) string {
	if b.Exclude[ev.Account] {
		return "That address belongs to the bot and cannot take part."
	}
	if err := b.Store.Link(ctx, tgID, name, ev.Account.Hex(), ev.Tx.Hex(), b.Now()); err != nil {
		return "Could not save the link: " + esc(err.Error())
	}
	if err := b.Store.UseSecret(ctx, tgID, ev.Account.Hex()); err != nil {
		b.Log.Warn("marking a secret used", "err", err)
	}
	b.Log.Info("linked", "tg", tgID, "address", ev.Account, "tx", ev.Tx)
	return fmt.Sprintf("Linked: %s is yours, and you will be paid in any rain you are around for.\n\n%s", b.Links.Address(ev.Account), b.funds(ctx, ev.Account))
}

func (b *Bot) wallet(ctx context.Context, p Person) string {
	u, err := b.Store.User(ctx, p.ID)
	if err != nil || u.Address == "" {
		return "No wallet linked. Send /link to link one."
	}
	a := common.HexToAddress(u.Address)
	s := fmt.Sprintf("Your wallet: %s\n", b.Links.Address(a))
	if ok, err := b.Chain.Linked(ctx, a); err == nil && !ok {
		s += "It has unlinked on the contract, so it is neither paid nor can it rain. Send /link to link again.\n"
	}
	return s + b.funds(ctx, a)
}

func (b *Bot) unlink(ctx context.Context, p Person) string {
	u, err := b.Store.User(ctx, p.ID)
	if err != nil || u.Address == "" {
		return "No wallet is linked."
	}
	if err := b.Store.Unlink(ctx, p.ID); err != nil {
		return "Could not unlink: " + esc(err.Error())
	}
	return fmt.Sprintf(`Forgotten: %s is no longer linked to you here.

The contract still counts it as linked until you call <code>unlink</code> on the <a href="%s">Rain contract</a>. Do that too if you want to be sure no rain can ever spend from it, whatever you have approved.`, b.Links.Address(common.HexToAddress(u.Address)), b.Links.Contract())
}

// --- the link watcher -----------------------------------------------------

// watchKey is per contract, so a redeployed Rain starts from its own block.
func (b *Bot) watchKey() string { return "links_block:" + strings.ToLower(b.Links.Rain.Hex()) }

// Watch follows Linked events and links whoever sends a pending secret, so a
// user does not have to paste a transaction hash. It returns when ctx ends.
func (b *Bot) Watch(ctx context.Context) {
	next := b.StartBlock
	if v, err := b.Store.Meta(ctx, b.watchKey()); err == nil {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			next = n + 1
		}
	}
	for ctx.Err() == nil {
		// A bounded range per call, so a bot that was off for days catches
		// up in steps rather than asking the node for everything at once.
		evs, last, err := b.Chain.Links(ctx, next, next+2000)
		if err != nil {
			b.Log.Warn("reading links", "err", err)
		} else {
			for i := range evs {
				b.watched(ctx, &evs[i])
			}
			if last+1 > next {
				next = last + 1
				if err := b.Store.SetMeta(ctx, b.watchKey(), strconv.FormatUint(last, 10)); err != nil {
					b.Log.Warn("saving the watcher's block", "err", err)
				}
				continue // more may be waiting
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(b.Poll):
		}
	}
}

func (b *Bot) watched(ctx context.Context, ev *LinkEvent) {
	if ev.Secret > 1<<63-1 {
		return // never issued: every secret fits 63 bits
	}
	is, err := b.Store.SecretOwner(ctx, int64(ev.Secret))
	if err != nil {
		return // nobody's secret, or an old one already replaced
	}
	if b.Now().Sub(is.At) > b.SecretTTL {
		return
	}
	account := strings.ToLower(ev.Account.Hex())
	switch is.UsedBy {
	case "":
		b.say(ctx, is.TgID, b.bind(ctx, is.TgID, is.Name, ev))
	case account:
		// The same address linking twice with one secret.
	default:
		// Someone else sent the secret after the user's own transaction, or
		// the user linked from two wallets. The first stands; the user can
		// settle it with the hash of the one that is theirs.
		b.say(ctx, is.TgID, fmt.Sprintf("Another address, %s, also sent your secret. You stay linked to %s. If %s is the one you want, send me the hash of its transaction: %s",
			b.Links.Address(ev.Account), b.Links.Address(common.HexToAddress(is.UsedBy)), b.Links.Address(ev.Account), b.Links.Tx(ev.Tx)))
	}
}

// --- after a restart ------------------------------------------------------

// Recover settles rains the bot stopped in the middle of. The contract says
// whether each id was paid; one that was not is left as lost rather than
// resent, since it may still be in a relayer's batch, and a second send of the
// same id would only be refused.
func (b *Bot) Recover(ctx context.Context) error {
	ids, err := b.Store.Sending(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		paid, err := b.Chain.Rained(ctx, uint64(id))
		if err != nil {
			return err
		}
		state := "lost"
		if paid {
			state = "paid"
		}
		b.Log.Info("unfinished rain", "id", id, "state", state)
		if err := b.Store.FinishRain(ctx, id, state, "", "settled at startup"); err != nil {
			return err
		}
	}
	return nil
}

// --- formatting -----------------------------------------------------------

// Links builds the explorer URLs every on-chain thing is shown with.
type Links struct {
	Explorer string // e.g. http://localhost:47080/explorer
	Rain     common.Address
	// Page is Rain's page, where the wallet signs what the bot prepares,
	// e.g. http://localhost:47080/rain/. Empty: the bot gives only the
	// explorer steps.
	Page string
	// Names reads primary names for Address; nil prints bare addresses.
	Names *zns.Names
}

// Tap is the line that opens the page with the work filled in, and the same
// page inside MetaMask's app for a phone, where Telegram's browser has no
// wallet.
func (l Links) Tap(what string, q url.Values) string {
	if l.Page == "" {
		return ""
	}
	page := l.Page + "?" + q.Encode()
	mm := "https://metamask.app.link/dapp/" + strings.TrimPrefix(strings.TrimPrefix(page, "https://"), "http://")
	return fmt.Sprintf(`<b><a href="%s">%s</a></b> on the Rain page: your wallet signs it there. On a phone, <a href="%s">open it in MetaMask</a>.`, html.EscapeString(page), what, html.EscapeString(mm))
}

func (l Links) Tx(h common.Hash) string {
	return fmt.Sprintf(`<a href="%s/tx/%s">%s…%s</a>`, l.Explorer, h.Hex(), h.Hex()[:8], h.Hex()[62:])
}

// Address links an address to the explorer, reading as its ZNS primary name
// when it has one (verified both ways by zns.Names), and as 0x1234…abcd
// otherwise.
func (l Links) Address(a common.Address) string {
	if name := l.Names.Name(a); name != "" {
		return fmt.Sprintf(`<a href="%s/address/%s">%s</a>`, l.Explorer, a.Hex(), html.EscapeString(name))
	}
	return fmt.Sprintf(`<a href="%s/address/%s">%s…%s</a>`, l.Explorer, a.Hex(), a.Hex()[:6], a.Hex()[38:])
}

// Contract is the Rain contract's tab on the explorer, where link and
// unlink are called.
func (l Links) Contract() string { return l.ContractOf(l.Rain) }

func (l Links) ContractOf(a common.Address) string {
	return fmt.Sprintf("%s/address/%s?tab=contract", l.Explorer, a.Hex())
}

// Mention links a name to a Telegram user, which notifies them.
func Mention(id int64, name string) string {
	return fmt.Sprintf(`<a href="tg://user?id=%d">%s</a>`, id, html.EscapeString(name))
}

func esc(s string) string { return html.EscapeString(s) }

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func noun(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func hashOrEmpty(h common.Hash) string {
	if h == (common.Hash{}) {
		return ""
	}
	return h.Hex()
}

// humanDuration writes a duration the way a person would: 1h, 1h30m, 10m, 45s,
// rather than Go's 1h0m0s.
func humanDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

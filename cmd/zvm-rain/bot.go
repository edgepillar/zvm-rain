package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/Zenoglyphs-VM/zvm-rain/internal/zns"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/Zenoglyphs-VM/zvm-rain/internal/chain"
	"github.com/Zenoglyphs-VM/zvm-rain/internal/contracts"
	"github.com/Zenoglyphs-VM/zvm-rain/internal/deploy"
	"github.com/Zenoglyphs-VM/zvm-rain/internal/keys"
	"github.com/Zenoglyphs-VM/zvm-rain/internal/rain"
	"github.com/Zenoglyphs-VM/zvm-rain/internal/store"
	"github.com/Zenoglyphs-VM/zvm-rain/internal/tg"
)

func cmdBot(ctx context.Context, args []string) error {
	// Every setting comes from the environment, so the whole deployment is
	// zvm-rain/.env and the command is just "bot". A flag still wins, for a
	// run by hand.
	defMin, err := envInt("RAIN_DEFAULT_MINUTES", 1)
	if err != nil {
		return err
	}
	maxMin, err := envInt("RAIN_MAX_MINUTES", 60)
	if err != nil {
		return err
	}
	maxTo, err := envInt("RAIN_MAX_RECIPIENTS", 50)
	if err != nil {
		return err
	}
	ttl0, err := envDuration("RAIN_SECRET_TTL", time.Hour)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("bot", flag.ExitOnError)
	rpc := fs.String("rpc", envOr("RAIN_RPC", os.Getenv("ZVM_RPC")), "ZVM JSON-RPC ($RAIN_RPC, else $ZVM_RPC)")
	keyFile := fs.String("keys", os.Getenv("RAIN_KEYS"), "the operator's secrets file ($RAIN_KEYS)")
	record := fs.String("record", os.Getenv("RAIN_RECORD"), "the deployment record ($RAIN_RECORD)")
	dbPath := fs.String("db", envOr("RAIN_DB", "rain.db"), "SQLite database ($RAIN_DB)")
	chats := fs.String("chats", os.Getenv("RAIN_CHATS"), "comma-separated group ids to rain in; empty for any group the bot is added to ($RAIN_CHATS)")
	page := fs.String("page", os.Getenv("RAIN_PAGE"), "Rain's page, which /link, /fund and /revoke link to; empty for explorer steps only ($RAIN_PAGE)")
	public := fs.String("public", envOr("RAIN_PUBLIC", os.Getenv("ZVM_GATEWAY")), "the public origin the explorer is under ($RAIN_PUBLIC, else $ZVM_GATEWAY)")
	tokens := fs.String("tokens", os.Getenv("RAIN_TOKENS"), "NAME=0xaddress pairs /rain accepts besides ZNN ($RAIN_TOKENS)")
	znsRegistry := fs.String("zns-registry", os.Getenv("ZNS_REGISTRY"), "ZNS's ENSRegistry on this chain, for names in messages; empty for addresses only ($ZNS_REGISTRY)")
	defaultMin := fs.Int("default-minutes", defMin, "the window when /rain names none ($RAIN_DEFAULT_MINUTES)")
	maxMinutes := fs.Int("max-minutes", maxMin, "the largest window /rain accepts ($RAIN_MAX_MINUTES)")
	maxRecipients := fs.Int("max-recipients", maxTo, "the most people one rain pays, the most recent speakers first ($RAIN_MAX_RECIPIENTS)")
	ttl := fs.Duration("secret-ttl", ttl0, "how long a /link secret stays good ($RAIN_SECRET_TTL)")
	_ = fs.Parse(args)
	if err := need("rpc", *rpc, "RAIN_RPC or ZVM_RPC"); err != nil {
		return err
	}
	if err := need("public", *public, "RAIN_PUBLIC or ZVM_GATEWAY"); err != nil {
		return err
	}

	token := os.Getenv("RAIN_TELEGRAM_TOKEN")
	if token == "" {
		return errors.New("set RAIN_TELEGRAM_TOKEN")
	}
	if *keyFile == "" || *record == "" {
		return errors.New("set RAIN_KEYS and RAIN_RECORD, or pass -keys and -record")
	}
	if *defaultMin < 1 || *defaultMin > *maxMinutes {
		return fmt.Errorf("RAIN_DEFAULT_MINUTES is %d, and must be between 1 and RAIN_MAX_MINUTES (%d)", *defaultMin, *maxMinutes)
	}
	if *maxRecipients < 1 || *maxRecipients > contracts.MaxRecipients {
		return fmt.Errorf("RAIN_MAX_RECIPIENTS is %d, and the contract pays at most %d", *maxRecipients, contracts.MaxRecipients)
	}
	k, err := keys.Load(*keyFile)
	if err != nil {
		return err
	}
	rec, err := deploy.LoadRecord(*record)
	if err != nil {
		return err
	}
	if rec.Bot != k.BotAddress() {
		return fmt.Errorf("%s names %s as the operator, and these keys are %s", *record, rec.Bot, k.BotAddress())
	}
	allowed, err := idSet(*chats)
	if err != nil {
		return fmt.Errorf("RAIN_CHATS: %w", err)
	}
	named, err := tokenSet(*tokens)
	if err != nil {
		return fmt.Errorf("RAIN_TOKENS: %w", err)
	}

	c, err := chain.Dial(ctx, *rpc, k.Bot)
	if err != nil {
		return err
	}
	if c.ChainID.Uint64() != rec.ChainID {
		return fmt.Errorf("%s is chain %d, the deployment is on %d", *rpc, c.ChainID, rec.ChainID)
	}
	if err := deploy.Check(ctx, c, rec); err != nil {
		return err
	}
	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	log := slog.Default()
	tgc := tg.New(token, log)
	me, err := tgc.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}
	groups := "any"
	if len(allowed) > 0 {
		groups = *chats
	}
	log.Info("bot", "username", me.Username, "operator", c.From, "rain", rec.Rain, "groups", groups)
	if !me.CanReadAll {
		log.Warn("privacy mode is on: in a group where the bot is not an admin it sees only commands, so /rain finds nobody talking; turn it off in @BotFather with /setprivacy")
	}

	bot := rain.New(rain.Config{
		Chain: &rain.OnChain{C: c, Contract: rec.Rain, WZNN: rec.WZNN, API: strings.TrimSuffix(strings.TrimRight(*rpc, "/"), "/rpc")}, WZNN: rec.WZNN, Out: tgc, Store: db,
		Tracker: rain.NewTracker(time.Duration(*maxMinutes) * time.Minute),
		Links:   rain.Links{Explorer: strings.TrimRight(*public, "/") + "/explorer", Rain: rec.Rain, Page: *page, Names: zns.New(c.Eth, common.HexToAddress(*znsRegistry))},
		Tokens:  named, DefaultMinutes: *defaultMin, MaxMinutes: *maxMinutes, MaxRecipients: *maxRecipients,
		SecretTTL: *ttl, Exclude: map[common.Address]bool{rec.Bot: true, rec.Rain: true}, StartBlock: rec.RainBlock, Log: log,
	})
	if err := bot.Recover(ctx); err != nil {
		return fmt.Errorf("settling unfinished rains: %w", err)
	}
	if err := registerCommands(ctx, tgc, *page); err != nil {
		log.Warn("setMyCommands", "err", err)
	}
	go bot.Watch(ctx)

	var offset int64
	for ctx.Err() == nil {
		updates, err := tgc.Updates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Warn("getUpdates", "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			if u.Message == nil || u.Message.From == nil || u.Message.From.IsBot {
				continue
			}
			handle(ctx, tgc, me, bot, allowed, u.Message)
		}
	}
	bot.Wait()
	return nil
}

func handle(ctx context.Context, tgc *tg.Client, me *tg.User, bot *rain.Bot, allowed map[int64]bool, m *tg.Message) {
	text := strings.TrimSpace(m.Text)
	cmd, args := parseCommand(text, me.Username)
	from := rain.Person{ID: m.From.ID, Name: m.From.Name()}
	dm := fmt.Sprintf(`<a href="https://t.me/%s">direct message</a>`, me.Username)
	reply := func(html string) {
		if html == "" {
			return
		}
		if err := tgc.Send(ctx, m.Chat.ID, html); err != nil {
			slog.Warn("send", "chat", m.Chat.ID, "err", err)
		}
	}

	if m.Chat.Type == "private" {
		reply(bot.Direct(ctx, from, cmd, args, text))
		return
	}
	if m.Chat.Type != "group" && m.Chat.Type != "supergroup" {
		return
	}
	if len(allowed) > 0 && !allowed[m.Chat.ID] {
		if cmd == "rain" {
			reply("This bot rains in its own groups only.")
		}
		return
	}
	for _, u := range m.NewChatMembers {
		if u.ID != me.ID {
			continue
		}
		reply("I split tokens between whoever is talking here. <code>/rain 100</code> shares 100 ZNN between everyone linked who spoke in the last minute. To be paid, send me /link in a " + dm + ".")
	}
	// Every message counts as being around, whatever it says. The tracker is
	// keyed by chat, so a rain here only ever considers who spoke here.
	bot.Seen(m.Chat.ID, from, time.Unix(m.Date, 0))
	switch cmd {
	case "rain":
		bot.Rain(ctx, m.Chat.ID, from, args)
	case "help", "start":
		reply("<code>/rain 100</code> splits 100 ZNN between everyone linked who spoke here in the last minute; <code>/rain 100 5</code> over five minutes; <code>/rain 100 qsr</code> in another token. To be paid or to rain, send me /link in a " + dm + ".")
	case "fund", "approve":
		// Funding needs a wallet, not a DM: straight to the page.
		if bot.Links.Page != "" {
			reply(fmt.Sprintf(`Approve Rain for a token on <a href="%s">the Rain page</a>: your wallet signs it there.`, bot.Links.Page))
		} else {
			reply("Send me that in a " + dm + ".")
		}
	case "link", "connect", "wallet", "revoke", "tokens", "unlink":
		reply("Send me that in a " + dm + ".")
	}
}

// parseCommand splits "/rain@bot 50" into ("rain", ["50"]). A command
// addressed to another bot comes back empty.
func parseCommand(text, username string) (string, []string) {
	if !strings.HasPrefix(text, "/") {
		return "", nil
	}
	fields := strings.Fields(text)
	name := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	if at := strings.IndexByte(name, '@'); at >= 0 {
		if !strings.EqualFold(name[at+1:], username) {
			return "", nil
		}
		name = name[:at]
	}
	return name, fields[1:]
}

func idSet(s string) (map[int64]bool, error) {
	out := map[int64]bool{}
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		id, err := strconv.ParseInt(f, 10, 64)
		if err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, nil
}

// tokenSet reads "wQSR=0x…,BULLET=0x…". Names match case-insensitively and
// are shown as written, in the order written.
func tokenSet(s string) ([]rain.Named, error) {
	var out []rain.Named
	seen := map[string]bool{}
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		name, addr, ok := strings.Cut(f, "=")
		name = strings.TrimSpace(name)
		addr = strings.TrimSpace(addr)
		if !ok || name == "" || !common.IsHexAddress(addr) {
			return nil, fmt.Errorf("%q is not NAME=0xaddress", f)
		}
		if strings.EqualFold(name, "ZNN") {
			return nil, errors.New("ZNN is the chain's coin and cannot be renamed")
		}
		if seen[strings.ToUpper(name)] {
			return nil, fmt.Errorf("%s is named twice", name)
		}
		seen[strings.ToUpper(name)] = true
		out = append(out, rain.Named{Name: name, Address: common.HexToAddress(addr)})
	}
	return out, nil
}

// registerCommands sets the "/" menus and the profile text. It runs at every
// start, so the menus always match the commands this build handles.
func registerCommands(ctx context.Context, c *tg.Client, page string) error {
	if page == "" {
		page = "the Rain page (/fund)"
	}
	private := []tg.Command{
		{Command: "link", Description: "link a wallet, to be paid and to rain"},
		{Command: "wallet", Description: "your linked wallet and what Rain may spend"},
		{Command: "fund", Description: "let Rain spend an amount of one token, on the Rain page"},
		{Command: "revoke", Description: "/revoke <token>: take an approval back"},
		{Command: "tokens", Description: "the tokens known by name"},
		{Command: "unlink", Description: "forget your wallet"},
		{Command: "help", Description: "how rain works"},
	}
	group := []tg.Command{
		{Command: "rain", Description: "/rain <amount> [token] [minutes]: split it between who spoke here"},
		{Command: "help", Description: "how rain works"},
	}
	// The default menu is what any chat outside the two scopes shows; it
	// carries both lists so none is ever empty.
	for _, s := range []struct {
		scope string
		cmds  []tg.Command
	}{{"all_private_chats", private}, {"all_group_chats", group}, {"default", append(append([]tg.Command{}, group[:1]...), private...)}} {
		if err := c.SetCommands(ctx, s.scope, s.cmds); err != nil {
			return fmt.Errorf("%s: %w", s.scope, err)
		}
	}
	return c.SetDescription(ctx,
		"Rain splits ZNN, or any token on the ZVM, evenly between everyone who spoke in a Telegram group in the last few minutes.\n\n"+
			"Press Start and send /link to be paid. To send rains, approve Rain for an amount you choose of one token at a time on "+page+"; nothing leaves your wallet until you rain. "+
			"Then, in a group I am in: /rain 100, /rain 50 wqsr, /rain 100 5 for the last five minutes.",
		"Split ZNN or any ZVM token evenly between everyone who spoke in a group lately. /link to be paid.")
}

// need reports a node or origin left unset, and what sets it. None is built
// in: .env.example names a local zvm-devnet's.
func need(flagName, value, env string) error {
	if value == "" {
		return fmt.Errorf("-%s is not set: pass it, or set %s", flagName, env)
	}
	return nil
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// A setting that is present but unreadable is an error, not the default: a
// typo in .env should stop the bot rather than run it with a value nobody set.
func envDuration(name string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return d, nil
}

func envInt(name string, def int) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return n, nil
}

package rain

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// Named is a token the bot knows by a name, from RAIN_TOKENS. The name is what
// people type and what the bot shows, so WZNN reads as WZNN even though WETH9
// calls itself WETH.
type Named struct {
	Name    string
	Address common.Address
}

// Spending is per token and per amount, and nothing else: a sender approves
// Rain on each token they want to rain, for an amount they pick, and nothing
// is deposited. ZNN has no approve, so it is approved as WZNN, which a ZNN
// rain unwraps. Nothing ever asks for an unlimited approval, and /wallet flags
// one if a sender gave it anyway.
var unlimited = new(big.Int).Lsh(big.NewInt(1), 255)

// resolve turns what someone typed into a token: ZNN, a configured name, or
// any ERC-20 by address.
func (b *Bot) resolve(ctx context.Context, s string) (Token, error) {
	if s == "" || strings.EqualFold(s, "znn") {
		return ZNN, nil
	}
	var addr common.Address
	switch {
	case common.IsHexAddress(s):
		addr = common.HexToAddress(s)
	case b.named(s) != nil:
		addr = b.named(s).Address
	default:
		return Token{}, fmt.Errorf("I do not know %s. Use one of %s, or any ERC-20 by its 0x address.", esc(s), strings.Join(b.names(), ", "))
	}
	t, err := b.Chain.Token(ctx, addr)
	if err != nil {
		return Token{}, errors.New(esc(err.Error()))
	}
	for _, n := range b.Tokens {
		if n.Address == addr {
			t.Symbol = n.Name
			break
		}
	}
	return t, nil
}

func (b *Bot) named(s string) *Named {
	for i := range b.Tokens {
		if strings.EqualFold(b.Tokens[i].Name, s) {
			return &b.Tokens[i]
		}
	}
	return nil
}

func (b *Bot) names() []string {
	out := []string{"ZNN"}
	for _, n := range b.Tokens {
		out = append(out, esc(n.Name))
	}
	return out
}

// tokens is /tokens.
func (b *Bot) tokens() string {
	lines := []string{fmt.Sprintf(`<b>ZNN</b>, the chain's coin: approved as <a href="%s">WZNN</a>, paid out as plain ZNN.`, b.Links.ContractOf(b.WZNN))}
	for _, n := range b.Tokens {
		lines = append(lines, fmt.Sprintf(`<b>%s</b>  <a href="%s">%s</a>`, esc(n.Name), b.Links.ContractOf(n.Address), n.Address.Hex()))
	}
	lines = append(lines, "", "Any other ERC-20 works too: use its 0x address wherever a name goes, as in <code>/rain 10 0x…</code>.")
	return strings.Join(lines, "\n")
}

// amountAndToken reads "500 wqsr", "wqsr 500", "500" or "wqsr".
func amountAndToken(args []string) (amount, token string, err error) {
	for _, a := range args {
		if isAmount(a) {
			if amount != "" {
				return "", "", fmt.Errorf("two amounts: %s and %s", esc(amount), esc(a))
			}
			amount = a
			continue
		}
		if token != "" {
			return "", "", fmt.Errorf("two tokens: %s and %s", esc(token), esc(a))
		}
		token = a
	}
	return amount, token, nil
}

// approvedOn is the contract a token's approval lives on: the token itself,
// or WZNN for ZNN.
func (b *Bot) approvedOn(t Token) (common.Address, string) {
	if t.Native() {
		return b.WZNN, "WZNN"
	}
	return t.Address, esc(t.Symbol)
}

// approval is the one call that lets Rain spend v of t, with the amount
// already in base units.
func (b *Bot) approval(ctx context.Context, t Token, v *big.Int) string {
	on, name := b.approvedOn(t)
	spender, amount := b.Chain.ApproveParams(ctx, on)
	return fmt.Sprintf(`On the <a href="%s">%s contract</a> (not the Rain contract: an approval lives on the token), open <b>Write</b>, connect your linked wallet and call <code>approve</code> with

<b>%s</b>  <code>%s</code>  (Rain)
<b>%s</b>  <code>%s</code>

That is %s %s in base units (%d decimals).`, b.Links.ContractOf(on), name, esc(spender), b.Links.Rain.Hex(), esc(amount), v, t.Format(v), name, t.Decimals)
}

// fund is /fund <amount> [token]: the call that lets Rain spend that much of
// that token and nothing else. For ZNN it first says how much ZNN to wrap,
// when the linked wallet does not already hold enough WZNN.
func (b *Bot) fund(ctx context.Context, p Person, args []string) string {
	amount, name, err := amountAndToken(args)
	if err != nil {
		return err.Error()
	}
	if amount == "" && name == "" {
		return b.fundOverview()
	}
	t, err := b.resolve(ctx, name)
	if err != nil {
		return err.Error()
	}
	if amount == "" {
		return fmt.Sprintf("How much? Choose it on %s, or send for example <code>/fund 100 %s</code>.", b.fundLink("the Rain page", &t, nil), esc(strings.ToLower(t.Symbol)))
	}
	v, err := t.Parse(amount)
	if err != nil {
		return esc(err.Error())
	}
	revoke := fmt.Sprintf("<code>/revoke %s</code>", esc(strings.ToLower(t.Symbol)))
	// The page first: one tap, and the wallet signs what is filled in there.
	// The explorer steps follow for anyone who would rather do it by hand.
	what := fmt.Sprintf("Approve %s %s", t.Format(v), esc(t.Symbol))
	if t.Native() {
		what += " (it wraps what you lack first)"
	}
	tap := b.Links.Tap(what, url.Values{"token": {pageKey(t, b.Tokens)}, "amount": {t.Plain(v)}})
	if tap != "" {
		tap += "\n\nBy hand instead:\n\n"
	}
	if !t.Native() {
		return fmt.Sprintf("To let Rain spend up to %s %s:\n\n%s%s\n\nIt replaces any earlier approval of Rain for %s and touches no other token. Nothing leaves your wallet until you /rain, and %s takes it back.",
			t.Format(v), esc(t.Symbol), tap, b.approval(ctx, t, v), esc(t.Symbol), revoke)
	}

	// ZNN: wrap what is missing, then approve WZNN.
	var s strings.Builder
	fmt.Fprintf(&s, "To let Rain spend up to %s ZNN, which it pays out as plain ZNN:\n\n%s", t.Format(v), tap)
	wrap := v
	if u, err := b.Store.User(ctx, p.ID); err == nil && u.Address != "" {
		if held, _, err := b.Chain.Holdings(ctx, t, common.HexToAddress(u.Address)); err == nil {
			if held.Cmp(v) >= 0 {
				wrap = nil
				fmt.Fprintf(&s, "You already hold %s WZNN, which covers it.\n\n", t.Format(held))
			} else {
				wrap = new(big.Int).Sub(v, held)
			}
		}
	}
	step := 1
	if wrap != nil {
		fmt.Fprintf(&s, "<b>1.</b> Wrap %s ZNN: on the <a href=\"%s\">WZNN contract</a>, open <b>Write</b>, find <code>deposit</code> and type <code>%s</code> in its <b>Value (ZNN)</b> field. WZNN is ZNN 1:1, and its <code>withdraw</code> unwraps it.\n\n",
			t.Format(wrap), b.Links.ContractOf(b.WZNN), t.Plain(wrap))
		step = 2
	}
	fmt.Fprintf(&s, "<b>%d.</b> %s\n\nNothing leaves your wallet until you /rain, and %s takes the approval back.", step, b.approval(ctx, t, v), revoke)
	return s.String()
}

func (b *Bot) fundOverview() string {
	if b.Links.Page != "" {
		return fmt.Sprintf(`Rain spends only what you approve it for, one token at a time, and nothing leaves your wallet until you rain.

<b>%s</b>: pick a token and an amount, and your wallet signs the approval there. For ZNN it wraps what you lack first.

/wallet shows what Rain may spend of each token.`, b.fundLink("Open the Rain page", nil, nil))
	}
	example := "wqsr"
	if len(b.Tokens) > 0 {
		example = strings.ToLower(b.Tokens[0].Name)
	}
	return fmt.Sprintf(`Rain spends only what you approve it for, one token at a time, and nothing leaves your wallet until you rain.

<code>/fund 100</code>  let Rain spend 100 ZNN
<code>/fund 500 %s</code>  let Rain spend 500 %s, and no other token
<code>/fund 50 0x…</code>  the same for any ERC-20, by address

I reply with the exact call to make. /tokens lists the names I know; /wallet shows what Rain may spend of each.`,
		esc(example), esc(strings.ToUpper(example)))
}

// fundLink is where the bot sends someone to let Rain spend: the Rain page,
// with the token and amount filled in when they are known. Without a page it
// is the /fund command that does the same by hand.
func (b *Bot) fundLink(text string, t *Token, amount *big.Int) string {
	if b.Links.Page == "" {
		cmd := "/fund"
		if t != nil && amount != nil {
			cmd += " " + t.Plain(amount)
		}
		if t != nil {
			cmd += " " + strings.ToLower(t.Symbol)
		}
		return "<code>" + esc(cmd) + "</code>"
	}
	q := url.Values{}
	if t != nil {
		q.Set("token", pageKey(*t, b.Tokens))
		if amount != nil {
			q.Set("amount", t.Plain(amount))
		}
	}
	u := b.Links.Page
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return fmt.Sprintf(`<a href="%s">%s</a>`, esc(u), text)
}

// revoke is /revoke <token>: an approval of zero.
func (b *Bot) revoke(ctx context.Context, args []string) string {
	if len(args) != 1 {
		return "Which token? For example <code>/revoke wqsr</code>, or <code>/revoke znn</code>."
	}
	t, err := b.resolve(ctx, args[0])
	if err != nil {
		return err.Error()
	}
	on, name := b.approvedOn(t)
	spender, amount := b.Chain.ApproveParams(ctx, on)
	byHand := fmt.Sprintf(`On the <a href="%s">%s contract</a>, open <b>Write</b> and call <code>approve</code> with <b>%s</b> <code>%s</code> and <b>%s</b> <code>0</code>. Rain can then spend none of your %s.`,
		b.Links.ContractOf(on), name, esc(spender), b.Links.Rain.Hex(), esc(amount), esc(t.Symbol))
	if tap := b.Links.Tap("Revoke "+esc(t.Symbol), url.Values{"token": {pageKey(t, b.Tokens)}, "revoke": {"1"}}); tap != "" {
		return tap + "\n\nBy hand instead: " + byHand
	}
	return byHand
}

// pageKey is how the page's ?token= names a token: "znn", a name it knows
// from RAIN_TOKENS, or the address.
func pageKey(t Token, named []Named) string {
	if t.Native() {
		return "znn"
	}
	for _, n := range named {
		if n.Address == t.Address {
			return strings.ToLower(n.Name)
		}
	}
	return t.Address.Hex()
}

// funds is what an address lets Rain spend, token by token.
func (b *Bot) funds(ctx context.Context, a common.Address) string {
	lines := []string{"What Rain may spend:"}
	tokens := []Token{ZNN}
	for _, n := range b.Tokens {
		if n.Address == b.WZNN {
			continue // the same approval as ZNN's
		}
		if t, err := b.resolve(ctx, n.Address.Hex()); err == nil {
			tokens = append(tokens, t)
		}
	}
	for _, t := range tokens {
		bal, ok, err := b.Chain.Holdings(ctx, t, a)
		if err != nil {
			return "Could not read your balances: " + esc(err.Error())
		}
		if ok.Sign() == 0 {
			continue
		}
		_, held := b.approvedOn(t)
		approved := t.Format(ok)
		if ok.Cmp(unlimited) >= 0 {
			approved = fmt.Sprintf("<b>unlimited</b> (safer: %s)", b.fundLink("approve a set amount", &t, bal))
		}
		lines = append(lines, fmt.Sprintf("%s  %s approved, %s %s held", esc(t.Symbol), approved, t.Format(bal), held))
	}
	if len(lines) == 1 {
		lines = append(lines, fmt.Sprintf("nothing yet. To send rains, approve Rain for a token on %s; to be paid, you need nothing more.", b.fundLink("the Rain page", nil, nil)))
	}
	return strings.Join(lines, "\n")
}

// short explains a rain the sender cannot cover, with the one command that
// would fix it. It returns "" when they can.
func (b *Bot) short(ctx context.Context, p Person, t Token, sender common.Address, need *big.Int) string {
	bal, approved, err := b.Chain.Holdings(ctx, t, sender)
	if err != nil {
		return "Could not read your balance: " + esc(err.Error())
	}
	who := Mention(p.ID, p.Name)
	_, held := b.approvedOn(t)
	// The fix is a link to the page with this token and amount filled in, so
	// the sender goes straight from the group to one signature (two for ZNN
	// that still needs wrapping). Without a page, the /fund command.
	fix := b.fundLink(fmt.Sprintf("Approve %s %s on the Rain page", t.Format(need), esc(t.Symbol)), &t, need) + "."
	if b.Links.Page == "" {
		fix = "Send me " + fix[:len(fix)-1] + " in a direct message."
	}
	switch {
	case approved.Cmp(need) < 0:
		return fmt.Sprintf("%s, you have approved Rain for %s %s, and this needs %s. %s",
			who, t.Format(approved), esc(t.Symbol), t.Format(need), fix)
	case bal.Cmp(need) < 0 && t.Native():
		return fmt.Sprintf("%s, you hold %s WZNN, and this needs %s. %s It wraps the ZNN you lack first.",
			who, t.Format(bal), t.Format(need), fix)
	case bal.Cmp(need) < 0:
		return fmt.Sprintf("%s, you hold %s %s, and this needs %s.", who, t.Format(bal), held, t.Format(need))
	}
	return ""
}

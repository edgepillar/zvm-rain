package rain

import (
	"context"
	"fmt"
	"math/big"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/Zenoglyphs-VM/zvm-rain/internal/store"
)

// fakeChain is the contract as a map: who is linked, what each address holds
// and has approved, and every rain sent.
type fakeChain struct {
	mu       sync.Mutex
	linked   map[common.Address]bool
	balance  map[common.Address]*big.Int // of the one token under test (WZNN for ZNN)
	approved map[common.Address]*big.Int
	rains    []fakeRain
	links    []LinkEvent
}

type fakeRain struct {
	id    uint64
	from  common.Address
	token Token
	each  *big.Int
	to    []common.Address
}

func newFake() *fakeChain {
	return &fakeChain{linked: map[common.Address]bool{}, balance: map[common.Address]*big.Int{}, approved: map[common.Address]*big.Int{}}
}

func (f *fakeChain) Linked(_ context.Context, a common.Address) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.linked[a], nil
}

func (f *fakeChain) Holdings(_ context.Context, t Token, a common.Address) (*big.Int, *big.Int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	bal := f.balance[a]
	if bal == nil {
		bal = new(big.Int)
	}
	ok := f.approved[a]
	if ok == nil {
		ok = new(big.Int)
	}
	return bal, ok, nil
}

func (f *fakeChain) Token(_ context.Context, a common.Address) (Token, error) {
	if a == (common.Address{}) {
		return ZNN, nil
	}
	return Token{Address: a, Symbol: "TOK", Decimals: 8}, nil
}

func (f *fakeChain) Rain(_ context.Context, id uint64, from common.Address, t Token, each *big.Int, to []common.Address) (*Paid, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rains = append(f.rains, fakeRain{id, from, t, each, to})
	return &Paid{Tx: common.HexToHash("0xabc")}, nil
}

func (f *fakeChain) Rained(context.Context, uint64) (bool, error) { return false, nil }

func (f *fakeChain) ApproveParams(context.Context, common.Address) (string, string) {
	return "guy", "wad"
}

func (f *fakeChain) ReadLink(_ context.Context, tx common.Hash) (*LinkEvent, error) {
	for _, l := range f.links {
		if l.Tx == tx {
			return &l, nil
		}
	}
	return nil, errNoLink
}

var errNoLink = &chainErr{"no link"}

type chainErr struct{ s string }

func (e *chainErr) Error() string { return e.s }

func (f *fakeChain) Links(_ context.Context, from, to uint64) ([]LinkEvent, uint64, error) {
	var out []LinkEvent
	last := from - 1
	for _, l := range f.links {
		if l.Block >= from && l.Block <= to {
			out = append(out, l)
			last = max(last, l.Block)
		}
	}
	return out, last, nil
}

type recorder struct {
	mu   sync.Mutex
	msgs map[int64][]string
}

func (r *recorder) Send(_ context.Context, chat int64, html string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.msgs == nil {
		r.msgs = map[int64][]string{}
	}
	r.msgs[chat] = append(r.msgs[chat], html)
	return nil
}

func (r *recorder) last(chat int64) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.msgs[chat]
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1]
}

var wqsr = common.HexToAddress("0x4200000000000000000000000000000000000101")

type harness struct {
	t     *testing.T
	bot   *Bot
	chain *fakeChain
	out   *recorder
	db    *store.Store
	now   time.Time
}

func setup(t *testing.T) *harness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "rain.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h := &harness{t: t, chain: newFake(), out: &recorder{}, db: db, now: time.Unix(1_800_000_000, 0)}
	h.bot = New(Config{
		Chain: h.chain, Out: h.out, Store: db, Tracker: NewTracker(time.Hour),
		Links: Links{Explorer: "https://x/explorer", Rain: common.HexToAddress("0x99")}, Tokens: []Named{{Name: "wQSR", Address: wqsr}},
		WZNN:           common.HexToAddress("0x00000000000000000000000000000000000077a7"),
		DefaultMinutes: 1, MaxMinutes: 60, MaxRecipients: 3, Now: func() time.Time { return h.now },
	})
	return h
}

// user links a Telegram id to an address, in the store and on the chain.
func (h *harness) user(id int64, addr string) common.Address {
	a := common.HexToAddress(addr)
	if err := h.db.Link(context.Background(), id, "u", a.Hex(), "0x", h.now); err != nil {
		h.t.Fatal(err)
	}
	h.chain.linked[a] = true
	return a
}

func (h *harness) say(chat, id int64, name string, ago time.Duration) {
	h.bot.Seen(chat, Person{ID: id, Name: name}, h.now.Add(-ago))
}

func (h *harness) rain(chat, from int64, args ...string) string {
	h.bot.Rain(context.Background(), chat, Person{ID: from, Name: "sender"}, args)
	h.bot.Wait()
	return h.out.last(chat)
}

func znn(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), pow10(18)) }

// A rain only ever pays people who spoke in the group it was called in.
func TestRainStaysInItsGroup(t *testing.T) {
	h := setup(t)
	sender := h.user(1, "0x10")
	h.chain.balance[sender], h.chain.approved[sender] = znn(100), znn(100)
	a := h.user(2, "0x20")
	h.user(3, "0x30") // speaks only in the other group
	h.say(-100, 2, "alice", 10*time.Second)
	h.say(-200, 3, "bob", 5*time.Second)
	h.say(-100, 1, "sender", 0)

	msg := h.rain(-100, 1, "10")
	if len(h.chain.rains) != 1 {
		t.Fatalf("rains = %d, message %q", len(h.chain.rains), msg)
	}
	r := h.chain.rains[0]
	if len(r.to) != 1 || r.to[0] != a {
		t.Fatalf("paid %v, want only alice %s", r.to, a)
	}
	if r.each.Cmp(znn(10)) != 0 {
		t.Fatalf("each = %s", r.each)
	}
	if strings.Contains(msg, "bob") {
		t.Fatalf("message names someone from another group: %q", msg)
	}
}

func TestNobodySpoke(t *testing.T) {
	h := setup(t)
	sender := h.user(1, "0x10")
	h.chain.balance[sender], h.chain.approved[sender] = znn(100), znn(100)
	h.say(-100, 1, "sender", 0)
	h.say(-100, 2, "alice", 2*time.Minute) // outside the default minute

	msg := h.rain(-100, 1, "10")
	if len(h.chain.rains) != 0 {
		t.Fatal("rained on nobody")
	}
	if !strings.Contains(msg, "Nobody else has spoken") || !strings.Contains(msg, "Nothing was spent") {
		t.Fatalf("message %q", msg)
	}
}

func TestNobodyLinked(t *testing.T) {
	h := setup(t)
	sender := h.user(1, "0x10")
	h.chain.balance[sender], h.chain.approved[sender] = znn(100), znn(100)
	h.say(-100, 2, "alice", time.Second)
	h.say(-100, 3, "bob", 2*time.Second)

	msg := h.rain(-100, 1, "10")
	if len(h.chain.rains) != 0 {
		t.Fatal("rained with nobody linked")
	}
	for _, want := range []string{"has linked a wallet yet", "Nothing was spent", "alice", "bob", "/link"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message lacks %q: %q", want, msg)
		}
	}
	var n int
	if err := h.dbCount(&n); err != nil || n != 0 {
		t.Fatalf("recorded %d rains (%v)", n, err)
	}
}

func (h *harness) dbCount(n *int) error {
	ids, err := h.db.Sending(context.Background())
	*n = len(ids)
	return err
}

// An address that unlinked on the contract is not paid, and does not sink
// the rain for everyone else.
func TestUnlinkedOnChainIsSkipped(t *testing.T) {
	h := setup(t)
	sender := h.user(1, "0x10")
	h.chain.balance[sender], h.chain.approved[sender] = znn(100), znn(100)
	a := h.user(2, "0x20")
	b := h.user(3, "0x30")
	h.chain.linked[b] = false
	h.say(-100, 2, "alice", time.Second)
	h.say(-100, 3, "bob", time.Second)
	h.rain(-100, 1, "10")
	if len(h.chain.rains) != 1 || len(h.chain.rains[0].to) != 1 || h.chain.rains[0].to[0] != a {
		t.Fatalf("rains %+v", h.chain.rains)
	}
}

// The split is even, the remainder stays with the sender, recipients reach
// the contract ascending, and a cap keeps the most recent speakers.
func TestSplitAndCap(t *testing.T) {
	h := setup(t)
	sender := h.user(1, "0x10")
	h.chain.balance[sender], h.chain.approved[sender] = znn(100), znn(100)
	h.user(2, "0x50")
	h.user(3, "0x40")
	h.user(4, "0x30")
	h.user(5, "0x20") // the oldest of four, over the cap of 3
	h.say(-100, 2, "a", 1*time.Second)
	h.say(-100, 3, "b", 2*time.Second)
	h.say(-100, 4, "c", 3*time.Second)
	h.say(-100, 5, "d", 4*time.Second)

	msg := h.rain(-100, 1, "10")
	r := h.chain.rains[0]
	want := []common.Address{common.HexToAddress("0x30"), common.HexToAddress("0x40"), common.HexToAddress("0x50")}
	if len(r.to) != 3 || r.to[0] != want[0] || r.to[1] != want[1] || r.to[2] != want[2] {
		t.Fatalf("to = %v", r.to)
	}
	if got := ZNN.Format(r.each); got != "3.333333" {
		t.Fatalf("each = %s", got)
	}
	if !strings.Contains(msg, "did not split evenly") || !strings.Contains(msg, "1 more linked person") {
		t.Fatalf("message %q", msg)
	}
}

// A token rain the sender has not approved enough for is refused before
// sending, with the one command that would cover it.
func TestTokenNeedsApproval(t *testing.T) {
	h := setup(t)
	sender := h.user(1, "0x10")
	h.user(2, "0x20")
	h.say(-100, 2, "alice", time.Second)
	h.chain.balance[sender] = big.NewInt(1_000 * 1e8)
	h.chain.approved[sender] = big.NewInt(5 * 1e8)

	msg := h.rain(-100, 1, "50", "wqsr")
	if len(h.chain.rains) != 0 {
		t.Fatal("sent an uncovered rain")
	}
	if !strings.Contains(msg, "<code>/fund 50 wqsr</code>") || !strings.Contains(msg, "approved Rain for 5 wQSR") {
		t.Fatalf("message %q", msg)
	}

	h.chain.approved[sender] = big.NewInt(50 * 1e8)
	msg = h.rain(-100, 1, "wqsr", "50")
	if len(h.chain.rains) != 1 || h.chain.rains[0].token.Address != wqsr {
		t.Fatalf("rains %+v, message %q", h.chain.rains, msg)
	}
	if !strings.Contains(msg, "<b>50 wQSR</b> each") {
		t.Fatalf("message %q", msg)
	}
}

func TestSenderMustLink(t *testing.T) {
	h := setup(t)
	h.say(-100, 2, "alice", time.Second)
	if msg := h.rain(-100, 1, "10"); !strings.Contains(msg, "link a wallet first") {
		t.Fatalf("message %q", msg)
	}
}

// /fund for a token gives the approve call in base units, for that token.
func TestFundGivesExactApproval(t *testing.T) {
	h := setup(t)
	msg := h.bot.Direct(context.Background(), Person{ID: 1}, "fund", []string{"500", "wqsr"}, "")
	for _, want := range []string{"approve", "50000000000", "0x0000000000000000000000000000000000000099", "wQSR", "/revoke wqsr"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("lacks %q: %q", want, msg)
		}
	}
	// ZNN: wrap what the linked wallet lacks, then approve WZNN for the whole.
	a := h.user(1, "0x10")
	h.chain.balance[a] = znn(1)
	msg = h.bot.Direct(context.Background(), Person{ID: 1}, "fund", []string{"1.5"}, "")
	for _, want := range []string{"Wrap 0.5 ZNN", "<code>0.5</code>", "WZNN contract", "1500000000000000000", "/revoke znn"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("znn lacks %q: %q", want, msg)
		}
	}
	h.chain.balance[a] = znn(2)
	msg = h.bot.Direct(context.Background(), Person{ID: 1}, "fund", []string{"1.5"}, "")
	if strings.Contains(msg, "Wrap") || !strings.Contains(msg, "already hold 2 WZNN") {
		t.Fatalf("znn, enough WZNN: %q", msg)
	}
}

// The watcher links whoever sends a pending secret, and a second address
// sending the same secret does not take the link over.
func TestWatcherLinks(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	h.bot.Direct(ctx, Person{ID: 7, Name: "carol"}, "link", nil, "")
	secret, _, err := h.db.Secret(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	mine, theirs := common.HexToAddress("0x77"), common.HexToAddress("0x88")
	h.bot.watched(ctx, &LinkEvent{Account: mine, Secret: uint64(secret), Tx: common.HexToHash("0x1"), Block: 1})
	h.bot.watched(ctx, &LinkEvent{Account: theirs, Secret: uint64(secret), Tx: common.HexToHash("0x2"), Block: 2})
	u, err := h.db.User(ctx, 7)
	if err != nil || common.HexToAddress(u.Address) != mine {
		t.Fatalf("user %+v, %v", u, err)
	}
	if !strings.Contains(h.out.last(7), "Another address") {
		t.Fatalf("no warning: %q", h.out.last(7))
	}
}

func TestParseRequest(t *testing.T) {
	for _, c := range []struct {
		in    string
		token string
		mins  int
		bad   bool
	}{
		{"100", "", 0, false},
		{"100 5", "", 5, false},
		{"100 qsr", "qsr", 0, false},
		{"100 5m wqsr", "wqsr", 5, false},
		{"100 wqsr 10min", "wqsr", 10, false},
		{"100 a b", "", 0, true},
		{"100 0", "", 0, true},
		{"", "", 0, true},
	} {
		r, err := ParseRequest(strings.Fields(c.in))
		if c.bad {
			if err == nil {
				t.Errorf("%q: no error", c.in)
			}
			continue
		}
		if err != nil || r.Token != c.token || r.Minutes != c.mins {
			t.Errorf("%q: %+v %v", c.in, r, err)
		}
	}
}

func TestAmounts(t *testing.T) {
	tok := Token{Symbol: "T", Decimals: 8}
	v, err := tok.Parse("1,234.5")
	if err != nil || v.Int64() != 123_450_000_000 {
		t.Fatal(v, err)
	}
	if s := tok.Format(v); s != "1,234.5" {
		t.Fatal(s)
	}
	if s := tok.Plain(v); s != "1234.5" {
		t.Fatal(s)
	}
	if _, err := tok.Parse("0.000000001"); err == nil {
		t.Fatal("accepted 9 decimals")
	}
	if s := ZNN.Format(big.NewInt(1)); s != "0.000000000000000001" {
		t.Fatal(s)
	}
}

func TestHumanDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		time.Hour: "1h", 90 * time.Minute: "1h30m", 10 * time.Minute: "10m", 45 * time.Second: "45s", 2*time.Hour + 5*time.Second: "2h0m5s",
	} {
		if got := humanDuration(d); got != want {
			t.Errorf("%s: %q, want %q", d, got, want)
		}
	}
}

// With a page configured, /link and /fund lead with a link that has the work
// filled in, for the browser and for MetaMask's app.
func TestPageLinks(t *testing.T) {
	h := setup(t)
	h.bot.Links.Page = "https://devnet.example.org/rain/"
	ctx := context.Background()
	msg := h.bot.Direct(ctx, Person{ID: 1, Name: "sol"}, "link", nil, "")
	secret, _, _ := h.db.Secret(ctx, 1)
	for _, want := range []string{
		fmt.Sprintf(`href="https://devnet.example.org/rain/?link=%d"`, secret),
		fmt.Sprintf(`href="https://metamask.app.link/dapp/devnet.example.org/rain/?link=%d"`, secret),
		"By hand instead",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("/link lacks %q: %q", want, msg)
		}
	}
	msg = h.bot.Direct(ctx, Person{ID: 1}, "fund", []string{"500", "wqsr"}, "")
	if !strings.Contains(msg, `rain/?amount=500&amp;token=wqsr"`) || !strings.Contains(msg, "Approve 500 wQSR") {
		t.Fatalf("/fund: %q", msg)
	}
	msg = h.bot.Direct(ctx, Person{ID: 1}, "fund", []string{"1.5"}, "")
	if !strings.Contains(msg, `rain/?amount=1.5&amp;token=znn"`) || !strings.Contains(msg, "wraps what you lack") {
		t.Fatalf("/fund znn: %q", msg)
	}
	msg = h.bot.Direct(ctx, Person{ID: 1}, "revoke", []string{"wqsr"}, "")
	if !strings.Contains(msg, `rain/?revoke=1&amp;token=wqsr"`) {
		t.Fatalf("/revoke: %q", msg)
	}
}

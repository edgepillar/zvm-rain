package rain

import (
	"context"
	"errors"
	"math/big"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/Zenoglyphs-VM/zvm-rain/internal/chain"
	"github.com/Zenoglyphs-VM/zvm-rain/internal/contracts"
	"github.com/Zenoglyphs-VM/zvm-rain/internal/keys"
)

// TestLive runs the contract through the bot's own chain code on a real node:
// a fresh Rain, fresh keys funded from the devnet wallet, and every rule the
// contract enforces. It spends devnet ZNN, so it runs only with
// RAIN_LIVE_RPC set, against an anvil fork of chain 69 first:
//
//	RAIN_LIVE_RPC=http://127.0.0.1:48646 RAIN_LIVE_WZNN=0x… go test ./internal/rain -run Live -v
//
// RAIN_LIVE_WZNN is that chain's wrapped ZNN, uniswapV2.weth on its node's /api/info.
func TestLive(t *testing.T) {
	url := os.Getenv("RAIN_LIVE_RPC")
	if url == "" {
		t.Skip("RAIN_LIVE_RPC not set")
	}
	ctx := context.Background()
	if !common.IsHexAddress(os.Getenv("RAIN_LIVE_WZNN")) {
		t.Fatal("RAIN_LIVE_WZNN: the chain's wrapped ZNN")
	}
	wznn := common.HexToAddress(os.Getenv("RAIN_LIVE_WZNN"))
	funder, err := chain.Dial(ctx, url, keys.DevEVMKey(900))
	if err != nil {
		t.Fatal(err)
	}
	type acct struct {
		c    *chain.Client
		addr common.Address
	}
	fresh := func(znn int64) acct {
		k, err := crypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		c, err := chain.Dial(ctx, url, k)
		if err != nil {
			t.Fatal(err)
		}
		to := c.From
		if _, err := funder.Send(ctx, &to, znnWei(znn), nil); err != nil {
			t.Fatal(err)
		}
		return acct{c, c.From}
	}
	send := func(a acct, to common.Address, value *big.Int, data []byte) error {
		_, err := a.c.Send(ctx, &to, value, data)
		return err
	}
	mustSend := func(a acct, to common.Address, value *big.Int, data []byte) {
		t.Helper()
		if err := send(a, to, value, data); err != nil {
			t.Fatal(err)
		}
	}
	wantRevert := func(err error, reason string) {
		t.Helper()
		var rev *chain.RevertError
		if !errors.As(err, &rev) || !strings.Contains(rev.Reason, reason) {
			t.Fatalf("want a revert %q, got %v", reason, err)
		}
	}
	balance := func(a common.Address) *big.Int {
		v, err := funder.Eth.BalanceAt(ctx, a, nil)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	wznnOf := func(a common.Address) *big.Int {
		out, err := funder.Call(ctx, wznn, contracts.ERC20.Pack("balanceOf", a))
		if err != nil {
			t.Fatal(err)
		}
		return new(big.Int).SetBytes(out)
	}

	op := fresh(5)
	data, err := contracts.Rain.DeployData(op.addr, wznn)
	if err != nil {
		t.Fatal(err)
	}
	r, err := op.c.Send(ctx, nil, nil, data)
	if err != nil {
		t.Fatal(err)
	}
	rain := r.ContractAddress
	oc := &OnChain{C: op.c, Contract: rain, WZNN: wznn}
	t.Logf("Rain at %s", rain)

	// ZNN sent straight to Rain is refused: there is nothing to deposit.
	sender := fresh(10)
	wantRevert(send(sender, rain, znnWei(1), nil), "approve instead")

	a, b, c := fresh(1), fresh(1), fresh(1)
	mustSend(a, rain, nil, contracts.Rain.Pack("link", uint64(11)))
	mustSend(b, rain, nil, contracts.Rain.Pack("link", uint64(22)))
	mustSend(sender, rain, nil, contracts.Rain.Pack("link", uint64(33)))

	// The watcher's view: three links, with their secrets.
	evs, _, err := oc.Links(ctx, r.BlockNumber.Uint64(), r.BlockNumber.Uint64()+1000)
	if err != nil || len(evs) != 3 {
		t.Fatalf("links %v %v", evs, err)
	}
	if got, err := oc.ReadLink(ctx, evs[2].Tx); err != nil || got.Account != sender.addr || got.Secret != 33 {
		t.Fatalf("ReadLink %+v %v", got, err)
	}

	to := []common.Address{a.addr, b.addr}
	sort.Slice(to, func(i, j int) bool { return to[i].Cmp(to[j]) < 0 })

	// Nothing approved yet, so nothing can be rained.
	_, err = oc.Rain(ctx, 1, sender.addr, ZNN, znnWei(1), to)
	wantRevert(err, "token transfer failed")

	// Wrap 3, approve 2: a ZNN rain of 1 each unwraps and pays plain ZNN.
	mustSend(sender, wznn, znnWei(3), contracts.ERC20.Pack("deposit"))
	mustSend(sender, wznn, nil, contracts.ERC20.Pack("approve", rain, znnWei(2)))
	if held, ok, err := oc.Holdings(ctx, ZNN, sender.addr); err != nil || held.Cmp(znnWei(3)) != 0 || ok.Cmp(znnWei(2)) != 0 {
		t.Fatalf("holdings %s %s %v", held, ok, err)
	}
	aBefore := balance(a.addr)
	paid, err := oc.Rain(ctx, 1, sender.addr, ZNN, znnWei(1), to)
	if err != nil {
		t.Fatal(err)
	}
	if got := new(big.Int).Sub(balance(a.addr), aBefore); got.Cmp(znnWei(1)) != 0 || len(paid.Wrapped) != 0 {
		t.Fatalf("a got %s ZNN, wrapped %v", got, paid.Wrapped)
	}
	if held, ok, _ := oc.Holdings(ctx, ZNN, sender.addr); held.Cmp(znnWei(1)) != 0 || ok.Sign() != 0 {
		t.Fatalf("after: %s WZNN held, %s approved", held, ok)
	}
	if bal := balance(rain); bal.Sign() != 0 {
		t.Fatalf("Rain kept %s ZNN", bal)
	}

	// The approval is spent; the WZNN still held is out of reach.
	_, err = oc.Rain(ctx, 2, sender.addr, ZNN, znnWei(1), to[:1])
	wantRevert(err, "token transfer failed")

	// Refusals, whatever the operator asks.
	mustSend(sender, wznn, nil, contracts.ERC20.Pack("approve", rain, znnWei(1)))
	_, err = oc.Rain(ctx, 1, sender.addr, ZNN, big.NewInt(1), to)
	wantRevert(err, "rain already paid")
	_, err = oc.Rain(ctx, 3, sender.addr, ZNN, big.NewInt(1), []common.Address{to[1], to[0]})
	wantRevert(err, "recipients not ascending")
	_, err = oc.Rain(ctx, 4, sender.addr, ZNN, big.NewInt(1), []common.Address{c.addr})
	wantRevert(err, "recipient not linked")
	_, err = oc.Rain(ctx, 5, sender.addr, ZNN, big.NewInt(1), []common.Address{sender.addr})
	wantRevert(err, "sender among recipients")
	err = send(sender, rain, nil, contracts.Rain.Pack("rain", uint64(6), sender.addr, common.Address{}, big.NewInt(1), to))
	wantRevert(err, "not the operator")

	// A linked contract with no receive refuses plain ZNN; it is paid its
	// share in WZNN, and the rest of the rain goes through.
	code, err := os.ReadFile("testdata/refuser.hex")
	if err != nil {
		t.Fatal(err)
	}
	arg := common.LeftPadBytes(rain.Bytes(), 32)
	rr, err := sender.c.Send(ctx, nil, nil, append(hexutil.MustDecode("0x"+strings.TrimSpace(string(code))), arg...))
	if err != nil {
		t.Fatal(err)
	}
	refuser := rr.ContractAddress
	mixed := []common.Address{a.addr, refuser}
	sort.Slice(mixed, func(i, j int) bool { return mixed[i].Cmp(mixed[j]) < 0 })
	quarter := new(big.Int).Quo(znnWei(1), big.NewInt(4))
	paid, err = oc.Rain(ctx, 10, sender.addr, ZNN, quarter, mixed)
	if err != nil {
		t.Fatal(err)
	}
	if len(paid.Wrapped) != 1 || paid.Wrapped[0] != refuser || wznnOf(refuser).Cmp(quarter) != 0 || balance(refuser).Sign() != 0 {
		t.Fatalf("refuser: wrapped %v, %s WZNN, %s ZNN", paid.Wrapped, wznnOf(refuser), balance(refuser))
	}

	// A token, by its own approval: WZNN rained as WZNN, and no more than
	// approved.
	tok, err := oc.Token(ctx, wznn)
	if err != nil || tok.Decimals != 18 {
		t.Fatalf("token %+v %v", tok, err)
	}
	bBefore := wznnOf(b.addr)
	if _, err := oc.Rain(ctx, 7, sender.addr, tok, quarter, to); err != nil {
		t.Fatal(err)
	}
	if got := new(big.Int).Sub(wznnOf(b.addr), bBefore); got.Cmp(quarter) != 0 {
		t.Fatalf("b got %s WZNN", got)
	}
	_, err = oc.Rain(ctx, 8, sender.addr, tok, quarter, to)
	wantRevert(err, "token transfer failed")

	// Unlinking is a kill switch, whatever is approved.
	mustSend(sender, wznn, nil, contracts.ERC20.Pack("approve", rain, znnWei(1)))
	mustSend(sender, rain, nil, contracts.Rain.Pack("unlink"))
	_, err = oc.Rain(ctx, 9, sender.addr, ZNN, big.NewInt(1), to)
	wantRevert(err, "sender not linked")

	for _, id := range []uint64{1, 7, 10} {
		if ok, _ := oc.Rained(ctx, id); !ok {
			t.Fatalf("rain %d not recorded", id)
		}
	}
	if ok, _ := oc.Rained(ctx, 2); ok {
		t.Fatal("a refused rain was recorded as paid")
	}
}

func znnWei(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), pow10(18)) }

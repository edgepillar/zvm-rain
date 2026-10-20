package rain

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/Zenoglyphs-VM/zvm-rain/internal/chain"
	"github.com/Zenoglyphs-VM/zvm-rain/internal/contracts"
)

// Chain is everything the bot asks of the ZVM, so the bot can be tested
// without one.
type Chain interface {
	Linked(ctx context.Context, a common.Address) (bool, error)
	// Holdings is what a rain could draw on: the owner's balance of a token
	// and what they approved Rain to spend of it. For ZNN both are of WZNN,
	// which a ZNN rain unwraps.
	Holdings(ctx context.Context, t Token, owner common.Address) (balance, approved *big.Int, err error)
	Token(ctx context.Context, a common.Address) (Token, error)
	Rain(ctx context.Context, id uint64, from common.Address, t Token, each *big.Int, to []common.Address) (*Paid, error)
	Rained(ctx context.Context, id uint64) (bool, error)
	ReadLink(ctx context.Context, tx common.Hash) (*LinkEvent, error)
	// Links returns the Linked events in [from, to], and to is capped at the
	// head; it reports the last block it covered.
	Links(ctx context.Context, from, to uint64) ([]LinkEvent, uint64, error)
	// ApproveParams names a token's approve inputs as the explorer shows them.
	ApproveParams(ctx context.Context, token common.Address) (spender, amount string)
}

// LinkEvent is a link(secret) that landed.
type LinkEvent struct {
	Account common.Address
	Secret  uint64
	Tx      common.Hash
	Block   uint64
}

// Paid is a rain that landed. Wrapped lists recipients who refused plain ZNN
// and were paid the same amount in WZNN instead.
type Paid struct {
	Tx      common.Hash
	Wrapped []common.Address
}

// OnChain is Chain over a JSON-RPC client.
type OnChain struct {
	C        *chain.Client
	Contract common.Address
	// WZNN is wrapped ZNN, which ZNN rains draw on: the contract's wznn().
	WZNN common.Address
	// API is the zvm node's API base, where verified ABIs are read; "" uses
	// the standard names.
	API string

	mu     sync.Mutex
	tokens map[common.Address]Token
	params map[common.Address][2]string
}

func (o *OnChain) call(ctx context.Context, to common.Address, a *contracts.Artifact, method string, args ...any) ([]any, error) {
	out, err := o.C.Call(ctx, to, a.Pack(method, args...))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	return a.ABI.Unpack(method, out)
}

func (o *OnChain) Linked(ctx context.Context, a common.Address) (bool, error) {
	v, err := o.call(ctx, o.Contract, contracts.Rain, "linked", a)
	if err != nil {
		return false, err
	}
	return v[0].(bool), nil
}

func (o *OnChain) Holdings(ctx context.Context, t Token, owner common.Address) (*big.Int, *big.Int, error) {
	token := t.Address
	if t.Native() {
		token = o.WZNN
	}
	b, err := o.call(ctx, token, contracts.ERC20, "balanceOf", owner)
	if err != nil {
		return nil, nil, err
	}
	a, err := o.call(ctx, token, contracts.ERC20, "allowance", owner, o.Contract)
	if err != nil {
		return nil, nil, err
	}
	return b[0].(*big.Int), a[0].(*big.Int), nil
}

// Token reads a token's symbol and decimals once and keeps them.
func (o *OnChain) Token(ctx context.Context, a common.Address) (Token, error) {
	if a == (common.Address{}) {
		return ZNN, nil
	}
	o.mu.Lock()
	t, ok := o.tokens[a]
	o.mu.Unlock()
	if ok {
		return t, nil
	}
	code, err := o.C.Eth.CodeAt(ctx, a, nil)
	if err != nil {
		return Token{}, err
	}
	if len(code) == 0 {
		return Token{}, fmt.Errorf("%s is not a contract", a.Hex())
	}
	d, err := o.call(ctx, a, contracts.ERC20, "decimals")
	if err != nil {
		return Token{}, fmt.Errorf("%s does not look like a token: %w", a.Hex(), err)
	}
	t = Token{Address: a, Decimals: d[0].(uint8), Symbol: a.Hex()[:8]}
	if s, err := o.call(ctx, a, contracts.ERC20, "symbol"); err == nil {
		if sym := strings.TrimSpace(s[0].(string)); sym != "" && len(sym) <= 16 {
			t.Symbol = sym
		}
	}
	o.mu.Lock()
	if o.tokens == nil {
		o.tokens = map[common.Address]Token{}
	}
	o.tokens[a] = t
	o.mu.Unlock()
	return t, nil
}

func (o *OnChain) Rain(ctx context.Context, id uint64, from common.Address, t Token, each *big.Int, to []common.Address) (*Paid, error) {
	dst := o.Contract
	r, err := o.C.Send(ctx, &dst, nil, contracts.Rain.Pack("rain", id, from, t.Address, each, to))
	if err != nil {
		return nil, err
	}
	p := &Paid{Tx: r.TxHash}
	wrapped := contracts.Rain.ABI.Events["PaidWrapped"].ID
	for _, l := range r.Logs {
		if l.Address == o.Contract && len(l.Topics) == 3 && l.Topics[0] == wrapped {
			p.Wrapped = append(p.Wrapped, common.BytesToAddress(l.Topics[2].Bytes()))
		}
	}
	return p, nil
}

func (o *OnChain) Rained(ctx context.Context, id uint64) (bool, error) {
	v, err := o.call(ctx, o.Contract, contracts.Rain, "rained", id)
	if err != nil {
		return false, err
	}
	return v[0].(bool), nil
}

var linkedID = contracts.Rain.ABI.Events["Linked"].ID

func (o *OnChain) linkEvent(l *types.Log) (*LinkEvent, bool) {
	if l.Address != o.Contract || len(l.Topics) != 2 || l.Topics[0] != linkedID {
		return nil, false
	}
	v, err := contracts.Rain.ABI.Unpack("Linked", l.Data)
	if err != nil || len(v) != 1 {
		return nil, false
	}
	return &LinkEvent{Account: common.BytesToAddress(l.Topics[1].Bytes()), Secret: v[0].(uint64), Tx: l.TxHash, Block: l.BlockNumber}, true
}

// ReadLink finds the Linked event in a transaction a user sent the bot. The
// account is the event's, which is msg.sender, rather than the transaction's
// signer, because a Zenon account calling through the native lane has no EVM
// signature.
func (o *OnChain) ReadLink(ctx context.Context, tx common.Hash) (*LinkEvent, error) {
	r, err := o.C.Eth.TransactionReceipt(ctx, tx)
	if errors.Is(err, ethereum.NotFound) {
		return nil, errors.New("that transaction has not landed yet, or is not on this chain")
	}
	if err != nil {
		return nil, err
	}
	if r.Status != types.ReceiptStatusSuccessful {
		return nil, errors.New("that transaction failed")
	}
	for _, l := range r.Logs {
		if ev, ok := o.linkEvent(l); ok {
			return ev, nil
		}
	}
	return nil, errors.New("that transaction does not call link on the Rain contract")
}

func (o *OnChain) Links(ctx context.Context, from, to uint64) ([]LinkEvent, uint64, error) {
	head, err := o.C.Eth.BlockNumber(ctx)
	if err != nil {
		return nil, 0, err
	}
	if to > head {
		to = head
	}
	if from > to {
		return nil, to, nil
	}
	logs, err := o.C.Eth.FilterLogs(ctx, ethereum.FilterQuery{
		FromBlock: new(big.Int).SetUint64(from), ToBlock: new(big.Int).SetUint64(to),
		Addresses: []common.Address{o.Contract}, Topics: [][]common.Hash{{linkedID}},
	})
	if err != nil {
		return nil, 0, err
	}
	var out []LinkEvent
	for i := range logs {
		if ev, ok := o.linkEvent(&logs[i]); ok {
			out = append(out, *ev)
		}
	}
	return out, to, nil
}

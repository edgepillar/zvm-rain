package rain

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// Token is something a rain can pay in. ZNN, the chain's coin, is the zero
// address, as it is in the contract.
type Token struct {
	Address  common.Address
	Symbol   string
	Decimals uint8
}

// ZNN has 18 decimals on the ZVM, like ETH on Ethereum, not the 8 it has on
// Network of Momentum.
var ZNN = Token{Symbol: "ZNN", Decimals: 18}

func (t Token) Native() bool { return t.Address == (common.Address{}) }

func pow10(d uint8) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(d)), nil) }

// Format writes base units as a decimal with thousands grouped and no trailing
// zeros. Shares below a millionth are shown to six significant digits, since
// a split of a small rain over many people is exactly where they occur.
func (t Token) Format(v *big.Int) string {
	if v == nil {
		return "0"
	}
	q, r := new(big.Int).QuoRem(v, pow10(t.Decimals), new(big.Int))
	whole := q.String()
	var b strings.Builder
	for i, c := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	s := b.String()
	if r.Sign() != 0 {
		frac := fmt.Sprintf("%0*s", int(t.Decimals), r.String())
		// Keep at most six digits after the first significant one.
		first := strings.IndexFunc(frac, func(c rune) bool { return c != '0' })
		if keep := first + 6; keep < len(frac) {
			frac = frac[:keep]
		}
		if frac = strings.TrimRight(frac, "0"); frac != "" {
			s += "." + frac
		}
	}
	return s
}

// Plain writes base units exactly and without grouping, for a command to copy.
func (t Token) Plain(v *big.Int) string {
	s := new(big.Rat).SetFrac(v, pow10(t.Decimals)).FloatString(int(t.Decimals))
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// Parse reads an amount such as "50", "12.5" or "1,000" into base units.
func (t Token) Parse(s string) (*big.Int, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	r, ok := new(big.Rat).SetString(s)
	if !ok || r.Sign() <= 0 || strings.ContainsAny(s, "/eE") {
		return nil, fmt.Errorf("%q is not an amount", s)
	}
	r.Mul(r, new(big.Rat).SetInt(pow10(t.Decimals)))
	if !r.IsInt() {
		return nil, fmt.Errorf("%s has %d decimals, and %q has more", t.Symbol, t.Decimals, s)
	}
	return r.Num(), nil
}

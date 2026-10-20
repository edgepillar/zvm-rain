package rain

import (
	"errors"
	"fmt"
	"html"
	"math/big"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// Request is a parsed /rain: an amount, and optionally a token and a window.
//
//	/rain 100              100 ZNN over whoever spoke in the last minute
//	/rain 100 5            over the last five minutes
//	/rain 50 wqsr          in wQSR
//	/rain wqsr 50 10m      the same over ten minutes; the order is free
//	/rain 10 0xabc…        any ERC-20, by address
//
// The first number is the amount, a second number (with or without an "m")
// the window in minutes, and anything that is not a number the token.
type Request struct {
	Amount  string
	Token   string // empty: ZNN
	Minutes int    // 0: the default
}

var errUsage = errors.New("Say how much: <code>/rain 100</code> for ZNN, <code>/rain 50 wqsr</code> for a token, <code>/rain 100 5</code> over five minutes.")

func ParseRequest(args []string) (*Request, error) {
	r := &Request{}
	for _, a := range args {
		switch {
		case r.Amount == "" && isAmount(a):
			r.Amount = a
		case r.Amount != "" && isMinutes(a):
			if r.Minutes != 0 {
				return nil, fmt.Errorf("Two windows: %s and %d minutes.", html.EscapeString(a), r.Minutes)
			}
			m, _ := minutes(a)
			if m <= 0 {
				return nil, errors.New("The window is at least a minute.")
			}
			r.Minutes = m
		case !isAmount(a):
			if r.Token != "" {
				return nil, fmt.Errorf("Two tokens: %s and %s.", html.EscapeString(r.Token), html.EscapeString(a))
			}
			r.Token = a
		default:
			return nil, fmt.Errorf("%s is neither a token nor a window in minutes.", html.EscapeString(a))
		}
	}
	if r.Amount == "" {
		return nil, errUsage
	}
	return r, nil
}

func isAmount(s string) bool {
	if common.IsHexAddress(s) {
		return false
	}
	_, ok := new(big.Rat).SetString(strings.ReplaceAll(s, ",", ""))
	return ok
}

func isMinutes(s string) bool {
	_, ok := minutes(s)
	return ok
}

func minutes(s string) (int, bool) {
	s = strings.ToLower(s)
	s = strings.TrimSuffix(s, "min")
	s = strings.TrimSuffix(s, "m")
	n, err := strconv.Atoi(s)
	return n, err == nil
}

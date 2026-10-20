package rain

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// ApproveParams names approve's two inputs the way the explorer's Write tab
// shows them for one token. They differ: WZNN, being WETH9, calls them guy
// and wad, and an instruction that says "spender" beside a field labelled
// "guy" is one people stop at.
func (o *OnChain) ApproveParams(ctx context.Context, token common.Address) (string, string) {
	o.mu.Lock()
	if p, ok := o.params[token]; ok {
		o.mu.Unlock()
		return p[0], p[1]
	}
	o.mu.Unlock()
	spender, amount := "spender", "amount"
	if o.API != "" {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(o.API, "/")+"/api/contracts/"+strings.ToLower(token.Hex())+"/abi", nil)
		if err == nil {
			if resp, err := http.DefaultClient.Do(req); err == nil {
				var body struct {
					ABI []struct {
						Type, Name string
						Inputs     []struct{ Name, Type string }
					} `json:"abi"`
				}
				if resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&body) == nil {
					for _, f := range body.ABI {
						if f.Type == "function" && f.Name == "approve" && len(f.Inputs) == 2 && f.Inputs[0].Name != "" && f.Inputs[1].Name != "" {
							spender, amount = f.Inputs[0].Name, f.Inputs[1].Name
						}
					}
				}
				resp.Body.Close()
			}
		}
	}
	o.mu.Lock()
	if o.params == nil {
		o.params = map[common.Address][2]string{}
	}
	o.params[token] = [2]string{spender, amount}
	o.mu.Unlock()
	return spender, amount
}

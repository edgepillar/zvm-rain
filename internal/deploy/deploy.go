// Package deploy puts Rain on the ZVM: it tops the bot's gas up from the devnet
// wallet, deploys the contract with the bot as operator, reads it back, and
// verifies the source on the explorer. Every step checks the chain first, so a
// deploy that stops is finished by running it again.
package deploy

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/Zenoglyphs-VM/zvm-rain/internal/chain"
	"github.com/Zenoglyphs-VM/zvm-rain/internal/contracts"
	"github.com/Zenoglyphs-VM/zvm-rain/internal/keys"
)

// Record is what a deploy leaves behind and what the bot reads the contract's
// address from. It holds no secrets and is meant to be committed.
type Record struct {
	ChainID    uint64            `json:"chainId"`
	Rain       common.Address    `json:"rain"`
	WZNN       common.Address    `json:"wznn"`
	Bot        common.Address    `json:"bot"`
	RainBlock  uint64            `json:"rainBlock"`
	Txs        map[string]string `json:"txs"`
	Verified   string            `json:"verified,omitempty"`
	DeployedAt time.Time         `json:"deployedAt"`
}

func LoadRecord(path string) (*Record, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &r, nil
}

func (r *Record) save(path string) error {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type Config struct {
	RPC       string // JSON-RPC, e.g. http://localhost:49000/rpc
	API       string // the zvm node's API base for verification; "" skips it
	Keys      *keys.Keys
	Funder    *ecdsa.PrivateKey // the devnet wallet that pays the bot's gas
	Record    string
	WZNN      common.Address // wrapped ZNN, which ZNN rains draw on
	BotGasWei *big.Int       // what the bot is topped up to
	Log       *slog.Logger
}

func Run(ctx context.Context, cfg Config) (*Record, error) {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	bot, err := chain.Dial(ctx, cfg.RPC, cfg.Keys.Bot)
	if err != nil {
		return nil, err
	}
	funder, err := chain.Dial(ctx, cfg.RPC, cfg.Funder)
	if err != nil {
		return nil, err
	}
	rec := &Record{Txs: map[string]string{}}
	if old, err := LoadRecord(cfg.Record); err == nil {
		rec = old
		if rec.Txs == nil {
			rec.Txs = map[string]string{}
		}
		if rec.Bot != (common.Address{}) && rec.Bot != cfg.Keys.BotAddress() {
			return nil, fmt.Errorf("%s was deployed for %s, and these keys are %s", cfg.Record, rec.Bot, cfg.Keys.BotAddress())
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	rec.ChainID, rec.Bot, rec.WZNN = bot.ChainID.Uint64(), cfg.Keys.BotAddress(), cfg.WZNN
	if !hasCode(ctx, bot, rec.WZNN) {
		return rec, fmt.Errorf("no WZNN contract at %s", rec.WZNN)
	}
	if rec.ChainID != contracts.ChainID {
		log.Warn("not the ZVM", "chain", rec.ChainID)
	}

	// Gas: top up rather than send, so a rerun pays only what is missing.
	have, err := bot.Eth.BalanceAt(ctx, rec.Bot, nil)
	if err != nil {
		return nil, err
	}
	if have.Cmp(cfg.BotGasWei) < 0 {
		need := new(big.Int).Sub(cfg.BotGasWei, have)
		log.Info("funding the bot from the devnet wallet", "bot", rec.Bot, "wei", need, "from", funder.From)
		to := rec.Bot
		r, err := funder.Send(ctx, &to, need, nil)
		if err != nil {
			return rec, fmt.Errorf("funding the bot: %w", err)
		}
		rec.Txs["fund-bot-"+r.BlockNumber.String()] = r.TxHash.Hex()
	}

	if !hasCode(ctx, bot, rec.Rain) {
		data, err := contracts.Rain.DeployData(rec.Bot, rec.WZNN)
		if err != nil {
			return rec, err
		}
		log.Info("deploying Rain", "operator", rec.Bot, "bytes", len(data))
		r, err := bot.Send(ctx, nil, nil, data)
		if err != nil {
			return rec, fmt.Errorf("deploying Rain: %w", err)
		}
		rec.Rain, rec.RainBlock = r.ContractAddress, r.BlockNumber.Uint64()
		rec.Txs["deploy-rain"] = r.TxHash.Hex()
		if err := rec.save(cfg.Record); err != nil {
			return rec, err
		}
		log.Info("Rain deployed", "address", rec.Rain, "tx", r.TxHash)
	}
	if err := Check(ctx, bot, rec); err != nil {
		return rec, err
	}
	if cfg.API != "" && rec.Verified == "" {
		v, err := verify(ctx, cfg.API, rec.Rain)
		if err != nil {
			_ = rec.save(cfg.Record)
			return rec, fmt.Errorf("verifying on the explorer: %w", err)
		}
		rec.Verified = v
	}
	if rec.DeployedAt.IsZero() {
		rec.DeployedAt = time.Now().UTC()
	}
	return rec, rec.save(cfg.Record)
}

// Check reads a deployment back: the code is this build's (immutables
// aside), the operator is the bot, and ZNN rains draw on the recorded WZNN.
func Check(ctx context.Context, c *chain.Client, rec *Record) error {
	code, err := c.Eth.CodeAt(ctx, rec.Rain, nil)
	if err != nil {
		return err
	}
	// The compiler leaves zeros where the immutable WZNN address goes, and
	// the constructor fills them in. Every byte that is not zero in the build
	// must match, which pins every opcode; the wznn() read below then checks
	// what was filled in.
	want := contracts.Rain.DeployedBytecode
	if len(code) != len(want) {
		return fmt.Errorf("the code at %s is not this build of Rain", rec.Rain)
	}
	for i := range code {
		if code[i] != want[i] && want[i] != 0 {
			return fmt.Errorf("the code at %s is not this build of Rain", rec.Rain)
		}
	}
	for _, x := range []struct {
		getter string
		want   common.Address
	}{{"operator", rec.Bot}, {"wznn", rec.WZNN}} {
		out, err := c.Call(ctx, rec.Rain, contracts.Rain.Pack(x.getter))
		if err != nil {
			return err
		}
		v, err := contracts.Rain.ABI.Unpack(x.getter, out)
		if err != nil {
			return err
		}
		if got := v[0].(common.Address); got != x.want {
			return fmt.Errorf("Rain's %s is %s, not %s", x.getter, got, x.want)
		}
	}
	return nil
}

func hasCode(ctx context.Context, c *chain.Client, a common.Address) bool {
	if a == (common.Address{}) {
		return false
	}
	code, err := c.Eth.CodeAt(ctx, a, nil)
	return err == nil && len(code) > 0
}

// verify submits the standard JSON input to the node's verifier and waits for
// its verdict.
func verify(ctx context.Context, api string, addr common.Address) (string, error) {
	base := strings.TrimRight(api, "/")
	a := contracts.Rain
	body, err := json.Marshal(map[string]any{
		"address": addr.Hex(), "compiler": a.CompilerVersion,
		"contract": a.SourceName + ":" + a.ContractName, "input": a.StandardJSONInput,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/verify", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	var job struct {
		ID, Status, Message, Error string
		Match                      string `json:"match"`
	}
	err = json.NewDecoder(resp.Body).Decode(&job)
	resp.Body.Close()
	if err != nil {
		return "", fmt.Errorf("%s", resp.Status)
	}
	if job.ID == "" {
		if strings.Contains(strings.ToLower(job.Error+job.Message), "already verified") {
			return "already verified", nil
		}
		return "", fmt.Errorf("%s %s", resp.Status, job.Error+job.Message)
	}
	deadline := time.Now().Add(10 * time.Minute)
	for job.Status == "queued" || job.Status == "running" {
		if time.Now().After(deadline) {
			return "", fmt.Errorf("job %s still %s after 10 minutes", job.ID, job.Status)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(3 * time.Second):
		}
		if err := getJSON(ctx, base+"/api/verify/"+job.ID, &job); err != nil {
			return "", err
		}
	}
	if job.Status != "verified" {
		return "", fmt.Errorf("%s: %s", job.Status, job.Message)
	}
	if job.Match == "" {
		return "verified", nil
	}
	return job.Match, nil
}

func getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

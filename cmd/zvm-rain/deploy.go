package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/common"

	"github.com/Zenoglyphs-VM/zvm-rain/internal/chain"
	"github.com/Zenoglyphs-VM/zvm-rain/internal/deploy"
	"github.com/Zenoglyphs-VM/zvm-rain/internal/keys"
)

func cmdKeys(args []string) error {
	fs := flag.NewFlagSet("keys", flag.ExitOnError)
	out := fs.String("out", "", "where to write the secrets file (refuses to overwrite)")
	_ = fs.Parse(args)
	if *out == "" {
		return errors.New("-out is required")
	}
	k, err := keys.Generate(*out)
	if err != nil {
		return err
	}
	fmt.Printf("wrote %s\n  bot  %s\n", *out, k.BotAddress())
	return nil
}

// wholeZNN parses a decimal ZNN amount into wei (18 decimals on the ZVM).
func wholeZNN(s string) (*big.Int, error) {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(s))
	if !ok || r.Sign() < 0 {
		return nil, fmt.Errorf("not an amount: %q", s)
	}
	r.Mul(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)))
	if !r.IsInt() {
		return nil, fmt.Errorf("%q has more than 18 decimals", s)
	}
	return r.Num(), nil
}

func cmdDeploy(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("deploy", flag.ExitOnError)
	rpc := fs.String("rpc", os.Getenv("ZVM_RPC"), "ZVM JSON-RPC ($ZVM_RPC)")
	api := fs.String("api", os.Getenv("ZVM_NODE"), "the zvm node's API, for verification; \"\" skips it ($ZVM_NODE)")
	keyFile := fs.String("keys", "", "the operator's secrets file")
	record := fs.String("record", "", "the deployment record to write, and to resume from")
	funder := fs.Uint("funder", 900, "zvm devnet EVM key index that pays the bot's gas (900 is the devnet deployer)")
	botGas := fs.String("bot-gas", "50", "ZNN the bot is topped up to for gas")
	wznn := fs.String("wznn", "", "wrapped ZNN (WETH9) on this chain, which ZNN rains draw on: the node's /api/info names it as uniswapV2.weth")
	_ = fs.Parse(args)
	if err := need("rpc", *rpc, "ZVM_RPC"); err != nil {
		return err
	}
	if !common.IsHexAddress(*wznn) {
		return fmt.Errorf("pass -wznn, wrapped ZNN on this chain (uniswapV2.weth on the node's /api/info)")
	}
	if *keyFile == "" || *record == "" {
		return errors.New("-keys and -record are required")
	}
	k, err := keys.Load(*keyFile)
	if err != nil {
		return err
	}
	gas, err := wholeZNN(*botGas)
	if err != nil {
		return err
	}
	rec, err := deploy.Run(ctx, deploy.Config{
		RPC: *rpc, API: *api, Keys: k, Funder: keys.DevEVMKey(uint32(*funder)), Record: *record, BotGasWei: gas, WZNN: common.HexToAddress(*wznn),
	})
	if rec != nil {
		fmt.Printf("rain      %s\nbot       %s\nverified  %s\nrecord    %s\n", rec.Rain, rec.Bot, rec.Verified, *record)
	}
	return err
}

func cmdCheck(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	rpc := fs.String("rpc", os.Getenv("ZVM_RPC"), "ZVM JSON-RPC ($ZVM_RPC)")
	record := fs.String("record", "", "the deployment record")
	_ = fs.Parse(args)
	if err := need("rpc", *rpc, "ZVM_RPC"); err != nil {
		return err
	}
	rec, err := deploy.LoadRecord(*record)
	if err != nil {
		return err
	}
	c, err := chain.Dial(ctx, *rpc, nil)
	if err != nil {
		return err
	}
	if err := deploy.Check(ctx, c, rec); err != nil {
		return err
	}
	fmt.Println("ok: Rain is this build, and its operator is the bot")
	return nil
}

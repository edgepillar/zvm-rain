// zvm-rain is a Telegram bot that splits tokens evenly between the people who
// spoke in a group in the last few minutes, through the Rain contract on
// the ZVM.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

const usage = `zvm-rain: rain tokens on a Telegram group, on the ZVM

  keys    -out FILE                 generate the operator's secrets file
  deploy  -keys FILE -record FILE   deploy Rain with the bot as its operator
  check   -record FILE              read a deployment back and check it
  bot                               run the Telegram bot (settings from RAIN_* in the environment)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	var err error
	args := os.Args[2:]
	switch os.Args[1] {
	case "keys":
		err = cmdKeys(args)
	case "deploy":
		err = cmdDeploy(ctx, args)
	case "check":
		err = cmdCheck(ctx, args)
	case "bot":
		err = cmdBot(ctx, args)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "zvm-rain:", err)
		os.Exit(1)
	}
}

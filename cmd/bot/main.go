package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"overnight-trading-bot/internal/app"
)

func main() {
	mode := flag.String("mode", "", "override APP_MODE: backtest|paper|sandbox|live_readonly|live_trade")
	halt := flag.Bool("halt", false, "manually set HALT and stop new automated actions")
	unhalt := flag.Bool("unhalt", false, "manually clear HALT after reconciliation")
	reason := flag.String("reason", "", "audit reason for -halt or -unhalt")
	health := flag.Bool("healthcheck", false, "check local /health endpoint")
	healthURL := flag.String("healthcheck-url", "", "healthcheck URL; default http://127.0.0.1:3300/health")
	flag.Parse()

	if err := app.Run(context.Background(), app.Options{
		Stdout:         os.Stdout,
		Stderr:         os.Stderr,
		ModeOverride:   *mode,
		Halt:           *halt,
		Unhalt:         *unhalt,
		Reason:         *reason,
		Healthcheck:    *health,
		HealthcheckURL: *healthURL,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "bot failed: %v\n", err)
		os.Exit(1)
	}
}

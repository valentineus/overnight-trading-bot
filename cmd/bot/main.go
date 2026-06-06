package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"overnight-trading-bot/internal/app"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to YAML configuration file")
	flag.Parse()

	if err := app.Run(context.Background(), app.Options{
		ConfigPath: *configPath,
		Stdout:     os.Stdout,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "bot failed: %v\n", err)
		os.Exit(1)
	}
}

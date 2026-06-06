package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

type Options struct {
	ConfigPath string
	Stdout     io.Writer
}

func Run(ctx context.Context, opts Options) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if opts.ConfigPath == "" {
		return errors.New("config path is required")
	}

	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}

	if _, err := os.Stat(opts.ConfigPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("config file %q does not exist; copy config.example.yaml to config.yaml and fill credentials", opts.ConfigPath)
		}

		return fmt.Errorf("check config file %q: %w", opts.ConfigPath, err)
	}

	fmt.Fprintf(opts.Stdout, "overnight trading bot initialized with config %q\n", opts.ConfigPath)
	return nil
}

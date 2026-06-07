package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jmoiron/sqlx"

	"overnight-trading-bot/internal/repository/mysql"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	dsn := flag.String("dsn", os.Getenv("DB_DSN"), "MySQL/MariaDB DSN")
	direction := flag.String("direction", "up", "up or down")
	flag.Parse()
	if flag.NArg() > 0 {
		*direction = flag.Arg(0)
	}
	if *dsn == "" {
		return fmt.Errorf("DB_DSN is required")
	}
	db, err := sqlx.Open("mysql", *dsn)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() {
		_ = db.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping db: %w", err)
	}
	switch *direction {
	case "up":
		err = mysql.ApplyMigrations(ctx, db.DB)
	case "down":
		err = mysql.RollbackAll(db.DB)
	default:
		err = fmt.Errorf("unknown direction %q", *direction)
	}
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	fmt.Println("migrations applied")
	return nil
}

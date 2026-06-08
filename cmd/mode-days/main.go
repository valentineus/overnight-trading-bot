package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"

	"overnight-trading-bot/internal/domain"
)

const moscowOffset = 3 * time.Hour

type modeDayRow struct {
	Mode string `db:"mode"`
	Days int    `db:"days"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	dsn := flag.String("dsn", os.Getenv("DB_DSN"), "MySQL/MariaDB DSN")
	fromRaw := flag.String("from", "", "optional start date YYYY-MM-DD")
	toRaw := flag.String("to", "", "optional end date YYYY-MM-DD, inclusive")
	check := flag.Bool("check", true, "fail when live readiness thresholds are not met")
	minReadonly := flag.Int("min-readonly-days", 20, "minimum live_readonly days")
	minPaper := flag.Int("min-paper-days", 20, "minimum paper days")
	minSandbox := flag.Int("min-sandbox-days", 10, "minimum sandbox days")
	flag.Parse()
	if *dsn == "" {
		return fmt.Errorf("DB_DSN is required")
	}
	from, err := parseOptionalDate(*fromRaw)
	if err != nil {
		return fmt.Errorf("from: %w", err)
	}
	to, err := parseOptionalDate(*toRaw)
	if err != nil {
		return fmt.Errorf("to: %w", err)
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
	counts, err := loadModeDayCounts(ctx, db, from, to)
	if err != nil {
		return err
	}
	printCounts(counts)
	if !*check {
		return nil
	}
	thresholds := map[domain.Mode]int{
		domain.ModeLiveReadonly: *minReadonly,
		domain.ModePaper:        *minPaper,
		domain.ModeSandbox:      *minSandbox,
	}
	return checkThresholds(counts, thresholds)
}

func loadModeDayCounts(ctx context.Context, db *sqlx.DB, from, to time.Time) (map[domain.Mode]int, error) {
	query := `SELECT mode, COUNT(DISTINCT DATE(DATE_ADD(ts, INTERVAL 3 HOUR))) AS days FROM system_state_history WHERE DAYOFWEEK(DATE_ADD(ts, INTERVAL 3 HOUR)) BETWEEN 2 AND 6`
	var args []any
	if !from.IsZero() {
		query += ` AND ts >= ?`
		args = append(args, from.Add(-moscowOffset))
	}
	if !to.IsZero() {
		query += ` AND ts < ?`
		args = append(args, to.AddDate(0, 0, 1).Add(-moscowOffset))
	}
	query += ` GROUP BY mode`
	var rows []modeDayRow
	if err := db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("query mode days: %w", err)
	}
	counts := make(map[domain.Mode]int, len(rows))
	for _, row := range rows {
		mode, err := domain.ParseMode(row.Mode)
		if err != nil {
			return nil, err
		}
		counts[mode] = row.Days
	}
	return counts, nil
}

func printCounts(counts map[domain.Mode]int) {
	modes := make([]string, 0, len(counts))
	for mode := range counts {
		modes = append(modes, string(mode))
	}
	sort.Strings(modes)
	for _, rawMode := range modes {
		mode := domain.Mode(rawMode)
		fmt.Printf("%s=%d\n", mode, counts[mode])
	}
}

func checkThresholds(counts map[domain.Mode]int, thresholds map[domain.Mode]int) error {
	var failed []string
	for mode, threshold := range thresholds {
		if counts[mode] < threshold {
			failed = append(failed, fmt.Sprintf("%s=%d/%d", mode, counts[mode], threshold))
		}
	}
	sort.Strings(failed)
	if len(failed) > 0 {
		return fmt.Errorf("mode day thresholds not met: %s", strings.Join(failed, ", "))
	}
	return nil
}

func parseOptionalDate(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	return time.ParseInLocation("2006-01-02", raw, time.UTC)
}

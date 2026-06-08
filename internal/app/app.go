package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/config"
	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/execution"
	"overnight-trading-bot/internal/features"
	"overnight-trading-bot/internal/healthcheck"
	"overnight-trading-bot/internal/instruments"
	"overnight-trading-bot/internal/logging"
	"overnight-trading-bot/internal/marketdata"
	"overnight-trading-bot/internal/notify"
	"overnight-trading-bot/internal/position"
	"overnight-trading-bot/internal/reconciliation"
	mysqlrepo "overnight-trading-bot/internal/repository/mysql"
	"overnight-trading-bot/internal/risk"
	"overnight-trading-bot/internal/scheduler"
	signalengine "overnight-trading-bot/internal/signal"
	"overnight-trading-bot/internal/statemachine"
	"overnight-trading-bot/internal/timeutil"
	"overnight-trading-bot/internal/tinvest"
)

type Options struct {
	Stdout         io.Writer
	Stderr         io.Writer
	ModeOverride   string
	Halt           bool
	Unhalt         bool
	Reason         string
	Healthcheck    bool
	HealthcheckURL string
	RunOnce        bool
}

func Run(ctx context.Context, opts Options) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if opts.Healthcheck {
		target := opts.HealthcheckURL
		if target == "" {
			target = "http://127.0.0.1:3300/ready"
		}
		return healthcheck.CheckEndpoint(ctx, target)
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load ENV config: %w", err)
	}
	if opts.ModeOverride != "" {
		mode, err := domain.ParseMode(opts.ModeOverride)
		if err != nil {
			return err
		}
		cfg.App.Mode = mode
		if err := cfg.Validate(); err != nil {
			return err
		}
	}
	log := logging.New(cfg.App.LogLevel, opts.Stdout)
	log.Info("overnight trading bot starting", "mode", cfg.App.Mode)

	if opts.Halt && opts.Unhalt {
		return errors.New("-halt and -unhalt are mutually exclusive")
	}
	if cfg.App.Mode == domain.ModeBacktest && !opts.Unhalt && !opts.Halt {
		_, _ = fmt.Fprintf(opts.Stdout, "overnight trading bot initialized in %s mode\n", cfg.App.Mode)
		return nil
	}
	if opts.Halt && cfg.DB.DSN == "" {
		return errors.New("-halt requires DB_DSN")
	}

	db, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() {
		_ = db.Close()
	}()
	if cfg.DB.MigrationsAutoApply {
		if err := mysqlrepo.ApplyMigrations(ctx, db.DB); err != nil {
			return err
		}
	}
	repo := mysqlrepo.NewRepository(db)
	if opts.Halt {
		if strings.TrimSpace(opts.Reason) == "" {
			return errors.New("-halt requires -reason")
		}
		if err := risk.NewManager(repo, risk.ManagerConfig{}).Halt(ctx, cfg.App.Mode, "manual_halt", opts.Reason, ""); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(opts.Stdout, "system halted: %s\n", opts.Reason)
		return nil
	}
	if opts.Unhalt {
		if strings.TrimSpace(opts.Reason) == "" {
			return errors.New("-unhalt requires -reason")
		}
		gateway, closer, err := buildGateway(ctx, cfg, log)
		if err != nil {
			return err
		}
		if closer != nil {
			defer closer()
		}
		accountIDHash := accountHash(cfg.TInvest.AccountID)
		clock := timeutil.RealClock{Loc: cfg.Location}
		recon := reconciliation.New(repo, gateway, cfg.TInvest.AccountID, accountIDHash).
			WithWindow(time.Duration(cfg.Risk.ReconciliationWindowHours)*time.Hour).
			WithInFlightGrace(time.Duration(cfg.Risk.ReconciliationSkewSec)*time.Second).
			WithCommissionPolicy(cfg.Commission.RequireZeroCommission, cfg.Commission.QuarantineOnNonZero, cfg.Risk.CommissionToleranceRUB).
			WithClock(clock)
		diffs, err := recon.Run(ctx)
		if err != nil {
			return fmt.Errorf("pre-unhalt reconciliation: %w", err)
		}
		if reconciliation.HasCritical(diffs) {
			return fmt.Errorf("pre-unhalt reconciliation has critical diffs: %d", len(diffs))
		}
		if err := repo.Unhalt(ctx, opts.Reason); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(opts.Stdout, "system unhalted: %s\n", opts.Reason)
		return nil
	}

	gateway, closer, err := buildGateway(ctx, cfg, log)
	if err != nil {
		return err
	}
	if closer != nil {
		defer closer()
	}
	if err := seedPaperGateway(ctx, repo, gateway); err != nil {
		return err
	}
	notifier, err := notify.NewTelegram(notify.TelegramConfig{
		BotToken:     cfg.Telegram.BotToken,
		ChatID:       cfg.Telegram.ChatID,
		NotifyInfo:   cfg.Telegram.NotifyInfo,
		NotifyWarn:   cfg.Telegram.NotifyWarn,
		NotifyAlert:  cfg.Telegram.NotifyAlert,
		NotifyReport: cfg.Telegram.NotifyReport,
		AuditSink:    repo,
	}, log)
	if err != nil {
		return fmt.Errorf("create notifier: %w", err)
	}
	defer func() {
		_ = notifier.Close()
	}()

	accountIDHash := accountHash(cfg.TInvest.AccountID)
	clock := timeutil.RealClock{Loc: cfg.Location}
	recon := reconciliation.New(repo, gateway, cfg.TInvest.AccountID, accountIDHash).
		WithWindow(time.Duration(cfg.Risk.ReconciliationWindowHours)*time.Hour).
		WithInFlightGrace(time.Duration(cfg.Risk.ReconciliationSkewSec)*time.Second).
		WithCommissionPolicy(cfg.Commission.RequireZeroCommission, cfg.Commission.QuarantineOnNonZero, cfg.Risk.CommissionToleranceRUB).
		WithClock(clock)
	sm := statemachine.New(repo, cfg.App.Mode)
	if _, err := sm.Recover(ctx, recon); err != nil {
		_ = notifier.Alert(ctx, fmt.Sprintf("state recovery failed: %s", err))
		return fmt.Errorf("state recovery: %w", err)
	}
	health := healthcheck.New(db.DB, gateway, time.Duration(cfg.Risk.MaxClockDriftSec)*time.Second)
	health.Start(cfg.App.HealthcheckAddr)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.App.ShutdownTimeoutSec)*time.Second)
		defer cancel()
		_ = health.Shutdown(shutdownCtx)
	}()
	if err := notifier.Info(ctx, fmt.Sprintf("bot started in %s mode", cfg.App.Mode)); err != nil {
		log.Warn("notify startup failed", "err", err)
	}
	if opts.RunOnce {
		_, _ = fmt.Fprintf(opts.Stdout, "overnight trading bot initialized in %s mode\n", cfg.App.Mode)
		return nil
	}
	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	runtime := buildScheduler(clock, sm, cfg, repo, gateway, notifier, recon, accountIDHash, log)
	if err := runtime.Run(runCtx); err != nil {
		if runCtx.Err() == nil {
			return err
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.App.ShutdownTimeoutSec)*time.Second)
		defer cancel()
		if shutdownErr := runtime.GracefulShutdown(shutdownCtx); shutdownErr != nil {
			return fmt.Errorf("%w; graceful shutdown: %v", err, shutdownErr)
		}
		return err
	}
	return nil
}

func buildScheduler(clock timeutil.Clock, sm statemachine.System, cfg config.Config, repo *mysqlrepo.Repository, gateway tinvest.Gateway, notifier notify.Notifier, recon reconciliation.Engine, accountIDHash string, log *slog.Logger) scheduler.Scheduler {
	registry := instruments.NewRegistry(repo, gateway)
	loader := marketdata.NewLoader(repo, gateway)
	loader.SetClock(clock)
	pipeline := features.NewPipeline(repo, features.PipelineConfig{
		RollingShort:           cfg.Strategy.RollingShort,
		RollingLong:            cfg.Strategy.RollingLong,
		EWMALambda:             cfg.Strategy.EWMALambda,
		RiskBufferBps:          cfg.Strategy.RiskBufferBps,
		EntrySlippageBps:       cfg.Backtest.EntrySlippageBps,
		ExitSlippageBps:        cfg.Backtest.ExitSlippageBps,
		CommissionRoundtripBps: cfg.Backtest.CommissionRoundtripBps,
		EntryWindow: timeutil.Window{
			Start: cfg.Execution.EntryWindowStart,
			End:   cfg.Execution.EntryWindowEnd,
		},
		ExitWindow: timeutil.Window{
			Start: cfg.Execution.ExitWindowStart,
			End:   cfg.Execution.ExitWindowEnd,
		},
		IntervalVolumeLookback: 20,
		Location:               cfg.Location,
	})
	signalEngine := signalengine.New(signalengine.Config{
		MinTStat60:              cfg.Strategy.MinTStat60,
		MinWinRate60:            cfg.Strategy.MinWinRate60,
		MinNetEdgeBps:           cfg.Strategy.MinNetEdgeBps,
		MinADVRUB:               cfg.Liquidity.MinADVRUB,
		MaxSpreadBpsDefault:     cfg.Liquidity.MaxSpreadBpsDefault,
		MaxSpreadBpsMoneyMarket: cfg.Liquidity.MaxSpreadBpsMoneyMarket,
		MaxSpreadBpsBondFunds:   cfg.Liquidity.MaxSpreadBpsBondFunds,
		MaxSpreadBpsEquityFunds: cfg.Liquidity.MaxSpreadBpsEquityFunds,
		MaxTickBps:              cfg.Liquidity.MaxTickBps,
		RequireZeroCommission:   cfg.Commission.RequireZeroCommission,
		MaxPositions:            cfg.Strategy.MaxPositions,
	})
	sizer := risk.NewSizer(risk.SizingConfig{
		MaxPositionPct:             cfg.Risk.MaxPositionPct,
		MaxTotalExposurePct:        cfg.Risk.MaxTotalExposurePct,
		MaxParticipationRate:       cfg.Liquidity.MaxParticipationRate,
		CashUsageBuffer:            cfg.Risk.CashUsageBuffer,
		RiskBudgetPerInstrumentPct: cfg.Risk.RiskBudgetPerInstrumentPct,
		MinOrderNotionalRUB:        cfg.Risk.MinOrderNotionalRUB,
	})
	freeOrders := risk.NewFreeOrderBudget(repo)
	riskManager := risk.NewManager(repo, risk.ManagerConfig{
		MaxDailyLossPct:           cfg.Risk.MaxDailyLossPct,
		MaxWeeklyLossPct:          cfg.Risk.MaxWeeklyLossPct,
		MaxMonthlyDrawdownPct:     cfg.Risk.MaxMonthlyDrawdownPct,
		MaxAvgSlippageBps10Trades: cfg.Risk.MaxAvgSlippageBps10Trades,
		MaxOpenPositions:          cfg.Risk.MaxOpenPositions,
		MinTimeToClose:            time.Duration(cfg.Execution.MinTimeToCloseSec) * time.Second,
		MaxQuoteAge:               time.Duration(cfg.Execution.MaxQuoteAgeSec) * time.Second,
	})
	execEngine := execution.NewEngine(cfg.App.Mode, cfg.TInvest.AccountID, gateway, repo)
	execEngine.SetClock(clock)
	execEngine.SetMaxQuoteAge(time.Duration(cfg.Execution.MaxQuoteAgeSec) * time.Second)
	execEngine.SetFreeOrderCountPolicy(cfg.Commission.FreeOrderCountPolicy)
	services := scheduler.Services{
		Repo:          repo,
		Gateway:       gateway,
		Registry:      registry,
		MarketData:    loader,
		Features:      pipeline,
		Signals:       signalEngine,
		Sizer:         sizer,
		FreeOrders:    freeOrders,
		Risk:          riskManager,
		Execution:     &execEngine,
		Positions:     position.NewManager(repo),
		Reconcile:     recon,
		Notifier:      notifier,
		AccountID:     cfg.TInvest.AccountID,
		AccountIDHash: accountIDHash,
		Log:           log,
	}
	return scheduler.New(clock, sm, scheduler.Config{
		Mode:                   cfg.App.Mode,
		Location:               cfg.Location,
		RollingLong:            cfg.Strategy.RollingLong,
		TickInterval:           30 * time.Second,
		EntrySignalTime:        cfg.Execution.EntrySignalTime,
		EntryWindowStart:       cfg.Execution.EntryWindowStart,
		EntryWindowEnd:         cfg.Execution.EntryWindowEnd,
		NoNewEntryAfter:        cfg.Execution.NoNewEntryAfter,
		ExitWatchStart:         cfg.Execution.ExitWatchStart,
		ExitNotBefore:          cfg.Execution.ExitNotBefore,
		ExitWindowStart:        cfg.Execution.ExitWindowStart,
		ExitWindowEnd:          cfg.Execution.ExitWindowEnd,
		HardExitDeadline:       cfg.Execution.HardExitDeadline,
		MarketClose:            cfg.Execution.MarketClose,
		QuoteDepth:             cfg.Execution.QuoteDepth,
		MaxQuoteAge:            time.Duration(cfg.Execution.MaxQuoteAgeSec) * time.Second,
		OrderPollInterval:      time.Duration(cfg.Execution.OrderPollIntervalMS) * time.Millisecond,
		PassiveImproveTicks:    cfg.Execution.PassiveImproveTicks,
		MaxEntryOrderAttempts:  cfg.Execution.MaxEntryOrderAttempts,
		MaxExitOrderAttempts:   cfg.Execution.MaxExitOrderAttempts,
		MinTimeToClose:         time.Duration(cfg.Execution.MinTimeToCloseSec) * time.Second,
		MaxClockDrift:          time.Duration(cfg.Risk.MaxClockDriftSec) * time.Second,
		APIOutageHalt:          time.Duration(cfg.Risk.APIOutageHaltSec) * time.Second,
		RequireZeroCommission:  cfg.Commission.RequireZeroCommission,
		QuarantineOnNonZero:    cfg.Commission.QuarantineOnNonZero,
		FreeOrderCountPolicy:   cfg.Commission.FreeOrderCountPolicy,
		ReconciliationInterval: 5 * time.Minute,
		MaxOpenPositions:       minPositive(cfg.Strategy.MaxPositions, cfg.Risk.MaxOpenPositions),
	}, services)
}

func minPositive(a, b int) int {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	case a < b:
		return a
	default:
		return b
	}
}

func openDB(ctx context.Context, cfg config.Config) (*sqlx.DB, error) {
	db, err := sqlx.Open("mysql", cfg.DB.DSN)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(cfg.DB.MaxOpenConns)
	db.SetMaxIdleConns(cfg.DB.MaxIdleConns)
	db.SetConnMaxLifetime(time.Duration(cfg.DB.ConnMaxLifetimeMin) * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("db ping: %w", err)
	}
	return db, nil
}

func buildGateway(ctx context.Context, cfg config.Config, log *slog.Logger) (tinvest.Gateway, func(), error) {
	switch cfg.App.Mode {
	case domain.ModePaper:
		if cfg.TInvest.Token != "" {
			accountID := cfg.TInvest.AccountID
			if accountID == "" {
				accountID = "paper-readonly"
			}
			gw, err := tinvest.NewRealGateway(ctx, tinvest.Options{
				Token:          cfg.TInvest.Token,
				AccountID:      accountID,
				Endpoint:       cfg.TInvest.Endpoint,
				AppName:        cfg.TInvest.AppName,
				RequestTimeout: time.Duration(cfg.TInvest.RequestTimeoutSec) * time.Second,
				RetryCount:     cfg.TInvest.RetryCount,
				RetryBackoff:   time.Duration(cfg.TInvest.RetryBackoffSec) * time.Second,
				Logger:         log,
			})
			if err != nil {
				return nil, nil, err
			}
			return tinvest.NewPaperGateway(gw), func() { _ = gw.Close() }, nil
		}
		return tinvest.NewFakeGateway(), nil, nil
	case domain.ModeSandbox:
		gw, err := tinvest.NewSandboxGateway(ctx, tinvest.Options{
			Token:          cfg.TInvest.Token,
			AccountID:      cfg.TInvest.AccountID,
			AppName:        cfg.TInvest.AppName,
			RequestTimeout: time.Duration(cfg.TInvest.RequestTimeoutSec) * time.Second,
			RetryCount:     cfg.TInvest.RetryCount,
			RetryBackoff:   time.Duration(cfg.TInvest.RetryBackoffSec) * time.Second,
			Logger:         log,
		})
		if err != nil {
			return nil, nil, err
		}
		return gw, func() { _ = gw.Close() }, nil
	case domain.ModeLiveReadonly, domain.ModeLiveTrade:
		endpoint := cfg.TInvest.Endpoint
		if cfg.TInvest.UseSandbox {
			return nil, nil, errors.New("TINVEST_USE_SANDBOX is only allowed with APP_MODE=sandbox")
		}
		gw, err := tinvest.NewRealGateway(ctx, tinvest.Options{
			Token:          cfg.TInvest.Token,
			AccountID:      cfg.TInvest.AccountID,
			Endpoint:       endpoint,
			AppName:        cfg.TInvest.AppName,
			RequestTimeout: time.Duration(cfg.TInvest.RequestTimeoutSec) * time.Second,
			RetryCount:     cfg.TInvest.RetryCount,
			RetryBackoff:   time.Duration(cfg.TInvest.RetryBackoffSec) * time.Second,
			Logger:         log,
		})
		if err != nil {
			return nil, nil, err
		}
		return gw, func() { _ = gw.Close() }, nil
	default:
		return tinvest.NewFakeGateway(), nil, nil
	}
}

func seedPaperGateway(ctx context.Context, repo interface {
	ListInstruments(context.Context, bool) ([]domain.Instrument, error)
}, gateway tinvest.Gateway) error {
	fake, ok := gateway.(*tinvest.FakeGateway)
	if !ok {
		paper, isPaper := gateway.(*tinvest.PaperGateway)
		if !isPaper {
			return nil
		}
		fake = paper.Fake()
	}
	instrumentsList, err := repo.ListInstruments(ctx, true)
	if err != nil {
		return err
	}
	for _, instrument := range instrumentsList {
		remote := instrument
		if remote.InstrumentUID == "" || strings.HasPrefix(remote.InstrumentUID, "PENDING:") {
			remote.InstrumentUID = "paper-" + strings.ToUpper(remote.Ticker)
		}
		if remote.Figi == "" {
			remote.Figi = remote.InstrumentUID
		}
		if remote.Lot <= 0 {
			remote.Lot = 1
		}
		if !remote.MinPriceIncrement.IsPositive() {
			remote.MinPriceIncrement = decimal.RequireFromString("0.01")
		}
		if remote.Currency == "" {
			remote.Currency = "RUB"
		}
		remote.Enabled = true
		remote.UpdatedAt = time.Now().UTC()
		fake.Instruments[remote.InstrumentUID] = remote
		fake.Statuses[remote.InstrumentUID] = domain.TradingStatusNormal
	}
	return nil
}

func accountHash(accountID string) string {
	sum := sha256.Sum256([]byte(accountID))
	return hex.EncodeToString(sum[:])
}

func HealthURL(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "http://127.0.0.1" + addr + "/ready"
	}
	if _, err := url.ParseRequestURI(addr); err == nil && strings.HasPrefix(addr, "http") {
		return addr
	}
	return "http://" + addr + "/ready"
}

func PingDB(ctx context.Context, db *sql.DB) error {
	return db.PingContext(ctx)
}

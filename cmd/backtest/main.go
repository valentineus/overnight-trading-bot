package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/backtest"
	"overnight-trading-bot/internal/domain"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	candlesPath := flag.String("candles", "", "CSV with columns instrument_uid,trade_date,open,high,low,close,volume_lots")
	minuteCandlesPath := flag.String("minute-candles", "", "optional minute CSV with the same columns; trade_date may be RFC3339")
	outputDir := flag.String("out", "./backtest_out", "output directory")
	useMinuteModel := flag.Bool("use-minute-model", false, "require minute candles for conservative limit-fill simulation")
	entrySlip := flag.String("entry-slippage-bps", "8", "entry slippage in bps")
	exitSlip := flag.String("exit-slippage-bps", "8", "exit slippage in bps")
	commission := flag.String("commission-roundtrip-bps", "0", "roundtrip commission in bps")
	riskBuffer := flag.String("risk-buffer-bps", "5", "risk buffer in bps included in signal cost")
	assumedSpread := flag.String("assumed-spread-bps", "20", "assumed executable spread cost in bps")
	rollingShort := flag.Int("rolling-short", 60, "short rolling window")
	rollingLong := flag.Int("rolling-long", 252, "long rolling window")
	ewmaLambda := flag.Float64("ewma-lambda", 0.08, "EWMA lambda")
	minTStat := flag.String("min-tstat-60", "1.25", "minimum short-window t-stat")
	minWinRate := flag.String("min-win-rate-60", "0.55", "minimum short-window win rate")
	minNetEdge := flag.String("min-net-edge-bps", "10", "minimum net edge in bps")
	minADV := flag.String("min-adv-rub", "5000000", "minimum ADV in RUB")
	maxSpread := flag.String("max-spread-bps", "20", "maximum spread in bps")
	maxTick := flag.String("max-tick-bps", "10", "maximum tick size in bps")
	requireZeroCommission := flag.Bool("require-zero-commission", true, "reject trades when roundtrip commission is non-zero")
	flag.Parse()
	if *candlesPath == "" {
		return fmt.Errorf("-candles is required")
	}
	file, err := os.Open(*candlesPath)
	if err != nil {
		return fmt.Errorf("open candles: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()
	candles, metadata, err := backtest.LoadCandlesCSVWithMetadata(file)
	if err != nil {
		return fmt.Errorf("load candles: %w", err)
	}
	var minuteCandles map[string][]domain.Candle
	if *minuteCandlesPath != "" {
		minuteFile, err := os.Open(*minuteCandlesPath)
		if err != nil {
			return fmt.Errorf("open minute candles: %w", err)
		}
		defer func() {
			_ = minuteFile.Close()
		}()
		var minuteMetadata map[string]backtest.InstrumentMetadata
		minuteCandles, minuteMetadata, err = backtest.LoadCandlesCSVWithMetadata(minuteFile)
		if err != nil {
			return fmt.Errorf("load minute candles: %w", err)
		}
		mergeMetadata(metadata, minuteMetadata)
	}
	if *useMinuteModel && len(minuteCandles) == 0 {
		return fmt.Errorf("-minute-candles is required when -use-minute-model=true")
	}
	entry, err := decimal.NewFromString(*entrySlip)
	if err != nil {
		return fmt.Errorf("entry slippage: %w", err)
	}
	exit, err := decimal.NewFromString(*exitSlip)
	if err != nil {
		return fmt.Errorf("exit slippage: %w", err)
	}
	comm, err := decimal.NewFromString(*commission)
	if err != nil {
		return fmt.Errorf("commission: %w", err)
	}
	riskBuf, err := decimal.NewFromString(*riskBuffer)
	if err != nil {
		return fmt.Errorf("risk buffer: %w", err)
	}
	assumed, err := decimal.NewFromString(*assumedSpread)
	if err != nil {
		return fmt.Errorf("assumed spread: %w", err)
	}
	tstat, err := decimal.NewFromString(*minTStat)
	if err != nil {
		return fmt.Errorf("min tstat: %w", err)
	}
	winRate, err := decimal.NewFromString(*minWinRate)
	if err != nil {
		return fmt.Errorf("min win rate: %w", err)
	}
	netEdge, err := decimal.NewFromString(*minNetEdge)
	if err != nil {
		return fmt.Errorf("min net edge: %w", err)
	}
	adv, err := decimal.NewFromString(*minADV)
	if err != nil {
		return fmt.Errorf("min adv: %w", err)
	}
	spread, err := decimal.NewFromString(*maxSpread)
	if err != nil {
		return fmt.Errorf("max spread: %w", err)
	}
	tick, err := decimal.NewFromString(*maxTick)
	if err != nil {
		return fmt.Errorf("max tick: %w", err)
	}
	lotsByInstrument, ticksByInstrument := metadataMaps(metadata)
	engine := backtest.New(backtest.Config{
		EntrySlippageBps:               entry,
		ExitSlippageBps:                exit,
		CommissionRoundtripBps:         comm,
		RiskBufferBps:                  riskBuf,
		OutputDir:                      *outputDir,
		RollingShort:                   *rollingShort,
		RollingLong:                    *rollingLong,
		EWMALambda:                     *ewmaLambda,
		MinTStat60:                     tstat,
		MinWinRate60:                   winRate,
		MinNetEdgeBps:                  netEdge,
		MinADVRUB:                      adv,
		MaxSpreadBps:                   spread,
		MaxTickBps:                     tick,
		AssumedSpreadBps:               assumed,
		RequireZeroCommission:          requireZeroCommission,
		LotsByInstrument:               lotsByInstrument,
		MinPriceIncrementsByInstrument: ticksByInstrument,
		UseMinuteModel:                 *useMinuteModel,
	})
	result, err := engine.RunWithMinuteCandles(candles, minuteCandles)
	if err != nil {
		return fmt.Errorf("run backtest: %w", err)
	}
	if err := result.Write(*outputDir); err != nil {
		return fmt.Errorf("write result: %w", err)
	}
	fmt.Printf("backtest complete: trades=%d total_return=%.6f\n", result.Metrics.NumberOfTrades, result.Metrics.TotalReturn)
	return nil
}

func mergeMetadata(dst, src map[string]backtest.InstrumentMetadata) {
	for uid, meta := range src {
		current := dst[uid]
		if current.Lot <= 0 {
			current.Lot = meta.Lot
		}
		if !current.MinPriceIncrement.IsPositive() {
			current.MinPriceIncrement = meta.MinPriceIncrement
		}
		if current.Lot > 0 || current.MinPriceIncrement.IsPositive() {
			dst[uid] = current
		}
	}
}

func metadataMaps(metadata map[string]backtest.InstrumentMetadata) (map[string]int64, map[string]decimal.Decimal) {
	lots := make(map[string]int64)
	ticks := make(map[string]decimal.Decimal)
	for uid, meta := range metadata {
		if meta.Lot > 0 {
			lots[uid] = meta.Lot
		}
		if meta.MinPriceIncrement.IsPositive() {
			ticks[uid] = meta.MinPriceIncrement
		}
	}
	return lots, ticks
}

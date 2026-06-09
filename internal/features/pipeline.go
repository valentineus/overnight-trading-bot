package features

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/repository"
	"overnight-trading-bot/internal/timeutil"
)

const defaultIntervalVolumeLookback = 20

type PipelineConfig struct {
	RollingShort           int
	RollingLong            int
	EWMALambda             float64
	RiskBufferBps          decimal.Decimal
	EntrySlippageBps       decimal.Decimal
	ExitSlippageBps        decimal.Decimal
	CommissionRoundtripBps decimal.Decimal
	EntryWindow            timeutil.Window
	ExitWindow             timeutil.Window
	IntervalVolumeLookback int
	TradingDays            []time.Time
	Location               *time.Location
}

type Pipeline struct {
	repo repository.Repository
	cfg  PipelineConfig
}

func NewPipeline(repo repository.Repository, cfg PipelineConfig) Pipeline {
	return Pipeline{repo: repo, cfg: cfg}
}

func (p Pipeline) WithTradingDays(days []time.Time) Pipeline {
	p.cfg.TradingDays = days
	return p
}

func (p Pipeline) Recompute(ctx context.Context, instrument domain.Instrument, tradeDate time.Time, spread SpreadResult) (domain.FeatureSet, error) {
	from := tradeDate.AddDate(0, 0, -p.cfg.RollingLong-5)
	to := dateOnly(tradeDate).AddDate(0, 0, -1)
	candles, err := p.repo.ListDailyCandles(ctx, instrument.InstrumentUID, from, to)
	if err != nil {
		return domain.FeatureSet{}, err
	}
	entryVolume, err := p.intervalVolume(ctx, instrument, tradeDate, p.cfg.EntryWindow)
	if err != nil {
		return domain.FeatureSet{}, err
	}
	exitVolume, err := p.intervalVolume(ctx, instrument, tradeDate, p.cfg.ExitWindow)
	if err != nil {
		return domain.FeatureSet{}, err
	}
	feature, err := Compute(instrument, candles, tradeDate, spread, p.cfg, entryVolume, exitVolume)
	if err != nil {
		return domain.FeatureSet{}, err
	}
	if err := p.repo.UpsertFeature(ctx, feature); err != nil {
		return domain.FeatureSet{}, err
	}
	return feature, nil
}

func (p Pipeline) intervalVolume(ctx context.Context, instrument domain.Instrument, date time.Time, window timeutil.Window) (decimal.Decimal, error) {
	if window.Start.Duration == 0 && window.End.Duration == 0 {
		return decimal.Zero, nil
	}
	loc := p.cfg.Location
	if loc == nil {
		loc = time.UTC
	}
	lookback := p.cfg.IntervalVolumeLookback
	if lookback <= 0 {
		lookback = defaultIntervalVolumeLookback
	}
	toDate := dateOnly(date).AddDate(0, 0, -1)
	from := window.Start.On(toDate.AddDate(0, 0, -lookback+1), loc).UTC()
	to := window.End.On(toDate, loc).UTC()
	candles, err := p.repo.ListMinuteCandles(ctx, instrument.InstrumentUID, from, to)
	if err != nil {
		return decimal.Zero, err
	}
	return AverageIntervalVolume(candles, instrument.Lot, window, loc), nil
}

func Compute(instrument domain.Instrument, candles []domain.Candle, tradeDate time.Time, spread SpreadResult, cfg PipelineConfig, entryVolume, exitVolume decimal.Decimal) (domain.FeatureSet, error) {
	candles = historicalDailyCandles(candles, tradeDate)
	if len(candles) < 2 {
		return domain.FeatureSet{}, fmt.Errorf("need at least 2 candles, got %d", len(candles))
	}
	var overnight []float64
	var lastROn decimal.Decimal
	var lastRDay decimal.Decimal
	calendar := tradingCalendarFrom(cfg.TradingDays)
	for i := 1; i < len(candles); i++ {
		if !consecutiveDailyCandles(candles[i-1].TradeDate, candles[i].TradeDate, calendar) {
			continue
		}
		rOn, err := OvernightReturn(candles[i].Open, candles[i-1].Close)
		if err != nil {
			return domain.FeatureSet{}, err
		}
		rDay, err := IntradayReturn(candles[i].Close, candles[i].Open)
		if err != nil {
			return domain.FeatureSet{}, err
		}
		onFloat, _ := rOn.Float64()
		overnight = append(overnight, onFloat)
		lastROn = rOn
		lastRDay = rDay
	}
	if len(overnight) == 0 {
		return domain.FeatureSet{}, fmt.Errorf("need at least 1 consecutive daily candle pair")
	}
	short := Rolling(overnight, cfg.RollingShort, cfg.EWMALambda)
	long := Rolling(overnight, cfg.RollingLong, cfg.EWMALambda)
	q05Abs := rollingQ05Abs(overnight, cfg.RollingShort)
	adv := ADV(candles, instrument.Lot, 20)
	rawEdgeBps := decimal.NewFromFloat(short.Mean).Mul(decimal.NewFromInt(10_000))
	commission := roundTripCommissionBps(instrument, cfg)
	expectedCost := spread.SpreadBps.
		Add(cfg.EntrySlippageBps).
		Add(cfg.ExitSlippageBps).
		Add(commission).
		Add(cfg.RiskBufferBps)
	costBreakdownJSON := expectedCostBreakdownJSON(spread, cfg, commission, expectedCost)
	return domain.FeatureSet{
		InstrumentUID:       instrument.InstrumentUID,
		TradeDate:           tradeDate,
		ROn:                 lastROn,
		RDay:                lastRDay,
		MuOn60:              decimal.NewFromFloat(short.Mean),
		MuOn252:             decimal.NewFromFloat(long.Mean),
		SigmaOn60:           decimal.NewFromFloat(short.StdDev),
		Q05On60Abs:          q05Abs,
		TStatOn60:           decimal.NewFromFloat(short.TStat),
		WinOn60:             decimal.NewFromFloat(short.WinRate),
		EWMAOn:              decimal.NewFromFloat(short.EWMA),
		SpreadBps:           spread.SpreadBps,
		HalfSpreadBps:       spread.HalfSpreadBps,
		TickBps:             spread.TickBps,
		ADV20:               adv,
		ExpectedCostBps:     expectedCost,
		CostBreakdownJSON:   costBreakdownJSON,
		NetEdgeBps:          rawEdgeBps.Sub(expectedCost),
		EntryIntervalVolume: entryVolume,
		ExitIntervalVolume:  exitVolume,
		CalculatedAt:        time.Now().UTC(),
	}, nil
}

func expectedCostBreakdownJSON(spread SpreadResult, cfg PipelineConfig, commission, expectedCost decimal.Decimal) string {
	spreadEntry := spread.HalfSpreadBps
	if spreadEntry.IsZero() && spread.SpreadBps.IsPositive() {
		spreadEntry = spread.SpreadBps.Div(decimal.NewFromInt(2))
	}
	spreadExit := spread.SpreadBps.Sub(spreadEntry)
	payload := map[string]string{
		"expected_spread_entry_bps":   spreadEntry.String(),
		"expected_spread_exit_bps":    spreadExit.String(),
		"expected_slippage_entry_bps": cfg.EntrySlippageBps.String(),
		"expected_slippage_exit_bps":  cfg.ExitSlippageBps.String(),
		"commission_roundtrip_bps":    commission.String(),
		"risk_buffer_bps":             cfg.RiskBufferBps.String(),
		"expected_cost_bps":           expectedCost.String(),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func rollingQ05Abs(values []float64, window int) decimal.Decimal {
	if window <= 0 || len(values) < window {
		return decimal.Zero
	}
	sample := values[len(values)-window:]
	q05 := decimal.NewFromFloat(Quantile(sample, 0.05))
	if q05.IsNegative() {
		return q05.Neg()
	}
	return q05
}

func roundTripCommissionBps(instrument domain.Instrument, cfg PipelineConfig) decimal.Decimal {
	instrumentCommission := instrument.ExpectedCommissionBpsPerSide.Mul(decimal.NewFromInt(2))
	if instrumentCommission.IsPositive() {
		return instrumentCommission
	}
	return cfg.CommissionRoundtripBps
}

func historicalDailyCandles(candles []domain.Candle, tradeDate time.Time) []domain.Candle {
	tradeDay := dateOnly(tradeDate)
	out := make([]domain.Candle, 0, len(candles))
	for _, candle := range candles {
		if dateOnly(candle.TradeDate).Before(tradeDay) {
			out = append(out, candle)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].TradeDate.Before(out[j].TradeDate)
	})
	return out
}

type tradingCalendar map[string]struct{}

func tradingCalendarFrom(days []time.Time) tradingCalendar {
	if len(days) == 0 {
		return nil
	}
	calendar := make(tradingCalendar, len(days))
	for _, day := range days {
		calendar[dateOnly(day).Format("2006-01-02")] = struct{}{}
	}
	return calendar
}

func consecutiveDailyCandles(previous, current time.Time, calendar tradingCalendar) bool {
	prevDay := dateOnly(previous)
	currentDay := dateOnly(current)
	if !currentDay.After(prevDay) {
		return false
	}
	if len(calendar) > 0 {
		tradingDays := 0
		for day := prevDay.AddDate(0, 0, 1); !day.After(currentDay); day = day.AddDate(0, 0, 1) {
			if _, ok := calendar[day.Format("2006-01-02")]; ok {
				tradingDays++
			}
		}
		return tradingDays == 1
	}
	weekdays := 0
	for day := prevDay.AddDate(0, 0, 1); !day.After(currentDay); day = day.AddDate(0, 0, 1) {
		if day.Weekday() != time.Saturday && day.Weekday() != time.Sunday {
			weekdays++
		}
	}
	return weekdays == 1
}

func dateOnly(ts time.Time) time.Time {
	year, month, day := ts.UTC().Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func IntervalVolume(candles []domain.Candle, lot int64) decimal.Decimal {
	if lot <= 0 {
		return decimal.Zero
	}
	total := decimal.Zero
	for _, candle := range candles {
		total = total.Add(candle.VolumeLots.Mul(decimal.NewFromInt(lot)).Mul(candle.Close))
	}
	return total
}

func AverageIntervalVolume(candles []domain.Candle, lot int64, window timeutil.Window, loc *time.Location) decimal.Decimal {
	if lot <= 0 || len(candles) == 0 {
		return decimal.Zero
	}
	if loc == nil {
		loc = time.UTC
	}
	byDate := make(map[string][]domain.Candle)
	for _, candle := range candles {
		local := candle.TradeDate.In(loc)
		tod := time.Duration(local.Hour())*time.Hour +
			time.Duration(local.Minute())*time.Minute +
			time.Duration(local.Second())*time.Second
		if tod < window.Start.Duration || tod > window.End.Duration {
			continue
		}
		key := local.Format("2006-01-02")
		byDate[key] = append(byDate[key], candle)
	}
	if len(byDate) == 0 {
		return decimal.Zero
	}
	keys := make([]string, 0, len(byDate))
	for key := range byDate {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	sum := decimal.Zero
	for _, key := range keys {
		sum = sum.Add(IntervalVolume(byDate[key], lot))
	}
	return sum.Div(decimal.NewFromInt(int64(len(keys))))
}

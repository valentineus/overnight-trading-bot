package features

import (
	"context"
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
	Location               *time.Location
}

type Pipeline struct {
	repo repository.Repository
	cfg  PipelineConfig
}

func NewPipeline(repo repository.Repository, cfg PipelineConfig) Pipeline {
	return Pipeline{repo: repo, cfg: cfg}
}

func (p Pipeline) Recompute(ctx context.Context, instrument domain.Instrument, tradeDate time.Time, spread SpreadResult) (domain.FeatureSet, error) {
	from := tradeDate.AddDate(0, 0, -p.cfg.RollingLong-5)
	candles, err := p.repo.ListDailyCandles(ctx, instrument.InstrumentUID, from, tradeDate)
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
	from := window.Start.On(date.AddDate(0, 0, -lookback), loc).UTC()
	to := window.End.On(date, loc).UTC()
	candles, err := p.repo.ListMinuteCandles(ctx, instrument.InstrumentUID, from, to)
	if err != nil {
		return decimal.Zero, err
	}
	return AverageIntervalVolume(candles, instrument.Lot, window, loc), nil
}

func Compute(instrument domain.Instrument, candles []domain.Candle, tradeDate time.Time, spread SpreadResult, cfg PipelineConfig, entryVolume, exitVolume decimal.Decimal) (domain.FeatureSet, error) {
	if len(candles) < 2 {
		return domain.FeatureSet{}, fmt.Errorf("need at least 2 candles, got %d", len(candles))
	}
	var overnight []float64
	var lastROn decimal.Decimal
	var lastRDay decimal.Decimal
	for i := 1; i < len(candles); i++ {
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
	short := Rolling(overnight, cfg.RollingShort, cfg.EWMALambda)
	long := Rolling(overnight, cfg.RollingLong, cfg.EWMALambda)
	adv := ADV(candles, instrument.Lot, 20)
	rawEdgeBps := decimal.NewFromFloat(short.Mean).Mul(decimal.NewFromInt(10_000))
	if !entryVolume.IsPositive() {
		entryVolume = adv
	}
	if !exitVolume.IsPositive() {
		exitVolume = adv
	}
	instrumentCommission := instrument.ExpectedCommissionBpsPerSide.Mul(decimal.NewFromInt(2))
	expectedCost := spread.SpreadBps.
		Add(cfg.EntrySlippageBps).
		Add(cfg.ExitSlippageBps).
		Add(cfg.CommissionRoundtripBps).
		Add(instrumentCommission).
		Add(cfg.RiskBufferBps)
	return domain.FeatureSet{
		InstrumentUID:       instrument.InstrumentUID,
		TradeDate:           tradeDate,
		ROn:                 lastROn,
		RDay:                lastRDay,
		MuOn60:              decimal.NewFromFloat(short.Mean),
		MuOn252:             decimal.NewFromFloat(long.Mean),
		SigmaOn60:           decimal.NewFromFloat(short.StdDev),
		TStatOn60:           decimal.NewFromFloat(short.TStat),
		WinOn60:             decimal.NewFromFloat(short.WinRate),
		EWMAOn:              decimal.NewFromFloat(short.EWMA),
		SpreadBps:           spread.SpreadBps,
		HalfSpreadBps:       spread.HalfSpreadBps,
		TickBps:             spread.TickBps,
		ADV20:               adv,
		ExpectedCostBps:     expectedCost,
		NetEdgeBps:          rawEdgeBps.Sub(expectedCost),
		EntryIntervalVolume: entryVolume,
		ExitIntervalVolume:  exitVolume,
		CalculatedAt:        time.Now().UTC(),
	}, nil
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

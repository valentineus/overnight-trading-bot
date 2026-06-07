package features

import (
	"errors"
	"math"
	"sort"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/money"
)

var ErrInvalidPrice = errors.New("price must be positive")

func OvernightReturn(open, previousClose decimal.Decimal) (decimal.Decimal, error) {
	if !open.IsPositive() || !previousClose.IsPositive() {
		return decimal.Zero, ErrInvalidPrice
	}
	return open.Div(previousClose).Sub(decimal.NewFromInt(1)), nil
}

func IntradayReturn(close, open decimal.Decimal) (decimal.Decimal, error) {
	if !close.IsPositive() || !open.IsPositive() {
		return decimal.Zero, ErrInvalidPrice
	}
	return close.Div(open).Sub(decimal.NewFromInt(1)), nil
}

func LogReturn(to, from decimal.Decimal) (float64, error) {
	if !to.IsPositive() || !from.IsPositive() {
		return 0, ErrInvalidPrice
	}
	ratio, _ := to.Div(from).Float64()
	return math.Log(ratio), nil
}

func CumulativeLinear(returns []decimal.Decimal) decimal.Decimal {
	total := decimal.NewFromInt(1)
	for _, r := range returns {
		total = total.Mul(decimal.NewFromInt(1).Add(r))
	}
	return total.Sub(decimal.NewFromInt(1))
}

func CumulativeLog(logReturns []float64) float64 {
	sum := 0.0
	for _, r := range logReturns {
		sum += r
	}
	return math.Exp(sum) - 1
}

type RollingResult struct {
	Mean      float64
	StdDev    float64
	TStat     float64
	WinRate   float64
	EWMA      float64
	Available bool
}

func Rolling(values []float64, window int, lambda float64) RollingResult {
	if window <= 0 || len(values) < window {
		return RollingResult{}
	}
	sample := values[len(values)-window:]
	mean := Mean(sample)
	std := StdDev(sample)
	win := WinRate(sample)
	ewma := EWMA(values, lambda)
	res := RollingResult{
		Mean:      mean,
		StdDev:    std,
		WinRate:   win,
		EWMA:      ewma,
		Available: true,
	}
	if std > 0 {
		res.TStat = mean / std * math.Sqrt(float64(window))
	}
	return res
}

func Mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

func StdDev(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	mean := Mean(values)
	sum := 0.0
	for _, value := range values {
		diff := value - mean
		sum += diff * diff
	}
	return math.Sqrt(sum / float64(len(values)-1))
}

func WinRate(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	wins := 0
	for _, value := range values {
		if value > 0 {
			wins++
		}
	}
	return float64(wins) / float64(len(values))
}

func EWMA(values []float64, lambda float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if lambda <= 0 || lambda > 1 {
		lambda = 0.08
	}
	ewma := values[0]
	for _, value := range values[1:] {
		ewma = lambda*value + (1-lambda)*ewma
	}
	return ewma
}

type SpreadResult struct {
	SpreadAbs     decimal.Decimal
	SpreadBps     decimal.Decimal
	HalfSpreadBps decimal.Decimal
	TickBps       decimal.Decimal
	Mid           decimal.Decimal
}

func Spread(bestBid, bestAsk, tick decimal.Decimal) (SpreadResult, error) {
	if !bestBid.IsPositive() || !bestAsk.IsPositive() || bestAsk.LessThanOrEqual(bestBid) {
		return SpreadResult{}, ErrInvalidPrice
	}
	mid := bestAsk.Add(bestBid).Div(decimal.NewFromInt(2))
	spreadAbs := bestAsk.Sub(bestBid)
	spreadBps, err := money.Bps(spreadAbs, mid)
	if err != nil {
		return SpreadResult{}, err
	}
	tickBps := decimal.Zero
	if tick.IsPositive() {
		tickBps, err = money.Bps(tick, mid)
		if err != nil {
			return SpreadResult{}, err
		}
	}
	return SpreadResult{
		SpreadAbs:     spreadAbs,
		SpreadBps:     spreadBps,
		HalfSpreadBps: spreadBps.Div(decimal.NewFromInt(2)),
		TickBps:       tickBps,
		Mid:           mid,
	}, nil
}

func ADV(candles []domain.Candle, lot int64, window int) decimal.Decimal {
	if lot <= 0 || window <= 0 || len(candles) == 0 {
		return decimal.Zero
	}
	sort.Slice(candles, func(i, j int) bool {
		return candles[i].TradeDate.Before(candles[j].TradeDate)
	})
	if len(candles) > window {
		candles = candles[len(candles)-window:]
	}
	total := decimal.Zero
	for _, candle := range candles {
		total = total.Add(candle.VolumeLots.Mul(decimal.NewFromInt(lot)).Mul(candle.Close))
	}
	return total.Div(decimal.NewFromInt(int64(len(candles))))
}

func Quantile(values []float64, q float64) float64 {
	if len(values) == 0 {
		return 0
	}
	cp := append([]float64(nil), values...)
	sort.Float64s(cp)
	if q <= 0 {
		return cp[0]
	}
	if q >= 1 {
		return cp[len(cp)-1]
	}
	pos := q * float64(len(cp)-1)
	lower := int(math.Floor(pos))
	upper := int(math.Ceil(pos))
	if lower == upper {
		return cp[lower]
	}
	weight := pos - float64(lower)
	return cp[lower]*(1-weight) + cp[upper]*weight
}

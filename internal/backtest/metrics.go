package backtest

import (
	"math"
	"sort"

	"github.com/shopspring/decimal"
)

type Metrics struct {
	TotalReturn          float64 `json:"total_return"`
	CAGR                 float64 `json:"cagr"`
	AnnualizedVolatility float64 `json:"annualized_volatility"`
	SharpeRatio          float64 `json:"sharpe_ratio"`
	SortinoRatio         float64 `json:"sortino_ratio"`
	MaxDrawdown          float64 `json:"max_drawdown"`
	CalmarRatio          float64 `json:"calmar_ratio"`
	WinRate              float64 `json:"win_rate"`
	AverageTradeReturn   float64 `json:"average_trade_return"`
	MedianTradeReturn    float64 `json:"median_trade_return"`
	ProfitFactor         float64 `json:"profit_factor"`
	AverageSpreadBps     float64 `json:"average_spread_bps"`
	AverageSlippageBps   float64 `json:"average_slippage_bps"`
	NumberOfTrades       int     `json:"number_of_trades"`
	WorstOvernightGap    float64 `json:"worst_overnight_gap"`
	VaR95                float64 `json:"var_95"`
	CVaR95               float64 `json:"cvar_95"`
	CapacityEstimate     float64 `json:"capacity_estimate"`
}

func ComputeMetrics(points []Point, trades []Trade) Metrics {
	if len(points) == 0 {
		return Metrics{}
	}
	start, _ := points[0].Equity.Float64()
	end, _ := points[len(points)-1].Equity.Float64()
	returns := make([]float64, 0, len(points)-1)
	for _, point := range points[1:] {
		r, _ := point.Return.Float64()
		returns = append(returns, r)
	}
	tradeReturns := make([]float64, 0, len(trades))
	spreads := make([]float64, 0, len(trades))
	slippages := make([]float64, 0, len(trades))
	profits := 0.0
	losses := 0.0
	wins := 0
	worstGap := 0.0
	capacity := 0.0
	for _, trade := range trades {
		r, _ := trade.Return.Float64()
		tradeReturns = append(tradeReturns, r)
		spread, _ := trade.SpreadBps.Float64()
		spreads = append(spreads, spread)
		slippage, _ := trade.SlippageBps.Float64()
		slippages = append(slippages, slippage)
		if r > 0 {
			wins++
			profits += r
		} else {
			losses += r
		}
		gap, _ := trade.OvernightGap.Float64()
		if gap < worstGap {
			worstGap = gap
		}
		tradeCapacity, _ := trade.CapacityRUB.Float64()
		if tradeCapacity > 0 && (capacity == 0 || tradeCapacity < capacity) {
			capacity = tradeCapacity
		}
	}
	totalReturn := 0.0
	if start > 0 {
		totalReturn = end/start - 1
	}
	vol := stddev(returns) * math.Sqrt(252)
	meanReturn := mean(returns)
	sharpe := 0.0
	if std := stddev(returns); std > 0 {
		sharpe = meanReturn / std * math.Sqrt(252)
	}
	sortino := 0.0
	if down := downsideStddev(returns); down > 0 {
		sortino = meanReturn / down * math.Sqrt(252)
	}
	tradingDays := math.Max(float64(len(returns)), 1)
	cagr := 0.0
	if start > 0 && end > 0 {
		cagr = math.Pow(end/start, 252/tradingDays) - 1
	}
	maxDD := maxDrawdown(points)
	calmar := 0.0
	if maxDD != 0 {
		calmar = cagr / math.Abs(maxDD)
	}
	pf := 0.0
	if losses != 0 {
		pf = profits / math.Abs(losses)
	}
	var95 := percentile(returns, 0.05)
	cvar95 := conditionalMean(returns, var95)
	return Metrics{
		TotalReturn:          totalReturn,
		CAGR:                 cagr,
		AnnualizedVolatility: vol,
		SharpeRatio:          sharpe,
		SortinoRatio:         sortino,
		MaxDrawdown:          maxDD,
		CalmarRatio:          calmar,
		WinRate:              ratio(wins, len(tradeReturns)),
		AverageTradeReturn:   mean(tradeReturns),
		MedianTradeReturn:    percentile(tradeReturns, 0.50),
		ProfitFactor:         pf,
		AverageSpreadBps:     mean(spreads),
		AverageSlippageBps:   mean(slippages),
		NumberOfTrades:       len(trades),
		WorstOvernightGap:    worstGap,
		VaR95:                var95,
		CVaR95:               cvar95,
		CapacityEstimate:     capacity,
	}
}

func maxDrawdown(points []Point) float64 {
	peak := 0.0
	maxDD := 0.0
	for _, point := range points {
		e, _ := point.Equity.Float64()
		if e > peak {
			peak = e
		}
		if peak > 0 {
			dd := e/peak - 1
			if dd < maxDD {
				maxDD = dd
			}
		}
	}
	return maxDD
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

func stddev(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	m := mean(values)
	sum := 0.0
	for _, value := range values {
		diff := value - m
		sum += diff * diff
	}
	return math.Sqrt(sum / float64(len(values)-1))
}

func downsideStddev(values []float64) float64 {
	var downs []float64
	for _, value := range values {
		if value < 0 {
			downs = append(downs, value)
		}
	}
	return stddev(downs)
}

func percentile(values []float64, q float64) float64 {
	if len(values) == 0 {
		return 0
	}
	cp := append([]float64(nil), values...)
	sort.Float64s(cp)
	pos := q * float64(len(cp)-1)
	lower := int(math.Floor(pos))
	upper := int(math.Ceil(pos))
	if lower == upper {
		return cp[lower]
	}
	weight := pos - float64(lower)
	return cp[lower]*(1-weight) + cp[upper]*weight
}

func conditionalMean(values []float64, threshold float64) float64 {
	var selected []float64
	for _, value := range values {
		if value <= threshold {
			selected = append(selected, value)
		}
	}
	return mean(selected)
}

func ratio(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

func point(date string, equity, ret string) Point {
	e, _ := decimal.NewFromString(equity)
	r, _ := decimal.NewFromString(ret)
	return Point{Date: date, Equity: e, Return: r}
}

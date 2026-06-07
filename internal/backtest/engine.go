package backtest

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/features"
	"overnight-trading-bot/internal/money"
	"overnight-trading-bot/internal/risk"
)

type Config struct {
	EntrySlippageBps       decimal.Decimal
	ExitSlippageBps        decimal.Decimal
	CommissionRoundtripBps decimal.Decimal
	InitialEquity          decimal.Decimal
	OutputDir              string
	RollingShort           int
	RollingLong            int
	EWMALambda             float64
	MinTStat60             decimal.Decimal
	MinWinRate60           decimal.Decimal
	MinNetEdgeBps          decimal.Decimal
	MinADVRUB              decimal.Decimal
	MaxSpreadBps           decimal.Decimal
	MaxTickBps             decimal.Decimal
	RequireZeroCommission  bool
	MaxPositions           int
	MaxPositionPct         decimal.Decimal
	MaxTotalExposurePct    decimal.Decimal
	MaxParticipationRate   decimal.Decimal
	CashUsageBuffer        decimal.Decimal
	RiskBudgetPct          decimal.Decimal
	MinOrderNotionalRUB    decimal.Decimal
	AssumedSpreadBps       decimal.Decimal
	AssumedTickBps         decimal.Decimal
	Lot                    int64
	UseMinuteModel         bool
	EntryWindow            TimeWindow
	ExitWindow             TimeWindow
}

type TimeWindow struct {
	Start time.Duration
	End   time.Duration
}

type Trade struct {
	InstrumentUID string          `json:"instrument_uid"`
	EntryDate     string          `json:"entry_date"`
	ExitDate      string          `json:"exit_date"`
	BuyPrice      decimal.Decimal `json:"buy_price"`
	SellPrice     decimal.Decimal `json:"sell_price"`
	Return        decimal.Decimal `json:"return"`
	Lots          int64           `json:"lots"`
	Notional      decimal.Decimal `json:"notional"`
	NetPnL        decimal.Decimal `json:"net_pnl"`
	SpreadBps     decimal.Decimal `json:"spread_bps"`
	SlippageBps   decimal.Decimal `json:"slippage_bps"`
	OvernightGap  decimal.Decimal `json:"overnight_gap"`
	CapacityRUB   decimal.Decimal `json:"capacity_rub"`
}

type Result struct {
	Metrics Metrics `json:"metrics"`
	Trades  []Trade `json:"trades"`
	Equity  []Point `json:"equity"`
}

type Point struct {
	Date   string          `json:"date"`
	Equity decimal.Decimal `json:"equity"`
	Return decimal.Decimal `json:"return"`
}

type Engine struct {
	cfg Config
}

func New(cfg Config) Engine {
	cfg = cfg.withDefaults()
	return Engine{cfg: cfg}
}

func (cfg Config) withDefaults() Config {
	if cfg.InitialEquity.IsZero() {
		cfg.InitialEquity = decimal.NewFromInt(100_000)
	}
	if cfg.RollingShort == 0 {
		cfg.RollingShort = 60
	}
	if cfg.RollingLong == 0 {
		cfg.RollingLong = 252
	}
	if cfg.EWMALambda == 0 {
		cfg.EWMALambda = 0.08
	}
	if cfg.MinTStat60.IsZero() {
		cfg.MinTStat60 = decimal.NewFromFloat(1.25)
	}
	if cfg.MinWinRate60.IsZero() {
		cfg.MinWinRate60 = decimal.NewFromFloat(0.55)
	}
	if cfg.MinNetEdgeBps.IsZero() {
		cfg.MinNetEdgeBps = decimal.NewFromInt(10)
	}
	if cfg.MinADVRUB.IsZero() {
		cfg.MinADVRUB = decimal.NewFromInt(5_000_000)
	}
	if cfg.MaxSpreadBps.IsZero() {
		cfg.MaxSpreadBps = decimal.NewFromInt(20)
	}
	if cfg.MaxTickBps.IsZero() {
		cfg.MaxTickBps = decimal.NewFromInt(10)
	}
	if !cfg.RequireZeroCommission && cfg.CommissionRoundtripBps.IsZero() {
		cfg.RequireZeroCommission = true
	}
	if cfg.MaxPositions == 0 {
		cfg.MaxPositions = 5
	}
	if cfg.MaxPositionPct.IsZero() {
		cfg.MaxPositionPct = decimal.NewFromFloat(0.10)
	}
	if cfg.MaxTotalExposurePct.IsZero() {
		cfg.MaxTotalExposurePct = decimal.NewFromFloat(0.50)
	}
	if cfg.MaxParticipationRate.IsZero() {
		cfg.MaxParticipationRate = decimal.NewFromFloat(0.01)
	}
	if cfg.CashUsageBuffer.IsZero() {
		cfg.CashUsageBuffer = decimal.NewFromFloat(0.95)
	}
	if cfg.RiskBudgetPct.IsZero() {
		cfg.RiskBudgetPct = decimal.NewFromFloat(0.005)
	}
	if cfg.MinOrderNotionalRUB.IsZero() {
		cfg.MinOrderNotionalRUB = decimal.NewFromInt(1000)
	}
	if cfg.Lot == 0 {
		cfg.Lot = 1
	}
	if cfg.EntryWindow.Start == 0 && cfg.EntryWindow.End == 0 {
		cfg.EntryWindow = TimeWindow{Start: durationOfDay(18, 20, 0), End: durationOfDay(18, 38, 30)}
	}
	if cfg.ExitWindow.Start == 0 && cfg.ExitWindow.End == 0 {
		cfg.ExitWindow = TimeWindow{Start: durationOfDay(10, 5, 0), End: durationOfDay(10, 25, 0)}
	}
	return cfg
}

func (e Engine) Run(candlesByInstrument map[string][]domain.Candle) (Result, error) {
	return e.RunWithMinuteCandles(candlesByInstrument, nil)
}

func (e Engine) RunWithMinuteCandles(candlesByInstrument map[string][]domain.Candle, minuteCandlesByInstrument map[string][]domain.Candle) (Result, error) {
	prepared := prepareCandles(candlesByInstrument)
	preparedMinutes := prepareCandles(minuteCandlesByInstrument)
	candidatesByExitDate := make(map[string][]candidate)
	tradingDateSet := make(map[string]struct{})
	for instrumentUID, candles := range prepared {
		for i := 1; i < len(candles); i++ {
			if i >= e.cfg.RollingShort {
				tradingDateSet[candles[i].TradeDate.Format("2006-01-02")] = struct{}{}
			}
			candidate, ok, err := e.evaluateCandidate(instrumentUID, candles, i)
			if err != nil {
				return Result{}, err
			}
			if ok {
				candidatesByExitDate[candidate.exit.TradeDate.Format("2006-01-02")] = append(candidatesByExitDate[candidate.exit.TradeDate.Format("2006-01-02")], candidate)
			}
		}
	}
	dates := make([]string, 0, len(tradingDateSet))
	for date := range tradingDateSet {
		dates = append(dates, date)
	}
	sort.Strings(dates)

	equity := e.cfg.InitialEquity
	cash := e.cfg.InitialEquity
	var trades []Trade
	points := []Point{{Date: "START", Equity: equity}}
	sizer := risk.NewSizer(risk.SizingConfig{
		MaxPositionPct:             e.cfg.MaxPositionPct,
		MaxTotalExposurePct:        e.cfg.MaxTotalExposurePct,
		MaxParticipationRate:       e.cfg.MaxParticipationRate,
		CashUsageBuffer:            e.cfg.CashUsageBuffer,
		RiskBudgetPerInstrumentPct: e.cfg.RiskBudgetPct,
		MinOrderNotionalRUB:        e.cfg.MinOrderNotionalRUB,
	})
	for _, date := range dates {
		dayCandidates := candidatesByExitDate[date]
		sort.Slice(dayCandidates, func(i, j int) bool {
			if dayCandidates[i].netEdge.Equal(dayCandidates[j].netEdge) {
				return dayCandidates[i].instrumentUID < dayCandidates[j].instrumentUID
			}
			return dayCandidates[i].netEdge.GreaterThan(dayCandidates[j].netEdge)
		})
		if len(dayCandidates) > e.cfg.MaxPositions {
			dayCandidates = dayCandidates[:e.cfg.MaxPositions]
		}
		dayStartEquity := equity
		dayPnL := decimal.Zero
		for _, c := range dayCandidates {
			sized := sizer.Size(risk.SizingInput{
				Portfolio:           domain.Portfolio{Equity: equity, Cash: cash},
				SelectedInstruments: len(dayCandidates),
				LimitPrice:          c.buy,
				Lot:                 e.cfg.Lot,
				EntryIntervalVolume: c.adv,
				ExitIntervalVolume:  c.adv,
				Q05OvernightAbs:     c.q05Abs,
			})
			if sized.Lots <= 0 {
				continue
			}
			lots := sized.Lots
			capacity := c.capacity
			if e.cfg.UseMinuteModel {
				executedLots, minuteCapacity, ok := e.minuteExecution(c, preparedMinutes[c.instrumentUID], sized.Lots)
				if !ok {
					continue
				}
				lots = executedLots
				capacity = minuteCapacity
			}
			notional := c.buy.Mul(decimal.NewFromInt(lots)).Mul(decimal.NewFromInt(e.cfg.Lot))
			ret := c.sell.Div(c.buy).Sub(decimal.NewFromInt(1)).Sub(money.FromBps(e.cfg.CommissionRoundtripBps))
			pnl := notional.Mul(ret)
			dayPnL = dayPnL.Add(pnl)
			cash = cash.Sub(notional)
			trades = append(trades, Trade{
				InstrumentUID: c.instrumentUID,
				EntryDate:     c.entry.TradeDate.Format("2006-01-02"),
				ExitDate:      c.exit.TradeDate.Format("2006-01-02"),
				BuyPrice:      c.buy,
				SellPrice:     c.sell,
				Return:        ret,
				Lots:          lots,
				Notional:      notional,
				NetPnL:        pnl,
				SpreadBps:     e.cfg.AssumedSpreadBps,
				SlippageBps:   e.cfg.EntrySlippageBps.Add(e.cfg.ExitSlippageBps),
				OvernightGap:  c.overnightGap,
				CapacityRUB:   capacity,
			})
		}
		equity = equity.Add(dayPnL)
		cash = equity
		dayReturn := decimal.Zero
		if dayStartEquity.IsPositive() {
			dayReturn = dayPnL.Div(dayStartEquity)
		}
		points = append(points, Point{
			Date:   date,
			Equity: equity,
			Return: dayReturn,
		})
	}
	sort.Slice(trades, func(i, j int) bool {
		if trades[i].ExitDate == trades[j].ExitDate {
			return trades[i].InstrumentUID < trades[j].InstrumentUID
		}
		return trades[i].ExitDate < trades[j].ExitDate
	})
	return Result{
		Metrics: ComputeMetrics(points, trades),
		Trades:  trades,
		Equity:  points,
	}, nil
}

func (e Engine) minuteExecution(c candidate, minutes []domain.Candle, requestedLots int64) (int64, decimal.Decimal, bool) {
	if requestedLots <= 0 || len(minutes) == 0 {
		return 0, decimal.Zero, false
	}
	entryLots, entryCapacity := e.fillableMinuteLots(minutes, c.entry.TradeDate, c.buy, domain.SideBuy, e.cfg.EntryWindow)
	exitLots, exitCapacity := e.fillableMinuteLots(minutes, c.exit.TradeDate, c.sell, domain.SideSell, e.cfg.ExitWindow)
	lots := min(requestedLots, entryLots)
	lots = min(lots, exitLots)
	if lots <= 0 {
		return 0, decimal.Zero, false
	}
	return lots, money.Min(entryCapacity, exitCapacity), true
}

func (e Engine) fillableMinuteLots(minutes []domain.Candle, date time.Time, limitPrice decimal.Decimal, side domain.Side, window TimeWindow) (int64, decimal.Decimal) {
	if !limitPrice.IsPositive() || e.cfg.Lot <= 0 {
		return 0, decimal.Zero
	}
	lotNotional := limitPrice.Mul(decimal.NewFromInt(e.cfg.Lot))
	if !lotNotional.IsPositive() {
		return 0, decimal.Zero
	}
	capacity := decimal.Zero
	for _, candle := range minutes {
		if !sameDate(candle.TradeDate, date) {
			continue
		}
		if !window.Contains(candle.TradeDate) {
			continue
		}
		reachable := side == domain.SideBuy && candle.Low.LessThanOrEqual(limitPrice)
		reachable = reachable || side == domain.SideSell && candle.High.GreaterThanOrEqual(limitPrice)
		if !reachable {
			continue
		}
		minuteCapacity := candle.VolumeLots.Mul(lotNotional).Mul(e.cfg.MaxParticipationRate)
		capacity = capacity.Add(minuteCapacity)
	}
	return capacity.Div(lotNotional).Floor().IntPart(), capacity
}

func (w TimeWindow) Contains(ts time.Time) bool {
	if w.Start == 0 && w.End == 0 {
		return true
	}
	tod := time.Duration(ts.Hour())*time.Hour +
		time.Duration(ts.Minute())*time.Minute +
		time.Duration(ts.Second())*time.Second
	return tod >= w.Start && tod <= w.End
}

func durationOfDay(hour, minute, second int) time.Duration {
	return time.Duration(hour)*time.Hour +
		time.Duration(minute)*time.Minute +
		time.Duration(second)*time.Second
}

type candidate struct {
	instrumentUID string
	entry         domain.Candle
	exit          domain.Candle
	buy           decimal.Decimal
	sell          decimal.Decimal
	netEdge       decimal.Decimal
	adv           decimal.Decimal
	q05Abs        decimal.Decimal
	overnightGap  decimal.Decimal
	capacity      decimal.Decimal
}

func (e Engine) evaluateCandidate(instrumentUID string, candles []domain.Candle, exitIndex int) (candidate, bool, error) {
	if exitIndex < e.cfg.RollingShort || exitIndex <= 0 {
		return candidate{}, false, nil
	}
	history := candles[:exitIndex]
	returns := make([]float64, 0, len(history)-1)
	for j := 1; j < len(history); j++ {
		r, err := features.OvernightReturn(history[j].Open, history[j-1].Close)
		if err != nil {
			return candidate{}, false, err
		}
		rf, _ := r.Float64()
		returns = append(returns, rf)
	}
	short := features.Rolling(returns, e.cfg.RollingShort, e.cfg.EWMALambda)
	long := features.Rolling(returns, min(e.cfg.RollingLong, len(returns)), e.cfg.EWMALambda)
	if !short.Available || !long.Available || short.StdDev == 0 {
		return candidate{}, false, nil
	}
	rawEdge := decimal.NewFromFloat(short.Mean).Mul(decimal.NewFromInt(10_000))
	cost := e.cfg.AssumedSpreadBps.
		Add(e.cfg.EntrySlippageBps).
		Add(e.cfg.ExitSlippageBps).
		Add(e.cfg.CommissionRoundtripBps)
	netEdge := rawEdge.Sub(cost)
	adv := features.ADV(history, e.cfg.Lot, 20)
	switch {
	case e.cfg.RequireZeroCommission && e.cfg.CommissionRoundtripBps.IsPositive():
		return candidate{}, false, nil
	case !decimal.NewFromFloat(short.Mean).IsPositive() || !decimal.NewFromFloat(long.Mean).IsPositive():
		return candidate{}, false, nil
	case decimal.NewFromFloat(short.TStat).LessThan(e.cfg.MinTStat60):
		return candidate{}, false, nil
	case decimal.NewFromFloat(short.WinRate).LessThan(e.cfg.MinWinRate60):
		return candidate{}, false, nil
	case netEdge.LessThan(e.cfg.MinNetEdgeBps):
		return candidate{}, false, nil
	case e.cfg.AssumedSpreadBps.GreaterThan(e.cfg.MaxSpreadBps):
		return candidate{}, false, nil
	case e.cfg.AssumedTickBps.GreaterThan(e.cfg.MaxTickBps):
		return candidate{}, false, nil
	case adv.LessThan(e.cfg.MinADVRUB):
		return candidate{}, false, nil
	}
	entry := candles[exitIndex-1]
	exit := candles[exitIndex]
	buy := entry.Close.Mul(decimal.NewFromInt(1).Add(money.FromBps(e.cfg.EntrySlippageBps)))
	sell := exit.Open.Mul(decimal.NewFromInt(1).Sub(money.FromBps(e.cfg.ExitSlippageBps)))
	gap, err := features.OvernightReturn(exit.Open, entry.Close)
	if err != nil {
		return candidate{}, false, err
	}
	q05Abs := decimal.NewFromFloat(features.Quantile(returns, 0.05))
	if q05Abs.IsNegative() {
		q05Abs = q05Abs.Neg()
	}
	return candidate{
		instrumentUID: instrumentUID,
		entry:         entry,
		exit:          exit,
		buy:           buy,
		sell:          sell,
		netEdge:       netEdge,
		adv:           adv,
		q05Abs:        q05Abs,
		overnightGap:  gap,
		capacity:      adv.Mul(e.cfg.MaxParticipationRate),
	}, true, nil
}

func prepareCandles(candlesByInstrument map[string][]domain.Candle) map[string][]domain.Candle {
	prepared := make(map[string][]domain.Candle, len(candlesByInstrument))
	for instrumentUID, candles := range candlesByInstrument {
		cp := append([]domain.Candle(nil), candles...)
		sort.Slice(cp, func(i, j int) bool {
			return cp[i].TradeDate.Before(cp[j].TradeDate)
		})
		prepared[instrumentUID] = cp
	}
	return prepared
}

func (r Result) Write(outputDir string) error {
	if outputDir == "" {
		outputDir = "./backtest_out"
	}
	if err := os.MkdirAll(outputDir, 0o750); err != nil {
		return err
	}
	summary, err := json.MarshalIndent(r.Metrics, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outputDir, "summary.json"), summary, 0o600); err != nil {
		return err
	}
	if err := writeTrades(filepath.Join(outputDir, "trades.csv"), r.Trades); err != nil {
		return err
	}
	return writeEquity(filepath.Join(outputDir, "equity.csv"), r.Equity)
}

func LoadCandlesCSV(reader io.Reader) (map[string][]domain.Candle, error) {
	r := csv.NewReader(reader)
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	out := make(map[string][]domain.Candle)
	for i, record := range records {
		if i == 0 && len(record) > 0 && record[0] == "instrument_uid" {
			continue
		}
		if len(record) < 7 {
			return nil, fmt.Errorf("line %d: expected 7 fields", i+1)
		}
		date, err := parseCandleTime(record[1])
		if err != nil {
			return nil, err
		}
		open, err := decimal.NewFromString(record[2])
		if err != nil {
			return nil, err
		}
		high, err := decimal.NewFromString(record[3])
		if err != nil {
			return nil, err
		}
		low, err := decimal.NewFromString(record[4])
		if err != nil {
			return nil, err
		}
		closePrice, err := decimal.NewFromString(record[5])
		if err != nil {
			return nil, err
		}
		volume, err := decimal.NewFromString(record[6])
		if err != nil {
			return nil, err
		}
		candle := domain.Candle{
			InstrumentUID: record[0],
			TradeDate:     date,
			Open:          open,
			High:          high,
			Low:           low,
			Close:         closePrice,
			VolumeLots:    volume,
			Source:        "csv",
			LoadedAt:      time.Now().UTC(),
		}
		out[candle.InstrumentUID] = append(out[candle.InstrumentUID], candle)
	}
	return out, nil
}

func parseCandleTime(raw string) (time.Time, error) {
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	var lastErr error
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, raw)
		if err == nil {
			return parsed.UTC(), nil
		}
		lastErr = err
	}
	return time.Time{}, lastErr
}

func sameDate(a, b time.Time) bool {
	return dateOnly(a).Equal(dateOnly(b))
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func writeTrades(path string, trades []Trade) error {
	// #nosec G304 -- path is the user-selected backtest output destination.
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
	}()
	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{"instrument_uid", "entry_date", "exit_date", "buy_price", "sell_price", "return", "lots", "notional", "net_pnl", "spread_bps", "slippage_bps", "overnight_gap", "capacity_rub"}); err != nil {
		return err
	}
	for _, trade := range trades {
		if err := w.Write([]string{
			trade.InstrumentUID,
			trade.EntryDate,
			trade.ExitDate,
			trade.BuyPrice.String(),
			trade.SellPrice.String(),
			trade.Return.String(),
			fmt.Sprintf("%d", trade.Lots),
			trade.Notional.String(),
			trade.NetPnL.String(),
			trade.SpreadBps.String(),
			trade.SlippageBps.String(),
			trade.OvernightGap.String(),
			trade.CapacityRUB.String(),
		}); err != nil {
			return err
		}
	}
	return w.Error()
}

func writeEquity(path string, points []Point) error {
	// #nosec G304 -- path is the user-selected backtest output destination.
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
	}()
	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{"date", "equity", "return"}); err != nil {
		return err
	}
	for _, point := range points {
		if err := w.Write([]string{point.Date, point.Equity.String(), point.Return.String()}); err != nil {
			return err
		}
	}
	return w.Error()
}

func ParseDecimalFlag(raw string) (decimal.Decimal, error) {
	if raw == "" {
		return decimal.Zero, nil
	}
	return decimal.NewFromString(raw)
}

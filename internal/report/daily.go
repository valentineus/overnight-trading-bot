package report

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
)

type DailyInput struct {
	Date             time.Time
	Mode             domain.Mode
	Signals          []domain.Signal
	Positions        []domain.Position
	Orders           []domain.Order
	AverageSpreadBps decimal.Decimal
	AverageSlipBps   decimal.Decimal
	RiskStatus       string
}

func ComposeDaily(input DailyInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Дата: %s\n", input.Date.Format("2006-01-02"))
	fmt.Fprintf(&b, "Режим: %s\n", input.Mode)
	fmt.Fprintf(&b, "Сигналы: %d\n", len(input.Signals))
	for _, signal := range input.Signals {
		fmt.Fprintf(&b, "- %s %s edge=%s reason=%s\n", signal.InstrumentUID, signal.Decision, signal.NetEdgeBps.StringFixed(2), signal.RejectReason)
	}
	reasons := groupedReasons(input.Signals)
	if len(reasons) > 0 {
		fmt.Fprintf(&b, "Причины skip/reject:\n")
		for _, reason := range sortedKeys(reasons) {
			count := reasons[reason]
			fmt.Fprintf(&b, "- %s: %d\n", reason, count)
		}
	}
	gross := decimal.Zero
	net := decimal.Zero
	commission := decimal.Zero
	expectedByInstrument := expectedEdgeByInstrument(input.Signals)
	for _, pos := range input.Positions {
		gross = gross.Add(pos.GrossPnL)
		net = net.Add(pos.NetPnL)
		commission = commission.Add(pos.CommissionTotal)
	}
	if len(input.Positions) > 0 {
		fmt.Fprintf(&b, "Позиции:\n")
		for _, pos := range input.Positions {
			expected := expectedByInstrument[pos.InstrumentUID]
			expectedError := pos.RealizedEdgeBps.Sub(expected)
			fmt.Fprintf(&b, "- %s status=%s net=%s commission=%s realized_edge_bps=%s expected_error_bps=%s\n",
				pos.InstrumentUID,
				pos.Status,
				pos.NetPnL.StringFixed(2),
				pos.CommissionTotal.StringFixed(2),
				pos.RealizedEdgeBps.StringFixed(2),
				expectedError.StringFixed(2),
			)
		}
	}
	fmt.Fprintf(&b, "Gross PnL: %s\n", gross.StringFixed(2))
	fmt.Fprintf(&b, "Net PnL: %s\n", net.StringFixed(2))
	fmt.Fprintf(&b, "Комиссии: %s\n", commission.StringFixed(2))
	averageSpread := input.AverageSpreadBps
	if averageSpread.IsZero() {
		averageSpread = averageContextDecimal(input.Signals, "spread_bps")
	}
	fmt.Fprintf(&b, "Средний spread: %s bps\n", averageSpread.StringFixed(2))
	averageSlip := input.AverageSlipBps
	if averageSlip.IsZero() {
		averageSlip = AverageAdverseSlippageBps(input.Orders, 0)
	}
	fmt.Fprintf(&b, "Среднее проскальзывание: %s bps\n", averageSlip.StringFixed(2))
	writeExecutionErrors(&b, input.Orders)
	fmt.Fprintf(&b, "Risk: %s", input.RiskStatus)
	return b.String()
}

func AverageAdverseSlippageBps(orders []domain.Order, limit int) decimal.Decimal {
	if len(orders) == 0 {
		return decimal.Zero
	}
	sorted := append([]domain.Order(nil), orders...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].UpdatedAt.After(sorted[j].UpdatedAt)
	})
	sum := decimal.Zero
	weight := decimal.Zero
	count := 0
	for _, order := range sorted {
		slippage, ok := orderAdverseSlippageBps(order)
		if !ok {
			continue
		}
		lots := decimal.NewFromInt(order.FilledLots)
		sum = sum.Add(slippage.Mul(lots))
		weight = weight.Add(lots)
		count++
		if limit > 0 && count == limit {
			break
		}
	}
	if weight.IsZero() {
		return decimal.Zero
	}
	return sum.Div(weight)
}

func orderAdverseSlippageBps(order domain.Order) (decimal.Decimal, bool) {
	if order.FilledLots <= 0 || !order.AvgFillPrice.IsPositive() {
		return decimal.Zero, false
	}
	reference := orderReferencePrice(order)
	if !reference.IsPositive() {
		return decimal.Zero, false
	}
	var adverse decimal.Decimal
	switch order.Side {
	case domain.SideBuy:
		adverse = order.AvgFillPrice.Sub(reference)
	case domain.SideSell:
		adverse = reference.Sub(order.AvgFillPrice)
	default:
		return decimal.Zero, false
	}
	if adverse.IsNegative() {
		adverse = decimal.Zero
	}
	return adverse.Div(reference).Mul(decimal.NewFromInt(10_000)), true
}

func orderReferencePrice(order domain.Order) decimal.Decimal {
	if mid := rawFillMidPrice(order.RawStateJSON); mid.IsPositive() {
		return mid
	}
	if mid := rawMidPrice(order.RawStateJSON); mid.IsPositive() {
		return mid
	}
	return order.LimitPrice
}

func rawFillMidPrice(raw string) decimal.Decimal {
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return decimal.Zero
	}
	if mid := fillMidFromContainer(root); mid.IsPositive() {
		return mid
	}
	if local, ok := root["local"].(map[string]any); ok {
		return fillMidFromContainer(local)
	}
	return decimal.Zero
}

func fillMidFromContainer(container map[string]any) decimal.Decimal {
	quotes, ok := container["fill_quotes"].([]any)
	if !ok || len(quotes) == 0 {
		return decimal.Zero
	}
	for i := len(quotes) - 1; i >= 0; i-- {
		quote, ok := quotes[i].(map[string]any)
		if !ok {
			continue
		}
		mid, ok := decimalFromAny(quote["mid"])
		if ok && mid.IsPositive() {
			return mid
		}
	}
	return decimal.Zero
}

func rawMidPrice(raw string) decimal.Decimal {
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return decimal.Zero
	}
	if mid := midFromContainer(root); mid.IsPositive() {
		return mid
	}
	if local, ok := root["local"].(map[string]any); ok {
		return midFromContainer(local)
	}
	return decimal.Zero
}

func midFromContainer(container map[string]any) decimal.Decimal {
	quote, ok := container["local_quote"].(map[string]any)
	if !ok {
		return decimal.Zero
	}
	mid, ok := decimalFromAny(quote["mid"])
	if !ok {
		return decimal.Zero
	}
	return mid
}

func groupedReasons(signals []domain.Signal) map[string]int {
	out := make(map[string]int)
	for _, sig := range signals {
		if sig.Decision == domain.DecisionEnter || sig.RejectReason == "" {
			continue
		}
		out[sig.RejectReason]++
	}
	return out
}

func sortedKeys(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func expectedEdgeByInstrument(signals []domain.Signal) map[string]decimal.Decimal {
	out := make(map[string]decimal.Decimal)
	for _, sig := range signals {
		if sig.Decision == domain.DecisionEnter {
			out[sig.InstrumentUID] = sig.NetEdgeBps
		}
	}
	return out
}

func averageContextDecimal(signals []domain.Signal, key string) decimal.Decimal {
	sum := decimal.Zero
	count := int64(0)
	for _, sig := range signals {
		var context map[string]any
		if err := json.Unmarshal([]byte(sig.ContextJSON), &context); err != nil {
			continue
		}
		value, ok := decimalFromAny(context[key])
		if !ok {
			continue
		}
		sum = sum.Add(value)
		count++
	}
	if count == 0 {
		return decimal.Zero
	}
	return sum.Div(decimal.NewFromInt(count))
}

func decimalFromAny(value any) (decimal.Decimal, bool) {
	switch typed := value.(type) {
	case string:
		parsed, err := decimal.NewFromString(typed)
		return parsed, err == nil
	case float64:
		return decimal.NewFromFloat(typed), true
	default:
		return decimal.Zero, false
	}
}

func writeExecutionErrors(b *strings.Builder, orders []domain.Order) {
	wroteHeader := false
	for _, order := range orders {
		if !isExecutionError(order.Status) {
			continue
		}
		if !wroteHeader {
			fmt.Fprintf(b, "Ошибки исполнения:\n")
			wroteHeader = true
		}
		fmt.Fprintf(b, "- %s %s status=%s filled=%d/%d\n", order.InstrumentUID, order.Side, order.Status, order.FilledLots, order.QuantityLots)
	}
}

func isExecutionError(status domain.OrderStatus) bool {
	switch status {
	case domain.OrderStatusFailed, domain.OrderStatusRejected, domain.OrderStatusExpired:
		return true
	default:
		return false
	}
}

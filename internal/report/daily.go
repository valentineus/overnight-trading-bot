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
	fmt.Fprintf(&b, "Среднее проскальзывание: %s bps\n", input.AverageSlipBps.StringFixed(2))
	writeExecutionErrors(&b, input.Orders)
	fmt.Fprintf(&b, "Risk: %s", input.RiskStatus)
	return b.String()
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

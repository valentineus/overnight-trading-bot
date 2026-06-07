package report

import (
	"fmt"
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
	gross := decimal.Zero
	net := decimal.Zero
	commission := decimal.Zero
	for _, pos := range input.Positions {
		gross = gross.Add(pos.GrossPnL)
		net = net.Add(pos.NetPnL)
		commission = commission.Add(pos.CommissionTotal)
	}
	fmt.Fprintf(&b, "Gross PnL: %s\n", gross.StringFixed(2))
	fmt.Fprintf(&b, "Net PnL: %s\n", net.StringFixed(2))
	fmt.Fprintf(&b, "Комиссии: %s\n", commission.StringFixed(2))
	fmt.Fprintf(&b, "Средний spread: %s bps\n", input.AverageSpreadBps.StringFixed(2))
	fmt.Fprintf(&b, "Среднее проскальзывание: %s bps\n", input.AverageSlipBps.StringFixed(2))
	fmt.Fprintf(&b, "Risk: %s", input.RiskStatus)
	return b.String()
}

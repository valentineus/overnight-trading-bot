package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/money"
)

var nonIDChar = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func LimitBuyPrice(bestBid, bestAsk, tick decimal.Decimal, improveTicks int) (decimal.Decimal, error) {
	if improveTicks < 0 {
		improveTicks = 0
	}
	if !tick.IsPositive() {
		return decimal.Zero, money.ErrInvalidTick
	}
	candidate := bestBid.Add(tick.Mul(decimal.NewFromInt(int64(improveTicks))))
	upper := bestAsk.Sub(tick)
	if candidate.LessThanOrEqual(upper) {
		return money.RoundToTick(candidate, tick, money.RoundFloor)
	}
	return money.RoundToTick(bestBid, tick, money.RoundFloor)
}

func LimitSellPrice(bestBid, bestAsk, tick decimal.Decimal, improveTicks int) (decimal.Decimal, error) {
	if improveTicks < 0 {
		improveTicks = 0
	}
	if !tick.IsPositive() {
		return decimal.Zero, money.ErrInvalidTick
	}
	candidate := bestAsk.Sub(tick.Mul(decimal.NewFromInt(int64(improveTicks))))
	lower := bestBid.Add(tick)
	if candidate.GreaterThanOrEqual(lower) {
		return money.RoundToTick(candidate, tick, money.RoundCeil)
	}
	return money.RoundToTick(bestAsk, tick, money.RoundCeil)
}

func ClientOrderID(tradeDate time.Time, instrumentUID string, side domain.Side, attempt int) string {
	base := fmt.Sprintf("%s|%s|%s|%d", tradeDate.Format("20060102"), instrumentUID, side, attempt)
	sum := sha256.Sum256([]byte(base))
	suffix := hex.EncodeToString(sum[:])[:8]
	cleanUID := nonIDChar.ReplaceAllString(instrumentUID, "_")
	if len(cleanUID) > 24 {
		cleanUID = cleanUID[:24]
	}
	return strings.ToLower(fmt.Sprintf("otb-%s-%s-%s-%02d-%s", tradeDate.Format("20060102"), cleanUID, side, attempt, suffix))
}

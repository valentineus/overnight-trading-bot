package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/money"
)

const maxClientOrderIDLen = 36

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
	suffix := hex.EncodeToString(sum[:])
	sideToken := "b"
	if side == domain.SideSell {
		sideToken = "s"
	}
	prefix := fmt.Sprintf("otb-%s-%s-%s-", tradeDate.Format("20060102"), sideToken, attemptToken(attempt))
	return strings.ToLower(prefix + suffix[:maxClientOrderIDLen-len(prefix)])
}

func attemptToken(attempt int) string {
	if attempt < 0 {
		attempt = 0
	}
	token := strings.ToLower(strconv.FormatInt(int64(attempt), 36))
	if len(token) > 2 {
		token = token[len(token)-2:]
	}
	for len(token) < 2 {
		token = "0" + token
	}
	return token
}

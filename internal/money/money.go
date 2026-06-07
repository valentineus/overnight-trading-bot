package money

import (
	"errors"

	"github.com/shopspring/decimal"

	pb "github.com/russianinvestments/invest-api-go-sdk/proto"
)

var (
	ErrInvalidTick = errors.New("tick must be positive")
	ErrInvalidBase = errors.New("base must be positive")
)

type RoundMode int

const (
	RoundNearest RoundMode = iota
	RoundFloor
	RoundCeil
)

func QuotationToDecimal(q *pb.Quotation) decimal.Decimal {
	if q == nil {
		return decimal.Zero
	}
	return decimal.NewFromInt(q.GetUnits()).Add(decimal.New(int64(q.GetNano()), -9))
}

func DecimalToQuotation(d decimal.Decimal) *pb.Quotation {
	units := d.Truncate(0)
	nano := d.Sub(units).Mul(decimal.NewFromInt(1_000_000_000)).Round(0)
	if nano.Equal(decimal.NewFromInt(1_000_000_000)) {
		units = units.Add(decimal.NewFromInt(1))
		nano = decimal.Zero
	}
	if nano.Equal(decimal.NewFromInt(-1_000_000_000)) {
		units = units.Sub(decimal.NewFromInt(1))
		nano = decimal.Zero
	}
	nanoPart := nano.IntPart()
	if nanoPart < -999_999_999 || nanoPart > 999_999_999 {
		panic("decimal quotation nano is out of protobuf range")
	}
	return &pb.Quotation{
		Units: units.IntPart(),
		Nano:  int32(nanoPart), // #nosec G115 -- nanoPart is bounded above.
	}
}

func MoneyValueToDecimal(v *pb.MoneyValue) decimal.Decimal {
	if v == nil {
		return decimal.Zero
	}
	return decimal.NewFromInt(v.GetUnits()).Add(decimal.New(int64(v.GetNano()), -9))
}

func Bps(part, base decimal.Decimal) (decimal.Decimal, error) {
	if !base.IsPositive() {
		return decimal.Zero, ErrInvalidBase
	}
	return part.Div(base).Mul(decimal.NewFromInt(10_000)), nil
}

func FromBps(bps decimal.Decimal) decimal.Decimal {
	return bps.Div(decimal.NewFromInt(10_000))
}

func RoundToTick(price, tick decimal.Decimal, mode RoundMode) (decimal.Decimal, error) {
	if !tick.IsPositive() {
		return decimal.Zero, ErrInvalidTick
	}
	steps := price.Div(tick)
	switch mode {
	case RoundFloor:
		steps = steps.Floor()
	case RoundCeil:
		steps = steps.Ceil()
	default:
		steps = steps.Round(0)
	}
	return steps.Mul(tick), nil
}

func Min(values ...decimal.Decimal) decimal.Decimal {
	if len(values) == 0 {
		return decimal.Zero
	}
	min := values[0]
	for _, value := range values[1:] {
		if value.LessThan(min) {
			min = value
		}
	}
	return min
}

func Max(values ...decimal.Decimal) decimal.Decimal {
	if len(values) == 0 {
		return decimal.Zero
	}
	max := values[0]
	for _, value := range values[1:] {
		if value.GreaterThan(max) {
			max = value
		}
	}
	return max
}

func Abs(value decimal.Decimal) decimal.Decimal {
	if value.IsNegative() {
		return value.Neg()
	}
	return value
}

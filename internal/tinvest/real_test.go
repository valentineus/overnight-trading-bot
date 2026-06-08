package tinvest

import (
	"strings"
	"testing"

	pb "github.com/russianinvestments/invest-api-go-sdk/proto"
	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
)

func TestOrderFromPostResponseZeroFillHasZeroAvgPrice(t *testing.T) {
	order := orderFromPostResponse(&pb.PostOrderResponse{
		OrderId:               "broker",
		ExecutionReportStatus: pb.OrderExecutionReportStatus_EXECUTION_REPORT_STATUS_NEW,
		LotsRequested:         1,
		LotsExecuted:          0,
		ExecutedOrderPrice:    &pb.MoneyValue{Currency: "rub", Units: 100},
		InstrumentUid:         "uid",
	}, "account", "client", domain.SideBuy, decimal.NewFromInt(100))
	if !order.AvgFillPrice.IsZero() {
		t.Fatalf("avg fill price=%s, want zero for unfilled order", order.AvgFillPrice)
	}
}

func TestMarshalProtoRedactsAccountID(t *testing.T) {
	raw := marshalProto(&pb.OrderTrades{
		OrderId:       "order",
		AccountId:     "plain-account-id",
		InstrumentUid: "uid",
	})
	if strings.Contains(raw, "plain-account-id") || strings.Contains(raw, "accountId") || strings.Contains(raw, "account_id") {
		t.Fatalf("raw proto leaked account id: %s", raw)
	}
	if !strings.Contains(raw, "order") {
		t.Fatalf("sanitizer removed non-sensitive data: %s", raw)
	}
}

func TestPortfolioFromResponseConvertsUnitsToLots(t *testing.T) {
	portfolio, err := portfolioFromResponse(&pb.PortfolioResponse{
		Positions: []*pb.PortfolioPosition{
			{
				InstrumentUid: "uid",
				Quantity:      &pb.Quotation{Units: 20},
				CurrentPrice:  &pb.MoneyValue{Currency: "rub", Units: 10},
			},
		},
	}, func(instrumentUID string) int64 {
		if instrumentUID == "uid" {
			return 10
		}
		return 0
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := portfolio.Holdings[0].QuantityLots; got != 2 {
		t.Fatalf("quantity lots=%d, want 2", got)
	}
	if !portfolio.Holdings[0].MarketValue.Equal(decimal.NewFromInt(200)) {
		t.Fatalf("market value=%s, want 200", portfolio.Holdings[0].MarketValue)
	}
}

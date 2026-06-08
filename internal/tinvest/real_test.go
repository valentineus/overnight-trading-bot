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
	}, func(instrumentUID string) (int64, error) {
		if instrumentUID == "uid" {
			return 10, nil
		}
		return 0, nil
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

func TestPortfolioFromResponseRejectsUnknownLotWhenQuantityLotsMissing(t *testing.T) {
	_, err := portfolioFromResponse(&pb.PortfolioResponse{
		Positions: []*pb.PortfolioPosition{
			{
				InstrumentUid: "uid",
				Quantity:      &pb.Quotation{Units: 20},
				CurrentPrice:  &pb.MoneyValue{Currency: "rub", Units: 10},
			},
		},
	}, func(string) (int64, error) {
		return 0, nil
	})
	if err == nil {
		t.Fatal("expected unknown lot error")
	}
}

func TestPortfolioFromResponseIgnoresCurrencyPositions(t *testing.T) {
	portfolio, err := portfolioFromResponse(&pb.PortfolioResponse{
		Positions: []*pb.PortfolioPosition{
			{
				InstrumentUid:        "rub",
				InstrumentType:       "currency",
				Quantity:             &pb.Quotation{Units: 1000},
				CurrentPrice:         &pb.MoneyValue{Currency: "rub", Units: 1},
				QuantityLots:         &pb.Quotation{Units: 1000},
				AveragePositionPrice: &pb.MoneyValue{Currency: "rub", Units: 1},
			},
		},
	}, func(string) (int64, error) {
		return 0, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(portfolio.Holdings) != 0 {
		t.Fatalf("currency position should be excluded from holdings: %+v", portfolio.Holdings)
	}
}

func TestOperationsFromResponseAttributesCommissionChildUID(t *testing.T) {
	ops := operationsFromResponse(&pb.OperationsResponse{
		Operations: []*pb.Operation{{
			Id:            "fee",
			OperationType: pb.OperationType_OPERATION_TYPE_BROKER_FEE,
			Payment:       &pb.MoneyValue{Currency: "rub", Units: -1},
			ChildOperations: []*pb.ChildOperationItem{{
				InstrumentUid: "uid",
				Payment:       &pb.MoneyValue{Currency: "rub", Units: -1},
			}},
		}},
	})
	if len(ops) != 1 {
		t.Fatalf("operations=%d, want 1", len(ops))
	}
	if ops[0].InstrumentUID != "uid" {
		t.Fatalf("instrument uid=%q, want uid", ops[0].InstrumentUID)
	}
	if !ops[0].Commission.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("commission=%s, want 1", ops[0].Commission)
	}
}

func TestOperationsFromCursorResponseUsesExplicitCommission(t *testing.T) {
	ops := operationsFromCursorResponse(&pb.GetOperationsByCursorResponse{
		Items: []*pb.OperationItem{{
			Id:            "trade",
			Type:          pb.OperationType_OPERATION_TYPE_BUY,
			InstrumentUid: "uid",
			Payment:       &pb.MoneyValue{Currency: "rub", Units: -100},
			Commission:    &pb.MoneyValue{Currency: "rub", Units: -1},
		}},
	})
	if len(ops) != 1 {
		t.Fatalf("operations=%d, want 1", len(ops))
	}
	if !ops[0].Commission.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("commission=%s, want 1", ops[0].Commission)
	}
}

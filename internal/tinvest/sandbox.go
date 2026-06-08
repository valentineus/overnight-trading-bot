package tinvest

import (
	"context"
	"time"

	pb "github.com/russianinvestments/invest-api-go-sdk/proto"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/money"
)

const sandboxEndpoint = "sandbox-invest-public-api.tinkoff.ru:443"

type SandboxGateway struct {
	*RealGateway
	sandboxPB pb.SandboxServiceClient
}

func NewSandboxGateway(ctx context.Context, opts Options) (*SandboxGateway, error) {
	opts.Endpoint = sandboxEndpoint
	realGateway, err := NewRealGateway(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &SandboxGateway{
		RealGateway: realGateway,
		sandboxPB:   pb.NewSandboxServiceClient(realGateway.client.Conn),
	}, nil
}

func (g *SandboxGateway) PostLimitOrder(ctx context.Context, accountID, instrumentUID string, side domain.Side, lots int64, price decimal.Decimal, clientOrderID string) (domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return domain.Order{}, err
	}
	direction := pb.OrderDirection_ORDER_DIRECTION_BUY
	if side == domain.SideSell {
		direction = pb.OrderDirection_ORDER_DIRECTION_SELL
	}
	quotation, err := money.DecimalToQuotation(price)
	if err != nil {
		return domain.Order{}, err
	}
	resp, err := requestWithTimeout(ctx, g.requestTimeout, func(callCtx context.Context) (*pb.PostOrderResponse, error) {
		return retryValue(callCtx, g.retryAttempts, g.retryBackoff, func() (*pb.PostOrderResponse, error) {
			return g.sandboxPB.PostSandboxOrder(callCtx, &pb.PostOrderRequest{
				InstrumentId: instrumentUID,
				Quantity:     lots,
				Price:        quotation,
				Direction:    direction,
				AccountId:    accountID,
				OrderType:    pb.OrderType_ORDER_TYPE_LIMIT,
				OrderId:      clientOrderID,
				TimeInForce:  pb.TimeInForceType_TIME_IN_FORCE_DAY,
				PriceType:    pb.PriceType_PRICE_TYPE_CURRENCY,
			})
		})
	})
	if err != nil {
		return domain.Order{}, err
	}
	return orderFromPostResponse(resp, accountID, clientOrderID, side, price), nil
}

func (g *SandboxGateway) CancelOrder(ctx context.Context, accountID, orderID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := requestWithTimeout(ctx, g.requestTimeout, func(callCtx context.Context) (struct{}, error) {
		return struct{}{}, withRetry(callCtx, g.retryAttempts, g.retryBackoff, func() error {
			_, err := g.sandboxPB.CancelSandboxOrder(callCtx, &pb.CancelOrderRequest{
				AccountId: accountID,
				OrderId:   orderID,
			})
			return err
		})
	})
	return err
}

func (g *SandboxGateway) GetOrderState(ctx context.Context, accountID, orderID string) (domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return domain.Order{}, err
	}
	resp, err := requestWithTimeout(ctx, g.requestTimeout, func(callCtx context.Context) (*pb.OrderState, error) {
		return retryValue(callCtx, g.retryAttempts, g.retryBackoff, func() (*pb.OrderState, error) {
			return g.sandboxPB.GetSandboxOrderState(callCtx, &pb.GetOrderStateRequest{
				AccountId: accountID,
				OrderId:   orderID,
				PriceType: pb.PriceType_PRICE_TYPE_CURRENCY,
			})
		})
	})
	if err != nil {
		return domain.Order{}, err
	}
	return orderFromState(resp, accountID), nil
}

func (g *SandboxGateway) GetActiveOrders(ctx context.Context, accountID string) ([]domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp, err := requestWithTimeout(ctx, g.requestTimeout, func(callCtx context.Context) (*pb.GetOrdersResponse, error) {
		return retryValue(callCtx, g.retryAttempts, g.retryBackoff, func() (*pb.GetOrdersResponse, error) {
			return g.sandboxPB.GetSandboxOrders(callCtx, &pb.GetOrdersRequest{AccountId: accountID})
		})
	})
	if err != nil {
		return nil, err
	}
	states := resp.GetOrders()
	out := make([]domain.Order, 0, len(states))
	for _, state := range states {
		out = append(out, orderFromState(state, accountID))
	}
	return out, nil
}

func (g *SandboxGateway) GetPortfolio(ctx context.Context, accountID string) (domain.Portfolio, error) {
	if err := ctx.Err(); err != nil {
		return domain.Portfolio{}, err
	}
	resp, err := requestWithTimeout(ctx, g.requestTimeout, func(callCtx context.Context) (*pb.PortfolioResponse, error) {
		return retryValue(callCtx, g.retryAttempts, g.retryBackoff, func() (*pb.PortfolioResponse, error) {
			currency := pb.PortfolioRequest_RUB
			return g.sandboxPB.GetSandboxPortfolio(callCtx, &pb.PortfolioRequest{
				AccountId: accountID,
				Currency:  &currency,
			})
		})
	})
	if err != nil {
		return domain.Portfolio{}, err
	}
	return portfolioFromResponse(resp, g.lotForInstrument)
}

func (g *SandboxGateway) GetOperations(ctx context.Context, accountID string, from, to time.Time) ([]domain.Operation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp, err := requestWithTimeout(ctx, g.requestTimeout, func(callCtx context.Context) (*pb.OperationsResponse, error) {
		return retryValue(callCtx, g.retryAttempts, g.retryBackoff, func() (*pb.OperationsResponse, error) {
			return g.sandboxPB.GetSandboxOperations(callCtx, &pb.OperationsRequest{
				AccountId: accountID,
				From:      timestamppb.New(from),
				To:        timestamppb.New(to),
			})
		})
	})
	if err != nil {
		return nil, err
	}
	return operationsFromResponse(resp), nil
}

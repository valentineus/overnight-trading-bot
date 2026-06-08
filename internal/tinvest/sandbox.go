package tinvest

import (
	"context"
	"time"

	"github.com/russianinvestments/invest-api-go-sdk/investgo"
	pb "github.com/russianinvestments/invest-api-go-sdk/proto"
	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/money"
)

const sandboxEndpoint = "sandbox-invest-public-api.tinkoff.ru:443"

type SandboxGateway struct {
	*RealGateway
	sandbox *investgo.SandboxServiceClient
}

func NewSandboxGateway(ctx context.Context, opts Options) (*SandboxGateway, error) {
	opts.Endpoint = sandboxEndpoint
	realGateway, err := NewRealGateway(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &SandboxGateway{
		RealGateway: realGateway,
		sandbox:     realGateway.client.NewSandboxServiceClient(),
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
	resp, err := requestWithTimeout(ctx, g.requestTimeout, func() (*investgo.PostOrderResponse, error) {
		return retryValue(ctx, g.retryAttempts, g.retryBackoff, func() (*investgo.PostOrderResponse, error) {
			return g.sandbox.PostSandboxOrder(&investgo.PostOrderRequest{
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
	return orderFromPostResponse(resp.PostOrderResponse, accountID, clientOrderID, side, price), nil
}

func (g *SandboxGateway) CancelOrder(ctx context.Context, accountID, orderID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := requestWithTimeout(ctx, g.requestTimeout, func() (struct{}, error) {
		return struct{}{}, withRetry(ctx, g.retryAttempts, g.retryBackoff, func() error {
			_, err := g.sandbox.CancelSandboxOrder(accountID, orderID)
			return err
		})
	})
	return err
}

func (g *SandboxGateway) GetOrderState(ctx context.Context, accountID, orderID string) (domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return domain.Order{}, err
	}
	resp, err := requestWithTimeout(ctx, g.requestTimeout, func() (*investgo.GetOrderStateResponse, error) {
		return retryValue(ctx, g.retryAttempts, g.retryBackoff, func() (*investgo.GetOrderStateResponse, error) {
			return g.sandbox.GetSandboxOrderState(accountID, orderID)
		})
	})
	if err != nil {
		return domain.Order{}, err
	}
	return orderFromState(resp.OrderState, accountID), nil
}

func (g *SandboxGateway) GetActiveOrders(ctx context.Context, accountID string) ([]domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp, err := requestWithTimeout(ctx, g.requestTimeout, func() (*investgo.GetOrdersResponse, error) {
		return retryValue(ctx, g.retryAttempts, g.retryBackoff, func() (*investgo.GetOrdersResponse, error) {
			return g.sandbox.GetSandboxOrders(accountID)
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
	resp, err := requestWithTimeout(ctx, g.requestTimeout, func() (*investgo.PortfolioResponse, error) {
		return retryValue(ctx, g.retryAttempts, g.retryBackoff, func() (*investgo.PortfolioResponse, error) {
			return g.sandbox.GetSandboxPortfolio(accountID, pb.PortfolioRequest_RUB)
		})
	})
	if err != nil {
		return domain.Portfolio{}, err
	}
	return portfolioFromResponse(resp.PortfolioResponse)
}

func (g *SandboxGateway) GetOperations(ctx context.Context, accountID string, from, to time.Time) ([]domain.Operation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp, err := requestWithTimeout(ctx, g.requestTimeout, func() (*investgo.OperationsResponse, error) {
		return retryValue(ctx, g.retryAttempts, g.retryBackoff, func() (*investgo.OperationsResponse, error) {
			return g.sandbox.GetSandboxOperations(&investgo.GetOperationsRequest{
				AccountId: accountID,
				From:      from,
				To:        to,
			})
		})
	})
	if err != nil {
		return nil, err
	}
	return operationsFromResponse(resp.OperationsResponse), nil
}

package tinvest

import "context"

const sandboxEndpoint = "sandbox-invest-public-api.tinkoff.ru:443"

func NewSandboxGateway(ctx context.Context, opts Options) (*RealGateway, error) {
	opts.Endpoint = sandboxEndpoint
	return NewRealGateway(ctx, opts)
}

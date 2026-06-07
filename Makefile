export GOCACHE := $(CURDIR)/.cache/go-build
export GOMODCACHE := $(CURDIR)/.cache/go-mod
export GOLANGCI_LINT_CACHE := $(CURDIR)/.cache/golangci-lint
export GOTELEMETRY := off
export TMPDIR := $(CURDIR)/.tmp

GO := go

.PHONY: cache fmt vet lint test race integration sandbox tidy run migrate build backtest

cache:
	mkdir -p $(GOCACHE) $(GOMODCACHE) $(GOLANGCI_LINT_CACHE) $(TMPDIR) bin

fmt: cache
	$(GO) fmt ./...

vet: cache
	$(GO) vet ./...

lint: cache
	golangci-lint run ./...

test: cache
	$(GO) test ./...

race: cache
	$(GO) test -race ./...

integration: cache
	GOMODCACHE=$(CURDIR)/.cache/go-mod-integration-v42 GOCACHE=$(CURDIR)/.cache/go-build-integration-v42 $(GO) test -tags=integration ./...

sandbox: cache
	$(GO) test -tags=sandbox ./...

tidy: cache
	$(GO) mod tidy

run: cache
	$(GO) run ./cmd/bot

migrate: cache
	$(GO) run ./cmd/migrate -direction=up

build: cache
	$(GO) build -trimpath -o bin/bot ./cmd/bot
	$(GO) build -trimpath -o bin/migrate ./cmd/migrate
	$(GO) build -trimpath -o bin/backtest ./cmd/backtest

backtest: cache
	$(GO) run ./cmd/backtest -candles "$${BT_CANDLES:?set BT_CANDLES}"

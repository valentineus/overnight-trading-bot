.PHONY: fmt test run tidy

fmt:
	go fmt ./...

test:
	go test ./...

run:
	go run ./cmd/bot

tidy:
	go mod tidy

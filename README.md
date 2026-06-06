# Overnight Trading Bot

Go-проект для overnight-бота по фондам T-Капитала через T-Invest API.

## Quick Start

```sh
cp config.example.yaml config.yaml
go test ./...
go run ./cmd/bot -config config.yaml
```

## Development

```sh
make fmt
make test
make run
```

`config.yaml` не коммитится: в нем будут локальные настройки, account id и ссылки на переменные окружения с токенами.

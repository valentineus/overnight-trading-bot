# Overnight Trading Bot

[English](README.md) / [Русский](README.ru.md)

A Go research bot for studying a `close -> next open` overnight strategy on T-Capital funds through the T-Invest API.

The project is intended for statistical research, backtesting, paper trading, sandbox checks, and tightly controlled live-readonly/live-trade experiments. It is not designed for market manipulation, price impact, or evading legal requirements. Orders must have a genuine execution intent, use limit-only execution, and pass liquidity, spread, commission, reconciliation, and risk controls.

One research thread is the overnight/intraday returns anomaly discussed by Bruce Knuteson in [“Nothing to See Here: How to Say It When You Need to”](https://ssrn.com/abstract=4619084) (`ssrn-4619084`).

License: [MIT](LICENSE).

## Quick Start

```sh
cp .env.example .env
make test
APP_MODE=backtest go run ./cmd/bot
```

Daemon modes (`paper`, `sandbox`, `live_readonly`, `live_trade`) require a MariaDB/MySQL `DB_DSN`. `live_trade` also requires `LIVE_TRADE_ACK=I_ACCEPT_RISK` and the live pre-flight checks listed below.

## Modes

| Mode | Purpose |
| --- | --- |
| `backtest` | Offline research mode. Does not require a database or T-Invest credentials when run through `cmd/bot`. |
| `paper` | Simulated orders. Without `TINVEST_TOKEN`, uses a fake gateway; with a token, uses real market data/status and simulated execution. |
| `sandbox` | T-Invest sandbox API. Requires token and account id. |
| `live_readonly` | Live API access without broker order placement. Used for observation and reconciliation. |
| `live_trade` | Real limit-order trading. Guarded by explicit risk acknowledgement and pre-flight requirements. |

## Configuration

Configuration is read from environment variables, usually through `.env`. If a value cannot be parsed, startup fails with `load ENV config`.

Common formats:

- Times use `HH:MM:SS` and are interpreted in `Europe/Moscow`.
- Percentages are decimal fractions: `0.10` means 10%, `0.005` means 0.5%.
- `bps` means basis points: `10` means 0.10%.
- Boolean values are `true` or `false`.
- Defaults below match `.env.example` and the code defaults.

### APP

| Variable | Default | Description |
| --- | --- | --- |
| `APP_MODE` | `paper` | One of `backtest`, `paper`, `sandbox`, `live_readonly`, `live_trade`; required by the code. |
| `APP_TIMEZONE` | `Europe/Moscow` | Trading schedule timezone; validation currently allows only `Europe/Moscow`. |
| `APP_LOG_LEVEL` | `info` | JSON log level: `debug`, `info`, `warn`, `warning`, `error`. |
| `APP_HEALTHCHECK_ADDR` | `:3300` | HTTP address for `/health` and `/ready`. |
| `APP_SHUTDOWN_TIMEOUT_SEC` | `30` | Graceful shutdown timeout in seconds. |

### TINVEST

| Variable | Default | Description |
| --- | --- | --- |
| `TINVEST_TOKEN` | empty | API token. Required for `sandbox`, `live_readonly`, and `live_trade`; optional in `paper`. |
| `TINVEST_ACCOUNT_ID` | empty | Broker account id. Required for API-backed modes. |
| `TINVEST_ENDPOINT` | `invest-public-api.tinkoff.ru:443` | T-Invest gRPC endpoint; sandbox mode overrides this where needed. |
| `TINVEST_APP_NAME` | `overnight-trading-bot` | Application/client name passed to the SDK. |
| `TINVEST_REQUEST_TIMEOUT_SEC` | `10` | API request timeout, including retry sequences. |
| `TINVEST_RETRY_COUNT` | `3` | Number of T-Invest SDK attempts. |
| `TINVEST_RETRY_BACKOFF_SEC` | `2` | Initial exponential backoff in seconds. |
| `TINVEST_USE_SANDBOX` | `false` | Compatibility guard; valid only with `APP_MODE=sandbox`. |
| `TINVEST_TRADING_CALENDAR_EXCHANGE` | `MOEX` | Exchange calendar used to load trading days. |

### DB

| Variable | Default | Description |
| --- | --- | --- |
| `DB_DSN` | example DSN | MySQL/MariaDB DSN. Required outside `backtest`. Stores instruments, candles, signals, orders, positions, risk events, and reports. |
| `DB_MAX_OPEN_CONNS` | `20` | Maximum open database connections. |
| `DB_MAX_IDLE_CONNS` | `5` | Idle connection pool size. |
| `DB_CONN_MAX_LIFETIME_MIN` | `30` | Connection lifetime in minutes. |
| `DB_MIGRATIONS_AUTO_APPLY` | `true` | Apply migrations automatically at daemon startup. |

### TELEGRAM

| Variable | Default | Description |
| --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | empty | Telegram bot token. Empty token or chat id disables notifications. |
| `TELEGRAM_CHAT_ID` | `0` | Telegram chat id; `0` disables Telegram. |
| `TELEGRAM_NOTIFY_INFO` | `true` | Send informational messages. |
| `TELEGRAM_NOTIFY_WARN` | `true` | Send warnings. |
| `TELEGRAM_NOTIFY_ALERT` | `true` | Send critical alerts. |
| `TELEGRAM_NOTIFY_REPORT` | `true` | Send daily reports. |

### STRATEGY

| Variable | Default | Description |
| --- | --- | --- |
| `STRATEGY_ROLLING_SHORT` | `60` | Short rolling window for overnight-return statistics. |
| `STRATEGY_ROLLING_LONG` | `252` | Long rolling window for persistent edge checks and backfill depth. |
| `STRATEGY_EWMA_LAMBDA` | `0.08` | EWMA weight for fresh overnight observations. |
| `STRATEGY_ALLOCATION_METHOD` | `equal_weight` | Capital allocation method; only `equal_weight` is currently supported. |
| `STRATEGY_MIN_TSTAT_60` | `1.25` | Minimum short-window t-statistic. |
| `STRATEGY_MIN_WIN_RATE_60` | `0.55` | Minimum positive overnight observation share. |
| `STRATEGY_MIN_NET_EDGE_BPS` | `10` | Minimum expected net edge after costs. |
| `STRATEGY_RISK_BUFFER_BPS` | `5` | Extra cost buffer subtracted from expected edge. |
| `STRATEGY_EXPECTED_ENTRY_SLIPPAGE_BPS` | `8` | Expected entry slippage used in signal costs and app-level backtest config. |
| `STRATEGY_EXPECTED_EXIT_SLIPPAGE_BPS` | `8` | Expected exit slippage used in signal costs and app-level backtest config. |
| `STRATEGY_INTERVAL_VOLUME_LOOKBACK_DAYS` | `20` | Lookback for entry/exit interval volume used by participation sizing. |
| `STRATEGY_MAX_POSITIONS` | `5` | Maximum selected/open positions at signal level. |

### EXEC

| Variable | Default | Description |
| --- | --- | --- |
| `EXEC_ENTRY_SIGNAL_TIME` | `18:10:00` | Time to prepare data and generate entry signals. |
| `EXEC_ENTRY_WINDOW_START` | `18:20:00` | Start of the entry order window. |
| `EXEC_ENTRY_WINDOW_END` | `18:38:30` | End of active entry order placement. |
| `EXEC_NO_NEW_ENTRY_AFTER` | `18:38:30` | No new entry orders after this time. |
| `EXEC_EXIT_WATCH_START` | `09:50:00` | Morning watch start before exit. |
| `EXEC_EXIT_NOT_BEFORE` | `10:03:00` | Lower bound for exit timing validation. |
| `EXEC_EXIT_WINDOW_START` | `10:05:00` | Start of exit order placement. |
| `EXEC_EXIT_WINDOW_END` | `10:25:00` | End of new exit order placement. |
| `EXEC_HARD_EXIT_DEADLINE` | `10:45:00` | Final exit deadline before reconciliation/reporting and HALT handling. |
| `EXEC_MARKET_CLOSE` | `18:50:00` | Market close reference for pre-trade time-to-close checks. |
| `EXEC_MIN_TIME_TO_CLOSE_SEC` | `90` | Minimum remaining time before close required for pre-trade checks. |
| `EXEC_ALLOW_MARKET_ORDERS` | `false` | Must remain `false`; the strategy is limit-only. |
| `EXEC_MAX_ENTRY_ORDER_ATTEMPTS` | `3` | Maximum entry repost attempts. |
| `EXEC_MAX_EXIT_ORDER_ATTEMPTS` | `3` | Maximum exit repost attempts. |
| `EXEC_PASSIVE_IMPROVE_TICKS` | `1` | Tick improvement from best bid/ask when pricing passive limits. |
| `EXEC_QUOTE_DEPTH` | `20` | Order-book depth, validated in `1..50`. |
| `EXEC_MAX_QUOTE_AGE_SEC` | `3` | Maximum acceptable quote age. |
| `EXEC_ORDER_POLL_INTERVAL_MS` | `500` | Order-status polling interval. |

### RISK

| Variable | Default | Description |
| --- | --- | --- |
| `RISK_USE_MARGIN` | `false` | Must remain `false`; margin is disabled. |
| `RISK_ALLOW_SHORT` | `false` | Must remain `false`; short positions are disabled. |
| `RISK_MAX_TOTAL_EXPOSURE_PCT` | `0.50` | Total exposure cap as a fraction of equity. |
| `RISK_MAX_POSITION_PCT` | `0.10` | Per-position exposure cap as a fraction of equity. |
| `RISK_MAX_DAILY_LOSS_PCT` | `0.01` | Daily loss stop. |
| `RISK_MAX_WEEKLY_LOSS_PCT` | `0.03` | Weekly loss stop. |
| `RISK_MAX_MONTHLY_DRAWDOWN_PCT` | `0.07` | Monthly drawdown stop. |
| `RISK_MAX_OPEN_POSITIONS` | `5` | Risk-level open-position cap. |
| `RISK_MAX_AVG_SLIPPAGE_BPS_10_TRADES` | `15` | Blocks new orders after excessive average slippage over 10 trades. |
| `RISK_API_OUTAGE_HALT_SEC` | `180` | API/infrastructure outage duration before HALT. |
| `RISK_MAX_CLOCK_DRIFT_SEC` | `2` | Maximum local/API server time drift accepted by readiness checks. |
| `RISK_RECONCILIATION_WINDOW_HOURS` | `72` | Broker/local reconciliation window. |
| `RISK_RECONCILIATION_SKEW_SEC` | `10` | Grace period for fresh in-flight orders during reconciliation. |
| `RISK_COMMISSION_TOLERANCE_RUB` | `0.01` | Commission comparison tolerance. Non-zero broker commission still violates zero-commission policy when required. |
| `RISK_CASH_USAGE_BUFFER` | `0.95` | Fraction of available cash usable for sizing. |
| `RISK_RISK_BUDGET_PER_INSTRUMENT_PCT` | `0.005` | Per-instrument risk budget used with adverse overnight move estimates. |
| `RISK_MIN_ORDER_NOTIONAL_RUB` | `1000` | Minimum order notional. |
| `RISK_SIZE_REDUCTION_WINDOW_TRADES` | `20` | Closed-trade window for realized-vs-expected edge checks. |
| `RISK_SIZE_REDUCTION_FACTOR` | `0.5` | Sizing multiplier applied after sustained edge deterioration. |
| `RISK_SIZE_REDUCTION_TRIGGER_BPS` | `-10` | Average error threshold that triggers size reduction. |

If the average `realized_edge_bps - expected_net_edge_bps` over the configured closed-trade window is below the trigger, the scheduler emits a risk event and reduces sizing. Repeated deterioration in `live_trade` can switch the runtime mode to `live_readonly`.

### LIQ

| Variable | Default | Description |
| --- | --- | --- |
| `LIQ_MIN_ADV_RUB` | `5000000` | Minimum 20-day average daily RUB volume. |
| `LIQ_MAX_PARTICIPATION_RATE` | `0.01` | Maximum share of entry/exit interval volume usable by the bot. |
| `LIQ_MAX_SPREAD_BPS_DEFAULT` | `20` | Default spread cap. |
| `LIQ_MAX_SPREAD_BPS_MONEY_MARKET` | `5` | Money-market fund spread cap. |
| `LIQ_MAX_SPREAD_BPS_BOND_FUNDS` | `10` | Bond fund spread cap. |
| `LIQ_MAX_SPREAD_BPS_EQUITY_FUNDS` | `25` | Equity fund spread cap. |
| `LIQ_MAX_TICK_BPS` | `10` | Maximum tick size relative to price. |

### COMM

| Variable | Default | Description |
| --- | --- | --- |
| `COMM_REQUIRE_ZERO_COMMISSION` | `true` | Rejects signals with expected commission above zero. |
| `COMM_QUARANTINE_ON_NONZERO` | `true` | Quarantines instruments and halts on actual non-zero broker commission. |
| `COMM_FREE_ORDER_COUNT_POLICY` | `submitted` | Free-order accounting policy: `submitted` or `cancel_counts`. |

For instruments, `free_order_limit_per_day=0` means the free-order policy is not configured and new entries are blocked; `-1` means the absence of a daily free-order limit has been explicitly confirmed.

### BT

| Variable | Default | Description |
| --- | --- | --- |
| `BT_DATE_FROM` | empty | Reserved period filter. |
| `BT_DATE_TO` | empty | Reserved period filter. |
| `BT_ENTRY_SLIPPAGE_BPS` | `8` | Backtest entry slippage. |
| `BT_EXIT_SLIPPAGE_BPS` | `8` | Backtest exit slippage. |
| `BT_COMMISSION_ROUNDTRIP_BPS` | `0` | Backtest round-trip commission. |
| `BT_USE_MINUTE_MODEL` | `false` | Enables conservative minute-candle execution modeling. |
| `BT_OUTPUT_DIR` | `./backtest_out` | Reserved output directory; the CLI currently uses `-out`. |

### LIVE

| Variable | Default | Description |
| --- | --- | --- |
| `LIVE_TRADE_ACK` | empty | Must be exactly `I_ACCEPT_RISK` for `APP_MODE=live_trade`. |
| `LIVE_READONLY_DAYS` | `0` | Must be at least `20` for `live_trade`. |
| `LIVE_PAPER_DAYS` | `0` | Must be at least `20` for `live_trade`. |
| `LIVE_SANDBOX_DAYS` | `0` | Must be at least `10` for `live_trade`. |
| `LIVE_COMMISSION_WHITELIST_CHECKED` | `false` | Must be `true` for `live_trade`. |
| `LIVE_TELEGRAM_TESTED` | `false` | Must be `true` for `live_trade`. |
| `LIVE_KILL_SWITCH_TESTED` | `false` | Must be `true` for `live_trade`. |
| `LIVE_SERVER_TIME_CHECKED` | `false` | Must be `true` for `live_trade`. |
| `LIVE_SMALL_CAPITAL` | `false` | Must be `true` for `live_trade`. |

## Commands

```sh
make fmt
make vet
make lint
make test
make race
make build

go run ./cmd/migrate -direction=up
go run ./cmd/migrate up
go run ./cmd/mode-days -check=true

go run ./cmd/backtest -candles candles.csv -out ./backtest_out
go run ./cmd/backtest -candles candles.csv -minute-candles minute.csv -use-minute-model -out ./backtest_out

go run ./cmd/bot -mode=paper
go run ./cmd/bot -halt -reason="manual kill switch"
go run ./cmd/bot -unhalt -reason="manual reconciliation complete"
go run ./cmd/bot -healthcheck
```

Backtest CSV columns:

```csv
instrument_uid,trade_date,open,high,low,close,volume_lots,lot,min_price_increment
TRUR,2024-01-09,100,101,99,100.5,10000,10,0.01
```

The minute model uses the same format, but `trade_date` may be a timestamp such as `2024-01-09T18:25:00Z` or `2024-01-09 18:25:00`. The backtest CLI requires `lot` and `min_price_increment` for every `instrument_uid`; metadata may come from either the daily CSV or the minute CSV.

`cmd/mode-days` counts distinct days from `system_state_history` and checks the `live_readonly >= 20`, `paper >= 20`, and `sandbox >= 10` live-trade gates. The history table is written after migration `0010`.

`ClientOrderID` is deterministic by `(date, instrument_uid, side, attempt)`, fits the T-Invest `order_id <= 36` limit, and contains a SHA-256 suffix to suppress duplicate broker orders after restarts.

## Deploy

The Gitea workflow builds static Linux binaries for `cmd/bot`, `cmd/migrate`, and `cmd/backtest`, ships them to the target host, installs the systemd unit from `deploy/systemd/overnight-trading-bot.service`, restarts the service, and verifies health with `overnight-trading-bot -healthcheck`.

Required Gitea secrets:

| Secret | Description |
| --- | --- |
| `secrets.DEPLOY_HOST` | Target host IP or DNS name. |
| `secrets.DEPLOY_SSH_PRIVATE_KEY_BASE64` | Root deployment SSH private key encoded with `base64 -w0 < id_ed25519`. |

Before the first deployment, create the production env file on the server:

```sh
install -d -m 0750 /etc/overnight-trading-bot
install -m 0640 .env.example /etc/overnight-trading-bot/overnight-trading-bot.env
```

Then replace `DB_DSN` with the external database address and fill T-Invest/Telegram secrets. The service runs as the unprivileged `overnight-bot` user with basic systemd hardening. Logs are available through:

```sh
journalctl -u overnight-trading-bot.service
```

## Runbook

API unavailable in the morning:

1. The bot retries requests with backoff.
2. If the outage exceeds `RISK_API_OUTAGE_HALT_SEC`, the system enters `HALTED`.
3. After recovery, run reconciliation first.
4. Manual recovery uses `go run ./cmd/bot -unhalt -reason="..."`.

Position not closed before the hard deadline:

1. The scheduler cancels active sell orders and marks unresolved positions as failed exit states.
2. New entries are blocked through `HALTED` with `hard_exit_deadline_missed`.
3. Manually reconcile broker portfolio, active orders, and the local database before unhalting.

Non-zero commission:

1. Reconciliation records a critical commission mismatch.
2. The instrument is quarantined when configured.
3. The bot enters `HALTED` through the zero-commission policy.

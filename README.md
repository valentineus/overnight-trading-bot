# Overnight Trading Bot

Go-бот для overnight-стратегии `close -> next open` на фондах T-Капитала через T-Invest API.

## Quick Start

```sh
cp .env.example .env
make test
APP_MODE=backtest go run ./cmd/bot
```

Для daemon-режимов (`paper`, `sandbox`, `live_readonly`, `live_trade`) нужен `DB_DSN` MariaDB/MySQL. `live_trade` дополнительно требует `LIVE_TRADE_ACK=I_ACCEPT_RISK` и выполненные pre-flight условия из секции `LIVE`.

## Environment Variables

Конфигурация читается из ENV через `.env`. Если значение не парсится в нужный тип, бот падает на старте с ошибкой `load ENV config`.

Общие форматы:

- Время указывается в формате `HH:MM:SS` и трактуется в `Europe/Moscow`.
- Доли указываются десятичной дробью: `0.10` означает 10%, `0.005` означает 0.5%.
- `bps` - базисные пункты: `10` означает 0.10%.
- Boolean-значения: `true` или `false`.
- В колонке "Дефолт" указан дефолт из кода. Если дефолта в коде нет, но в `.env.example` есть пример, это отмечено отдельно.
- Границы делятся на жёсткую валидацию старта и практические ограничения. Там, где валидации пока нет, указано рекомендуемое значение.

### APP

| Переменная | Что указывать | Дефолт | Границы/валидация | За что отвечает и что меняется |
| --- | --- | --- | --- | --- |
| `APP_MODE` | `backtest`, `paper`, `sandbox`, `live_readonly`, `live_trade` | нет, в `.env.example`: `paper` | обязательна; только перечисленные значения | Режим работы. `backtest` не требует БД и API в `cmd/bot`; `paper` без `TINVEST_TOKEN` использует fake gateway, а с токеном берёт реальные market data/status через T-Invest при симулированных заявках; `sandbox`, `live_readonly`, `live_trade` подключаются к T-Invest API; `live_trade` может отправлять брокерские заявки. |
| `APP_TIMEZONE` | `Europe/Moscow` | `Europe/Moscow` | жёстко только `Europe/Moscow` | Таймзона расписания торговых окон. Изменить нельзя без изменения валидации. |
| `APP_LOG_LEVEL` | `debug`, `info`, `warn`, `warning`, `error` | `info` | неизвестное значение трактуется как `info` | Уровень JSON-логов. Ниже уровень - больше диагностических записей. |
| `APP_HEALTHCHECK_ADDR` | HTTP listen address, например `:3300` или `127.0.0.1:3300` | `:3300` | без отдельной валидации | Адрес `/health` и `/ready`. При изменении меняется порт или интерфейс healthcheck-сервера. |
| `APP_SHUTDOWN_TIMEOUT_SEC` | целое число секунд | `30` | должно быть `> 0` | Таймаут graceful shutdown для HTTP healthcheck при остановке. |

### TINVEST

| Переменная | Что указывать | Дефолт | Границы/валидация | За что отвечает и что меняется |
| --- | --- | --- | --- | --- |
| `TINVEST_TOKEN` | токен T-Invest API | пусто | обязателен для `sandbox`, `live_readonly`, `live_trade`; опционален для `paper` | Доступ к реальному или sandbox API. В `paper` без токена используется fake gateway, с токеном - реальные market data и симулированные заявки. |
| `TINVEST_ACCOUNT_ID` | идентификатор брокерского счёта | пусто | обязателен для `sandbox`, `live_readonly`, `live_trade` | Счёт для портфеля, заявок и сверки. Для API-режимов бот падает на старте, если account id не указан. |
| `TINVEST_ENDPOINT` | gRPC endpoint T-Invest, обычно `host:port` | `invest-public-api.tinkoff.ru:443` | строка; валидации формата нет | Endpoint для API. В `sandbox` код принудительно использует sandbox endpoint. |
| `TINVEST_APP_NAME` | имя приложения | `overnight-trading-bot` | строка | Передаётся в SDK как имя клиента. Меняет идентификацию приложения на стороне API/логов. |
| `TINVEST_REQUEST_TIMEOUT_SEC` | целое число секунд | `10` | должно быть `> 0` | Таймаут API-запросов к T-Invest, включая retry-последовательность. Меньше значение быстрее освобождает торговый цикл при зависшем API, но повышает шанс timeout на медленной сети. |
| `TINVEST_RETRY_COUNT` | целое число попыток | `3` | `<= 0` трактуется как одна попытка | Общее число попыток для SDK-вызовов T-Invest через exponential backoff. Больше значение повышает устойчивость к кратким сбоям, но может дольше задерживать окончательную ошибку. |
| `TINVEST_RETRY_BACKOFF_SEC` | целое число секунд | `2` | рекомендуется `>= 0` | Начальный интервал exponential backoff для SDK-вызовов T-Invest. Больше значение снижает частоту повторов при сбоях, но дольше задерживает окончательную ошибку. |
| `TINVEST_USE_SANDBOX` | `true` или `false` | `false` | boolean; разрешено только при `APP_MODE=sandbox` | Защитный флаг совместимости. В `live_readonly` и `live_trade` запрещён валидацией, чтобы случайно не подменить фактическую среду исполнения. |

### DB

| Переменная | Что указывать | Дефолт | Границы/валидация | За что отвечает и что меняется |
| --- | --- | --- | --- | --- |
| `DB_DSN` | MySQL/MariaDB DSN, например `bot:change-me@tcp(db.example.internal:3306)/overnight_bot?parseTime=true&loc=UTC&multiStatements=true` | нет, пример есть в `.env.example` | обязателен во всех режимах, кроме `backtest`; должен открываться драйвером MySQL | Подключение к БД. В БД хранятся инструменты, свечи, сигналы, заявки, позиции, состояния, события риска и отчёты. |
| `DB_MAX_OPEN_CONNS` | целое число | `20` | валидации нет; `<= 0` для `database/sql` означает без лимита | Максимум открытых соединений с БД. Больше - выше параллелизм, но больше нагрузка на MariaDB. |
| `DB_MAX_IDLE_CONNS` | целое число | `5` | валидации нет; `<= 0` отключает idle pool | Размер пула простаивающих соединений. Больше - меньше переподключений, но больше удерживаемых соединений. |
| `DB_CONN_MAX_LIFETIME_MIN` | целое число минут | `30` | валидации нет; `<= 0` отключает лимит lifetime | Сколько живёт соединение до пересоздания. Меньше - чаще переподключения, больше - дольше используются старые соединения. |
| `DB_MIGRATIONS_AUTO_APPLY` | `true` или `false` | `true` | boolean | Автоматически применяет миграции при старте daemon-режима. `false` требует запускать миграции вручную через `cmd/migrate`. |

### TELEGRAM

| Переменная | Что указывать | Дефолт | Границы/валидация | За что отвечает и что меняется |
| --- | --- | --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | токен Telegram-бота | пусто | строка | Если токен или `TELEGRAM_CHAT_ID` пустые, уведомления отключены и используется noop notifier. |
| `TELEGRAM_CHAT_ID` | числовой chat id | `0` | `int64`; `0` отключает Telegram | Чат, куда отправляются уведомления. |
| `TELEGRAM_NOTIFY_INFO` | `true` или `false` | `true` | boolean | Включает информационные сообщения, например старт бота и события заявок. При переполнении очереди такие сообщения могут быть отброшены. |
| `TELEGRAM_NOTIFY_WARN` | `true` или `false` | `true` | boolean | Включает предупреждения. |
| `TELEGRAM_NOTIFY_ALERT` | `true` или `false` | `true` | boolean | Включает alert-сообщения. Они считаются критичными для доставки и ждут место в очереди. |
| `TELEGRAM_NOTIFY_REPORT` | `true` или `false` | `true` | boolean | Включает дневные отчёты. |

### STRATEGY

| Переменная | Что указывать | Дефолт | Границы/валидация | За что отвечает и что меняется |
| --- | --- | --- | --- | --- |
| `STRATEGY_ROLLING_SHORT` | количество торговых дней | `60` | рекомендуется `> 0` | Короткое окно статистики overnight-доходности. Больше - стабильнее оценка, но медленнее реакция; меньше - быстрее реакция, но больше шум. |
| `STRATEGY_ROLLING_LONG` | количество торговых дней | `252` | рекомендуется `>= STRATEGY_ROLLING_SHORT` и `> 0` | Длинное окно для проверки положительного долгосрочного edge и глубины backfill. Больше требует больше истории. |
| `STRATEGY_EWMA_LAMBDA` | дробь для EWMA | `0.08` | рабочий диапазон `(0, 1]`; вне диапазона EWMA-функция использует `0.08` | Вес новых наблюдений в EWMA. Больше - свежее движение влияет сильнее. |
| `STRATEGY_ALLOCATION_METHOD` | `equal_weight` | `equal_weight` | сейчас поддерживается только `equal_weight` | Метод распределения капитала между выбранными сигналами. Текущая реализация делит лимит экспозиции поровну между выбранными инструментами. |
| `STRATEGY_MIN_TSTAT_60` | decimal t-stat | `1.25` | валидации нет; обычно `>= 0` | Минимальная статистическая значимость короткого edge. Выше - меньше входов, ниже - больше входов. |
| `STRATEGY_MIN_WIN_RATE_60` | доля прибыльных overnight-дней | `0.55` | рекомендуется `0..1` | Минимальная доля положительных overnight-наблюдений. Выше - строже фильтр сигналов. |
| `STRATEGY_MIN_NET_EDGE_BPS` | bps | `10` | валидации нет; обычно `>= 0` | Минимальный ожидаемый edge после издержек. Выше - меньше, но потенциально качественнее сигналы. |
| `STRATEGY_RISK_BUFFER_BPS` | bps | `5` | валидации нет; обычно `>= 0` | Дополнительная надбавка к ожидаемым издержкам в расчёте `NetEdgeBps`. Больше - консервативнее отбор. |
| `STRATEGY_MAX_POSITIONS` | целое число позиций | `5` | `> 0` включает лимит; `<= 0` фактически отключает signal-level лимит | Максимум одновременно открытых позиций на уровне генерации сигналов. Больше - больше диверсификация и нагрузка на капитал. |

### EXEC

| Переменная | Что указывать | Дефолт | Границы/валидация | За что отвечает и что меняется |
| --- | --- | --- | --- | --- |
| `EXEC_ENTRY_SIGNAL_TIME` | `HH:MM:SS` | `18:10:00` | должно парситься как время | Время старта подготовки данных и генерации сигналов. |
| `EXEC_ENTRY_WINDOW_START` | `HH:MM:SS` | `18:20:00` | `ENTRY_WINDOW_START < ENTRY_WINDOW_END <= NO_NEW_ENTRY_AFTER` | Начало окна постановки заявок на вход. Позже - меньше времени на исполнение входа. |
| `EXEC_ENTRY_WINDOW_END` | `HH:MM:SS` | `18:38:30` | см. правило окна входа | Конец активной постановки заявок на вход и market close для pre-trade проверки входа. |
| `EXEC_NO_NEW_ENTRY_AFTER` | `HH:MM:SS` | `18:38:30` | не раньше `EXEC_ENTRY_WINDOW_END` | После этого времени новые входы не ставятся, бот переходит в overnight hold. |
| `EXEC_EXIT_WATCH_START` | `HH:MM:SS` | `09:50:00` | `EXIT_WATCH_START <= EXIT_NOT_BEFORE <= EXIT_WINDOW_START < EXIT_WINDOW_END <= HARD_EXIT_DEADLINE` | Начало утреннего наблюдения перед выходом. До `EXEC_EXIT_WINDOW_START` заявки на выход ещё не ставятся. |
| `EXEC_EXIT_NOT_BEFORE` | `HH:MM:SS` | `10:03:00` | см. правило окна выхода; сейчас используется только валидацией | Нижняя граница "не выходить раньше". На текущий scheduler напрямую не влияет, потому что заявки начинаются с `EXEC_EXIT_WINDOW_START`. |
| `EXEC_EXIT_WINDOW_START` | `HH:MM:SS` | `10:05:00` | см. правило окна выхода | Начало постановки заявок на выход. Раньше - больше шанс выйти быстрее, но ближе к открытию рынка. |
| `EXEC_EXIT_WINDOW_END` | `HH:MM:SS` | `10:25:00` | см. правило окна выхода | Конец постановки новых exit-заявок, после него идёт мониторинг до hard deadline. |
| `EXEC_HARD_EXIT_DEADLINE` | `HH:MM:SS` | `10:45:00` | не раньше `EXEC_EXIT_WINDOW_END` | Крайний срок выхода. После него запускаются reconciliation и report; незакрытая позиция ведёт к ручной обработке/HALT-сценарию. |
| `EXEC_MIN_TIME_TO_CLOSE_SEC` | целое число секунд | `90` | `> 0` включает проверку; `<= 0` отключает | Минимальный запас до конца торгового окна для pre-trade. Больше - меньше риск ставить заявку слишком поздно. |
| `EXEC_ALLOW_MARKET_ORDERS` | только `false` | `false` | жёстко должно быть `false` | Защитный флаг стратегии LIMIT-only. `true` запрещён валидацией. |
| `EXEC_MAX_ENTRY_ORDER_ATTEMPTS` | целое число | `3` | рекомендуется `>= 1` | Максимальное число постановок входной заявки в `MonitorUntil`: после polling/repost остаток отменяется к дедлайну окна входа. |
| `EXEC_MAX_EXIT_ORDER_ATTEMPTS` | целое число | `3` | рекомендуется `>= 1` | Максимальное число постановок выходной заявки в `MonitorUntil`: после polling/repost остаток отменяется к hard deadline. |
| `EXEC_PASSIVE_IMPROVE_TICKS` | целое число тиков | `1` | отрицательное значение в pricing приравнивается к `0` | Насколько улучшать passive limit price от лучшего bid/ask. Больше - цена агрессивнее, но код не пересекает spread. |
| `EXEC_QUOTE_DEPTH` | целое число уровней стакана | `20` | `1..50` | Глубина стакана для оценки bid/ask и spread. Больше - больше данных из API, но для цены используется лучший уровень. |
| `EXEC_MAX_QUOTE_AGE_SEC` | целое число секунд | `3` | `> 0` включает проверку; `<= 0` отключает | Максимальный возраст котировки. Меньше - строже к свежести данных, но больше отказов `quote age exceeds`. |
| `EXEC_ORDER_POLL_INTERVAL_MS` | целое число миллисекунд | `500` | рекомендуется `> 0` | Частота polling статусов заявок в `MonitorUntil`; также задаёт нижнюю границу интервала между repost-попытками. |

### RISK

| Переменная | Что указывать | Дефолт | Границы/валидация | За что отвечает и что меняется |
| --- | --- | --- | --- | --- |
| `RISK_USE_MARGIN` | только `false` | `false` | жёстко должно быть `false` | Защитный запрет маржинальной торговли. |
| `RISK_ALLOW_SHORT` | только `false` | `false` | жёстко должно быть `false` | Защитный запрет коротких позиций. |
| `RISK_MAX_TOTAL_EXPOSURE_PCT` | доля equity | `0.50` | рекомендуется `0..1`; `0` фактически запрещает новые позиции | Общий лимит экспозиции, делится на выбранные инструменты при sizing. Больше - больше капитал в рынке. |
| `RISK_MAX_POSITION_PCT` | доля equity | `0.10` | рекомендуется `0..1`; `0` запрещает размер позиции | Максимальный размер одной позиции от equity. |
| `RISK_MAX_DAILY_LOSS_PCT` | доля equity | `0.01` | `> 0` включает лимит; `<= 0` отключает | Дневной стоп по убытку. При достижении pre-trade отклоняет новые заявки. |
| `RISK_MAX_WEEKLY_LOSS_PCT` | доля equity | `0.03` | `> 0` включает лимит; `<= 0` отключает | Недельный стоп по убытку. |
| `RISK_MAX_MONTHLY_DRAWDOWN_PCT` | доля equity | `0.07` | `> 0` включает лимит; `<= 0` отключает | Месячный лимит просадки. |
| `RISK_MAX_OPEN_POSITIONS` | целое число | `5` | `> 0` включает лимит; `<= 0` отключает | Risk-level максимум открытых позиций перед постановкой заявки. |
| `RISK_MAX_AVG_SLIPPAGE_BPS_10_TRADES` | bps | `15` | `> 0` включает лимит; `<= 0` отключает | Блокирует новые заявки при слишком большом среднем slippage за 10 сделок. |
| `RISK_API_OUTAGE_HALT_SEC` | целое число секунд | `180` | должно быть `> 0` | Если инфраструктурный/API сбой длится дольше, бот переводится в HALT. Больше - терпимее к сбоям, меньше - быстрее останавливается. |
| `RISK_MAX_CLOCK_DRIFT_SEC` | целое число секунд | `2` | `> 0` включает проверку drift; `<= 0` отключает | Максимальный рассинхрон локального времени и серверного времени API в `/ready`. |
| `RISK_RECONCILIATION_WINDOW_HOURS` | целое число часов | `72` | должно быть `> 0` | Глубина сверки последних заявок и операций брокера. Больше - больше история сверки, но тяжелее запросы. |
| `RISK_RECONCILIATION_SKEW_SEC` | целое число секунд | `10` | `>= 0` | Grace-window для только что отправленных локальных заявок: свежие in-flight orders не считаются diff, пока брокерский active-list догоняет запись. |
| `RISK_COMMISSION_TOLERANCE_RUB` | сумма в рублях | `0.01` | `>= 0` | Допуск для reconciliation по расхождению локальной и брокерской комиссии. Ненулевая брокерская комиссия всё равно считается нарушением при `COMM_REQUIRE_ZERO_COMMISSION=true`. |
| `RISK_CASH_USAGE_BUFFER` | доля cash | `0.95` | рекомендуется `0..1`; `0` запрещает использование cash | Какая часть свободных денег может идти в sizing. Меньше - больше денежный буфер. |
| `RISK_RISK_BUDGET_PER_INSTRUMENT_PCT` | доля equity | `0.005` | рекомендуется `> 0` | Риск-бюджет на инструмент, используется вместе с оценкой неблагоприятного overnight-движения. Больше - крупнее позиции при прочих равных. |
| `RISK_MIN_ORDER_NOTIONAL_RUB` | сумма в рублях | `1000` | `> 0` включает минимум; `<= 0` фактически отключает | Минимальный notional заявки. Если рассчитанная позиция меньше, сигнал отклоняется по sizing. |

Если средний `realized_edge_bps - expected_net_edge_bps` по последним 20 закрытым сделкам ниже `-10 bps`, scheduler пишет `risk_event(WARN, size_reduction_rule_triggered)` и до восстановления качества режет sizing до `0.5x`.

### LIQ

| Переменная | Что указывать | Дефолт | Границы/валидация | За что отвечает и что меняется |
| --- | --- | --- | --- | --- |
| `LIQ_MIN_ADV_RUB` | сумма в рублях | `5000000` | рекомендуется `>= 0` | Минимальный средний дневной оборот за 20 дней. Выше - отсекает менее ликвидные фонды. |
| `LIQ_MAX_PARTICIPATION_RATE` | доля объёма | `0.01` | рекомендуется `0..1` | Максимальная доля объёма входного/выходного окна, которую может занять бот при sizing. Больше - крупнее позиции, но выше рыночное воздействие. |
| `LIQ_MAX_SPREAD_BPS_DEFAULT` | bps | `20` | рекомендуется `>= 0` | Максимальный spread для фондов без специальной категории. Ниже - строже фильтр ликвидности. |
| `LIQ_MAX_SPREAD_BPS_MONEY_MARKET` | bps | `5` | рекомендуется `>= 0` | Максимальный spread для money market фондов. |
| `LIQ_MAX_SPREAD_BPS_BOND_FUNDS` | bps | `10` | рекомендуется `>= 0` | Максимальный spread для bond/corporate bond фондов. |
| `LIQ_MAX_SPREAD_BPS_EQUITY_FUNDS` | bps | `25` | рекомендуется `>= 0` | Максимальный spread для equity фондов. |
| `LIQ_MAX_TICK_BPS` | bps | `10` | рекомендуется `>= 0` | Максимальный размер минимального шага цены относительно цены. Ниже - отсекает инструменты с грубым тиком. |

### COMM

| Переменная | Что указывать | Дефолт | Границы/валидация | За что отвечает и что меняется |
| --- | --- | --- | --- | --- |
| `COMM_REQUIRE_ZERO_COMMISSION` | `true` или `false` | `true` | boolean | При `true` сигналы по инструментам с ожидаемой комиссией `> 0` отклоняются. |
| `COMM_QUARANTINE_ON_NONZERO` | `true` или `false` | `true` | boolean | При фактической брокерской комиссии `> 0` инструмент переводится в quarantine, а система останавливается через HALT по zero-commission policy. |
| `COMM_FREE_ORDER_COUNT_POLICY` | `submitted` или `cancel_counts` | `submitted` | одно из двух значений | Политика учёта бесплатных заявок: `submitted` считает только отправку новой заявки, `cancel_counts` дополнительно считает успешные отмены перед repost. |

В справочнике инструментов `free_order_limit_per_day=0` означает, что политика бесплатных заявок не настроена и новые входы запрещены; `-1` означает явно подтверждённое отсутствие дневного лимита.

### BT

| Переменная | Что указывать | Дефолт | Границы/валидация | За что отвечает и что меняется |
| --- | --- | --- | --- | --- |
| `BT_DATE_FROM` | дата `YYYY-MM-DD` | пусто | сейчас не применяется | Зарезервировано под фильтр периода backtest. На текущий `cmd/bot` и `cmd/backtest` не влияет. |
| `BT_DATE_TO` | дата `YYYY-MM-DD` | пусто | сейчас не применяется | Зарезервировано под фильтр периода backtest. На текущий `cmd/bot` и `cmd/backtest` не влияет. |
| `BT_ENTRY_SLIPPAGE_BPS` | bps | `8` | рекомендуется `>= 0` | Модельная издержка входа. Используется в расчёте `ExpectedCostBps`/`NetEdgeBps`; больше - строже отбор сигналов. |
| `BT_EXIT_SLIPPAGE_BPS` | bps | `8` | рекомендуется `>= 0` | Модельная издержка выхода. Больше - снижает ожидаемый net edge. |
| `BT_COMMISSION_ROUNDTRIP_BPS` | bps | `0` | рекомендуется `>= 0` | Модельная комиссия за полный круг. Увеличение снижает `NetEdgeBps`; при zero-commission политике ненулевые комиссии могут отсеивать сделки в backtest engine. |
| `BT_USE_MINUTE_MODEL` | `true` или `false` | `false` | boolean | Включает консервативную minute-модель backtest: лимитная цена должна быть достижима внутри минутной свечи, размер ограничивается participation cap по минутному объёму. В CLI соответствует `-use-minute-model` и требует `-minute-candles`. |
| `BT_OUTPUT_DIR` | путь к директории | `./backtest_out` | сейчас не применяется в `cmd/backtest`, где используется флаг `-out` | Зарезервированный ENV-путь для результатов backtest. |

### LIVE

| Переменная | Что указывать | Дефолт | Границы/валидация | За что отвечает и что меняется |
| --- | --- | --- | --- | --- |
| `LIVE_TRADE_ACK` | ровно `I_ACCEPT_RISK` | пусто | обязателен только для `APP_MODE=live_trade` | Ручное подтверждение риска для режима реальной торговли. Без него `live_trade` не стартует. |
| `LIVE_READONLY_DAYS` | целое число торговых дней | `0` | для `live_trade` должно быть `>= 20` | Подтверждает накопленный период работы в `live_readonly` перед реальной торговлей. |
| `LIVE_PAPER_DAYS` | целое число торговых дней | `0` | для `live_trade` должно быть `>= 20` | Подтверждает период `paper`-прогона с bid/ask моделью. |
| `LIVE_SANDBOX_DAYS` | целое число торговых дней | `0` | для `live_trade` должно быть `>= 10` | Подтверждает период sandbox без критических ошибок. |
| `LIVE_COMMISSION_WHITELIST_CHECKED` | `true` или `false` | `false` | для `live_trade` должно быть `true` | Ручное подтверждение актуальных комиссий и whitelist инструментов. |
| `LIVE_TELEGRAM_TESTED` | `true` или `false` | `false` | для `live_trade` должно быть `true` | Подтверждает тест доставки Telegram-уведомлений. |
| `LIVE_KILL_SWITCH_TESTED` | `true` или `false` | `false` | для `live_trade` должно быть `true` | Подтверждает тест ручного halt/unhalt сценария. |
| `LIVE_SERVER_TIME_CHECKED` | `true` или `false` | `false` | для `live_trade` должно быть `true` | Подтверждает проверку server-time/drift в sandbox. |
| `LIVE_SMALL_CAPITAL` | `true` или `false` | `false` | для `live_trade` должно быть `true` | Подтверждает запуск реальной торговли с малым стартовым капиталом. |

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
go run ./cmd/backtest -candles candles.csv -out ./backtest_out
go run ./cmd/backtest -candles candles.csv -minute-candles minute.csv -use-minute-model -out ./backtest_out
go run ./cmd/bot -mode=paper
go run ./cmd/bot -halt -reason="manual kill switch"
go run ./cmd/bot -unhalt -reason="manual reconciliation complete"
go run ./cmd/bot -healthcheck
```

Backtest CSV columns:

```csv
instrument_uid,trade_date,open,high,low,close,volume_lots
TRUR,2024-01-09,100,101,99,100.5,10000
```

Для minute-модели используется тот же формат, но `trade_date` может быть timestamp (`2024-01-09T18:25:00Z` или `2024-01-09 18:25:00`).

`ClientOrderID` детерминирован по `(date, instrument_uid, side, attempt)`, укладывается в лимит T-Invest `order_id <= 36` и содержит SHA-256 suffix. При ручных массовых перезапусках с теми же параметрами id остаётся тем же, что намеренно подавляет дубли.

## Deploy

`.gitea/workflows/deploy.yml` собирает статический бинарь и кладёт его на сервер. Никакого Docker/Podman ни в CI, ни на сервере не используется — служба запускается напрямую через `systemd` внутри LXC-контейнера. БД (MariaDB/MySQL) живёт на отдельном хосте и подключается через `DB_DSN`.

Pipeline на push в `master` (один job `deploy`):

1. `actions/setup-go@v5` (версия из `go.mod`), `go mod download`, `go vet ./...`, `go test ./...`.
2. Кросс-компиляция `linux/amd64` с `CGO_ENABLED=0` и `-trimpath -ldflags="-s -w"` для `cmd/bot`, `cmd/migrate`, `cmd/backtest`. Бинари именуются `overnight-trading-bot`, `overnight-trading-bot-migrate`, `overnight-trading-bot-backtest` — чтобы безопасно жить в `/usr/local/bin`.
3. Сборка `out/release.tar.gz` с бинарями и `deploy/systemd/overnight-trading-bot.service`.
4. SSH-ключ из base64 → `~/.ssh/deploy_key`, `ssh-keyscan` целевого хоста.
5. `scp` архива в `/var/tmp/overnight-trading-bot-deploy/release.tar.gz` на сервере.
6. На сервере: проверка наличия env-файла, идемпотентное создание системного пользователя `overnight-bot`, распаковка во временный staging-каталог `/var/tmp/overnight-trading-bot-deploy/stage`, атомарная замена `/usr/local/bin/overnight-trading-bot{,-migrate,-backtest}` через `*.new` → `mv -f`, копирование systemd unit в `/etc/systemd/system/`, `systemctl daemon-reload && enable && restart`. После рестарта workflow ждёт `is-active` (до 60 с), затем проверяет `overnight-trading-bot -healthcheck` (до 30 с); при провале печатает последние 100 строк `journalctl` и завершается с ошибкой.

Переменные Gitea:

- `secrets.DEPLOY_HOST` - IP сервера.
- `secrets.DEPLOY_SSH_PRIVATE_KEY_BASE64` - приватный SSH-ключ root в base64 (`base64 -w0 < id_ed25519`).

На сервере (debian 13 LXC) заранее должны быть установлены `systemd` (и стандартные утилиты `tar`, `useradd`, `journalctl` — все из coreutils/util-linux/systemd, в базовом образе debian 13 уже есть). Перед первым деплоем нужно создать production env-файл:

```sh
install -d -m 0750 /etc/overnight-trading-bot
install -m 0640 .env.example /etc/overnight-trading-bot/overnight-trading-bot.env
```

В `/etc/overnight-trading-bot/overnight-trading-bot.env` нужно заменить `DB_DSN` на адрес внешней БД и заполнить секреты T-Invest/Telegram. Workflow при каждом деплое перевыставляет владельцем группу `overnight-bot` и режим `0640`, чтобы env читался службой, но не был world-readable. Если файла нет, workflow падает до перезапуска службы.

Systemd unit (`deploy/systemd/overnight-trading-bot.service`) запускает `/usr/local/bin/overnight-trading-bot` под непривилегированным пользователем `overnight-bot` с базовым systemd hardening (`NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, пустой `CapabilityBoundingSet` и т.д.). Логи смотрятся через `journalctl -u overnight-trading-bot.service`.

## Runbook

API недоступен утром:

1. Бот повторяет запросы с retry/backoff.
2. Если сбой дольше `RISK_API_OUTAGE_HALT_SEC`, состояние переводится в `HALTED`.
3. После восстановления сначала запускается reconciliation.
4. Ручной вывод из HALT: `go run ./cmd/bot -unhalt -reason="..."`.

Позиция не закрыта до hard deadline:

1. Scheduler отменяет активные sell-заявки, помечает незакрытые `HOLDING_OVERNIGHT`/`EXIT_ORDER_SENT`/`EXIT_PARTIALLY_FILLED` как `EXIT_FAILED` и отправляет critical alert.
2. Новые входы блокируются через HALT (`hard_exit_deadline_missed`).
3. Требуется ручная сверка брокерского портфеля, активных заявок и локальной БД; `-unhalt` выполнит reconciliation перед снятием HALT и откажется продолжать при critical diff.

Ненулевая комиссия:

1. Reconciliation фиксирует критическое расхождение по комиссии.
2. Бот уходит в `HALTED` через событие риска `reconciliation_critical`.
3. Инструмент нужно вручную перевести в quarantine или выключить до выяснения причины. Автоматический quarantine по `COMM_QUARANTINE_ON_NONZERO` сейчас не подключён.

Превышен лимит риска при открытой позиции:

1. `HALTED` блокирует любые новые заявки, включая автоматический exit.
2. Оператор делает ручную сверку брокерского портфеля, активных заявок и локальных `positions`/`orders`.
3. Если позицию нужно закрывать ботом, сначала выполняется `go run ./cmd/bot -unhalt -reason="manual reconciliation before exit"` после успешной reconciliation; если расхождения остаются, закрытие выполняется вручную у брокера и затем синхронизируется в БД.

Полевой sandbox-чек времени сервера:

1. Перед `live_readonly` выполнить sandbox-запуск, в котором `GetServerTime` получает `Date` из gRPC metadata.
2. Если SDK/API не возвращает `Date`, `GetServerTime` отдаёт явную ошибку; в `paper` она глушится, в API-режимах учитывается через `RISK_API_OUTAGE_HALT_SEC`.
3. До исправления источника server-time не переводить этот аккаунт в `live_trade`.

## Live Preconditions

Перед `live_trade` должны быть выполнены условия из ТЗ: минимум 20 торговых дней `live_readonly`, 20 дней `paper`, 10 дней `sandbox`, ручная проверка комиссий и whitelist, Telegram-тест, kill-switch-тест, успешный server-time check в sandbox и малый стартовый капитал.

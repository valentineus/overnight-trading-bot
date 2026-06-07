CREATE TABLE IF NOT EXISTS schema_meta (
  meta_key VARCHAR(64) PRIMARY KEY,
  meta_value VARCHAR(255) NOT NULL,
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS instruments (
  instrument_uid VARCHAR(128) PRIMARY KEY,
  figi VARCHAR(64),
  ticker VARCHAR(32) NOT NULL,
  class_code VARCHAR(32) NOT NULL DEFAULT 'TQTF',
  name VARCHAR(255) NOT NULL DEFAULT '',
  lot BIGINT NOT NULL DEFAULT 1,
  min_price_increment DECIMAL(20,8) NOT NULL DEFAULT 0,
  currency VARCHAR(8) NOT NULL DEFAULT 'RUB',
  enabled TINYINT(1) NOT NULL DEFAULT 1,
  fund_type VARCHAR(64) NOT NULL DEFAULT '',
  expected_commission_bps_per_side DECIMAL(12,4) NOT NULL DEFAULT 0,
  free_order_limit_per_day INT NOT NULL DEFAULT 0 COMMENT '0 means no configured free-order cap',
  quarantine TINYINT(1) NOT NULL DEFAULT 0,
  quarantine_reason TEXT,
  exclude_reason TEXT,
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  UNIQUE KEY ux_instruments_ticker_class (ticker, class_code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS candles_daily (
  instrument_uid VARCHAR(128) NOT NULL,
  trade_date DATE NOT NULL,
  open DECIMAL(20,8) NOT NULL,
  high DECIMAL(20,8) NOT NULL,
  low DECIMAL(20,8) NOT NULL,
  close DECIMAL(20,8) NOT NULL,
  volume_lots DECIMAL(20,8) NOT NULL DEFAULT 0,
  source VARCHAR(32) NOT NULL,
  loaded_at DATETIME(3) NOT NULL,
  PRIMARY KEY (instrument_uid, trade_date),
  CONSTRAINT fk_candles_daily_instrument FOREIGN KEY (instrument_uid) REFERENCES instruments(instrument_uid) ON UPDATE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS candles_minute (
  instrument_uid VARCHAR(128) NOT NULL,
  ts DATETIME(3) NOT NULL,
  open DECIMAL(20,8) NOT NULL,
  high DECIMAL(20,8) NOT NULL,
  low DECIMAL(20,8) NOT NULL,
  close DECIMAL(20,8) NOT NULL,
  volume_lots DECIMAL(20,8) NOT NULL DEFAULT 0,
  source VARCHAR(32) NOT NULL,
  loaded_at DATETIME(3) NOT NULL,
  PRIMARY KEY (instrument_uid, ts),
  CONSTRAINT fk_candles_minute_instrument FOREIGN KEY (instrument_uid) REFERENCES instruments(instrument_uid) ON UPDATE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS features (
  instrument_uid VARCHAR(128) NOT NULL,
  trade_date DATE NOT NULL,
  r_on DECIMAL(20,10) NOT NULL DEFAULT 0,
  r_day DECIMAL(20,10) NOT NULL DEFAULT 0,
  mu_on_60 DECIMAL(20,10) NOT NULL DEFAULT 0,
  mu_on_252 DECIMAL(20,10) NOT NULL DEFAULT 0,
  sigma_on_60 DECIMAL(20,10) NOT NULL DEFAULT 0,
  tstat_on_60 DECIMAL(20,10) NOT NULL DEFAULT 0,
  win_on_60 DECIMAL(20,10) NOT NULL DEFAULT 0,
  ewma_on DECIMAL(20,10) NOT NULL DEFAULT 0,
  spread_bps DECIMAL(12,4) NOT NULL DEFAULT 0,
  half_spread_bps DECIMAL(12,4) NOT NULL DEFAULT 0,
  tick_bps DECIMAL(12,4) NOT NULL DEFAULT 0,
  adv_20 DECIMAL(20,8) NOT NULL DEFAULT 0,
  expected_cost_bps DECIMAL(12,4) NOT NULL DEFAULT 0,
  net_edge_bps DECIMAL(12,4) NOT NULL DEFAULT 0,
  entry_interval_volume DECIMAL(20,8) NOT NULL DEFAULT 0,
  exit_interval_volume DECIMAL(20,8) NOT NULL DEFAULT 0,
  calculated_at DATETIME(3) NOT NULL,
  PRIMARY KEY (instrument_uid, trade_date),
  CONSTRAINT fk_features_instrument FOREIGN KEY (instrument_uid) REFERENCES instruments(instrument_uid) ON UPDATE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS signals (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  trade_date DATE NOT NULL,
  instrument_uid VARCHAR(128) NOT NULL,
  decision ENUM('ENTER','SKIP','REJECT') NOT NULL,
  score DECIMAL(20,10) NOT NULL DEFAULT 0,
  net_edge_bps DECIMAL(12,4) NOT NULL DEFAULT 0,
  target_notional DECIMAL(20,8) NOT NULL DEFAULT 0,
  target_lots BIGINT NOT NULL DEFAULT 0,
  reject_reason VARCHAR(128),
  context_json JSON,
  created_at DATETIME(3) NOT NULL,
  UNIQUE KEY ux_signals_date_instr (trade_date, instrument_uid),
  CONSTRAINT fk_signals_instrument FOREIGN KEY (instrument_uid) REFERENCES instruments(instrument_uid) ON UPDATE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS orders (
  client_order_id VARCHAR(128) PRIMARY KEY,
  broker_order_id VARCHAR(128),
  account_id_hash VARCHAR(128) NOT NULL,
  instrument_uid VARCHAR(128) NOT NULL,
  trade_date DATE NOT NULL,
  side ENUM('BUY','SELL') NOT NULL,
  order_type ENUM('LIMIT') NOT NULL,
  limit_price DECIMAL(20,8) NOT NULL DEFAULT 0,
  quantity_lots BIGINT NOT NULL,
  filled_lots BIGINT NOT NULL DEFAULT 0,
  avg_fill_price DECIMAL(20,8) NOT NULL DEFAULT 0,
  status ENUM('NEW','SENT','PARTIALLY_FILLED','FILLED','CANCELLED','REJECTED','EXPIRED','FAILED') NOT NULL,
  commission DECIMAL(20,8) NOT NULL DEFAULT 0,
  attempt_no INT NOT NULL DEFAULT 1,
  raw_state_json JSON,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  UNIQUE KEY ux_orders_broker_order_id (broker_order_id),
  KEY ix_orders_active (account_id_hash, status),
  CONSTRAINT fk_orders_instrument FOREIGN KEY (instrument_uid) REFERENCES instruments(instrument_uid) ON UPDATE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS positions (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  account_id_hash VARCHAR(128) NOT NULL,
  instrument_uid VARCHAR(128) NOT NULL,
  open_trade_date DATE NOT NULL,
  lots BIGINT NOT NULL,
  avg_buy_price DECIMAL(20,8) NOT NULL DEFAULT 0,
  avg_sell_price DECIMAL(20,8) NOT NULL DEFAULT 0,
  status ENUM('NO_POSITION','ENTRY_SIGNALLED','ENTRY_ORDER_SENT','ENTRY_PARTIALLY_FILLED','ENTRY_FILLED','HOLDING_OVERNIGHT','EXIT_ORDER_SENT','EXIT_PARTIALLY_FILLED','EXIT_FILLED','EXIT_FAILED','QUARANTINE') NOT NULL,
  gross_pnl DECIMAL(20,8) NOT NULL DEFAULT 0,
  net_pnl DECIMAL(20,8) NOT NULL DEFAULT 0,
  commission_total DECIMAL(20,8) NOT NULL DEFAULT 0,
  realized_edge_bps DECIMAL(12,4) NOT NULL DEFAULT 0,
  opened_at DATETIME(3),
  closed_at DATETIME(3),
  updated_at DATETIME(3) NOT NULL,
  KEY ix_positions_open (account_id_hash, status),
  CONSTRAINT fk_positions_instrument FOREIGN KEY (instrument_uid) REFERENCES instruments(instrument_uid) ON UPDATE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS risk_events (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  ts DATETIME(3) NOT NULL,
  severity ENUM('INFO','WARN','ALERT','CRITICAL') NOT NULL,
  event_type VARCHAR(128) NOT NULL,
  instrument_uid VARCHAR(128),
  message TEXT NOT NULL,
  raw_context_json JSON,
  KEY ix_risk_events_ts (ts)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS free_order_counters (
  trade_date DATE NOT NULL,
  instrument_uid VARCHAR(128) NOT NULL,
  orders_sent INT NOT NULL DEFAULT 0,
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (trade_date, instrument_uid),
  CONSTRAINT fk_free_orders_instrument FOREIGN KEY (instrument_uid) REFERENCES instruments(instrument_uid) ON UPDATE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS system_state (
  id TINYINT NOT NULL PRIMARY KEY,
  state ENUM('INIT','SYNC_INSTRUMENTS','SYNC_MARKET_DATA','GENERATE_SIGNALS','WAIT_ENTRY_WINDOW','PLACE_ENTRY_ORDERS','MONITOR_ENTRY_ORDERS','HOLD_OVERNIGHT','WAIT_EXIT_WINDOW','PLACE_EXIT_ORDERS','MONITOR_EXIT_ORDERS','RECONCILE','REPORT','SLEEP','HALTED') NOT NULL,
  mode ENUM('backtest','paper','sandbox','live_readonly','live_trade') NOT NULL,
  halted TINYINT(1) NOT NULL DEFAULT 0,
  halt_reason TEXT,
  last_heartbeat DATETIME(3) NOT NULL,
  context_json JSON
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS reconciliations (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  ts DATETIME(3) NOT NULL,
  has_diff TINYINT(1) NOT NULL,
  diff_json JSON,
  KEY ix_reconciliations_ts (ts)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO schema_meta(meta_key, meta_value) VALUES ('schema_version', '0001')
ON DUPLICATE KEY UPDATE meta_value=VALUES(meta_value);

INSERT INTO system_state(id, state, mode, halted, last_heartbeat, context_json)
VALUES (1, 'INIT', 'paper', 0, UTC_TIMESTAMP(3), JSON_OBJECT())
ON DUPLICATE KEY UPDATE id=id;

CREATE TABLE IF NOT EXISTS system_state_history (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  ts DATETIME(3) NOT NULL,
  state ENUM('INIT','SYNC_INSTRUMENTS','SYNC_MARKET_DATA','GENERATE_SIGNALS','WAIT_ENTRY_WINDOW','PLACE_ENTRY_ORDERS','MONITOR_ENTRY_ORDERS','HOLD_OVERNIGHT','WAIT_EXIT_WINDOW','PLACE_EXIT_ORDERS','MONITOR_EXIT_ORDERS','RECONCILE','REPORT','SLEEP','HALTED') NOT NULL,
  mode ENUM('backtest','paper','sandbox','live_readonly','live_trade') NOT NULL,
  halted TINYINT(1) NOT NULL DEFAULT 0,
  halt_reason TEXT,
  context_json JSON,
  KEY ix_system_state_history_ts (ts),
  KEY ix_system_state_history_mode_ts (mode, ts)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO system_state_history (ts, state, mode, halted, halt_reason, context_json)
SELECT last_heartbeat, state, mode, halted, halt_reason, context_json
FROM system_state
WHERE id=1
  AND NOT EXISTS (SELECT 1 FROM system_state_history);

UPDATE schema_meta SET meta_value='0010' WHERE meta_key='schema_version';

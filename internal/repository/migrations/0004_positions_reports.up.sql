ALTER TABLE positions ADD COLUMN exit_filled_lots BIGINT NOT NULL DEFAULT 0 AFTER lots;
ALTER TABLE positions ADD UNIQUE KEY ux_positions_trade (account_id_hash, instrument_uid, open_trade_date);

CREATE TABLE IF NOT EXISTS daily_reports (
  report_date DATE NOT NULL,
  account_id_hash VARCHAR(128) NOT NULL,
  sent_at DATETIME(3) NOT NULL,
  PRIMARY KEY (report_date, account_id_hash)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

UPDATE schema_meta SET meta_value='0004' WHERE meta_key='schema_version';

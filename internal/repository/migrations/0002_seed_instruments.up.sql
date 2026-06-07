INSERT INTO instruments (
  instrument_uid, ticker, class_code, name, lot, min_price_increment, currency,
  enabled, fund_type, expected_commission_bps_per_side, free_order_limit_per_day,
  quarantine, exclude_reason, updated_at
) VALUES
  ('PENDING:TRUR', 'TRUR', 'TQTF', 'TRUR', 1, 0.0001, 'RUB', 1, 'mixed', 0, 15, 0, NULL, UTC_TIMESTAMP(3)),
  ('PENDING:TGLD', 'TGLD', 'TQTF', 'TGLD', 1, 0.0001, 'RUB', 1, 'commodity', 0, 15, 0, NULL, UTC_TIMESTAMP(3)),
  ('PENDING:TBRU', 'TBRU', 'TQTF', 'TBRU', 1, 0.0001, 'RUB', 1, 'bonds', 0, 0, 0, NULL, UTC_TIMESTAMP(3)),
  ('PENDING:TDIV', 'TDIV', 'TQTF', 'TDIV', 1, 0.0001, 'RUB', 1, 'equity_income', 0, 0, 0, NULL, UTC_TIMESTAMP(3)),
  ('PENDING:TMON', 'TMON', 'TQTF', 'TMON', 1, 0.0001, 'RUB', 1, 'money_market', 0, 0, 0, NULL, UTC_TIMESTAMP(3)),
  ('PENDING:TOFZ', 'TOFZ', 'TQTF', 'TOFZ', 1, 0.0001, 'RUB', 1, 'bonds', 0, 0, 0, NULL, UTC_TIMESTAMP(3)),
  ('PENDING:TLCB', 'TLCB', 'TQTF', 'TLCB', 1, 0.0001, 'RUB', 1, 'corporate_bonds', 0, 0, 0, NULL, UTC_TIMESTAMP(3)),
  ('PENDING:TITR', 'TITR', 'TQTF', 'TITR', 1, 0.0001, 'RUB', 1, 'equity', 0, 0, 0, NULL, UTC_TIMESTAMP(3)),
  ('PENDING:TRND', 'TRND', 'TQTF', 'TRND', 1, 0.0001, 'RUB', 1, 'equity', 0, 0, 0, NULL, UTC_TIMESTAMP(3)),
  ('PENDING:TMOS', 'TMOS', 'TQTF', 'TMOS', 1, 0.0001, 'RUB', 0, 'equity', 0, 0, 0, 'Excluded by default due to possible non-zero sell-side fee', UTC_TIMESTAMP(3))
ON DUPLICATE KEY UPDATE
  enabled=VALUES(enabled),
  fund_type=VALUES(fund_type),
  expected_commission_bps_per_side=VALUES(expected_commission_bps_per_side),
  free_order_limit_per_day=VALUES(free_order_limit_per_day),
  exclude_reason=VALUES(exclude_reason),
  updated_at=UTC_TIMESTAMP(3);

UPDATE schema_meta SET meta_value='0002' WHERE meta_key='schema_version';

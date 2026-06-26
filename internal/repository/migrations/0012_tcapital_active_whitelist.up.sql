INSERT INTO instruments (
  instrument_uid, figi, ticker, class_code, name, lot, min_price_increment, currency,
  enabled, fund_type, expected_commission_bps_per_side, free_order_limit_per_day,
  quarantine, quarantine_reason, exclude_reason, updated_at
) VALUES
  ('e8acd2fb-6de6-4ea4-9bfb-0daad9b2ed7b', 'TCS60A1039N1', 'TBRU@', 'SPBRU', 'Российские облигации', 1, 0.01, 'RUB', 1, 'bonds', 0, 15, 0, NULL, NULL, UTC_TIMESTAMP(3)),
  ('d5cba263-cda7-440c-a21d-134fb5d334f6', 'TCS10A107563', 'TDIV@', 'SPBRU', 'Дивидендные акции', 1, 0.01, 'RUB', 1, 'equity', 0, 15, 0, NULL, NULL, UTC_TIMESTAMP(3)),
  ('de82be66-3b9b-4612-9572-61e3c6039013', 'TCS80A101X50', 'TGLD@', 'SPBRU', 'Золото', 1, 0.01, 'RUB', 1, 'commodity', 0, 15, 0, NULL, NULL, UTC_TIMESTAMP(3)),
  ('2f243f46-34ce-4d50-a931-c6f8a67eb758', 'TCS20A107597', 'TLCB@', 'SPBRU', 'Локальные валютные облигации', 1, 0.01, 'RUB', 1, 'bonds', 0, 15, 0, NULL, NULL, UTC_TIMESTAMP(3)),
  ('498ec3ff-ef27-4729-9703-a5aac48d5789', 'TCS70A106DL2', 'TMON@', 'SPBRU', 'Денежный рынок', 1, 0.01, 'RUB', 1, 'money_market', 0, 15, 0, NULL, NULL, UTC_TIMESTAMP(3)),
  ('f509af83-6e71-462f-901f-bcb073f6773b', 'TCS60A101X76', 'TMOS@', 'SPBRU', 'Крупнейшие компании РФ', 1, 0.01, 'RUB', 1, 'equity', 0, 15, 0, NULL, NULL, UTC_TIMESTAMP(3)),
  ('c5049184-ded4-49d0-8e14-bffefc40a223', 'TCS70A10A1L8', 'TOFZ@', 'SPBRU', 'Т-Капитал ОФЗ', 1, 0.01, 'RUB', 1, 'bonds', 0, 15, 0, NULL, NULL, UTC_TIMESTAMP(3)),
  ('4597c92e-128c-44de-abd2-a1d88d163b0c', 'TCSM25708WX3', 'TPAY', 'TQBR', 'Пассивный доход', 1, 0.01, 'RUB', 1, 'bonds', 0, 15, 0, NULL, NULL, UTC_TIMESTAMP(3)),
  ('5293ef3c-37bb-4d6f-8d43-802c57560881', 'TCS20A10B0G9', 'TRND@', 'SPBRU', 'Трендовые акции', 1, 0.01, 'RUB', 1, 'equity', 0, 15, 0, NULL, NULL, UTC_TIMESTAMP(3)),
  ('d16d8124-ce0c-4869-9efb-98700332feab', 'TCS60A1011U5', 'TRUR@', 'SPBRU', 'Вечный портфель', 1, 0.01, 'RUB', 1, 'mixed', 0, 15, 0, NULL, NULL, UTC_TIMESTAMP(3))
ON DUPLICATE KEY UPDATE
  figi=VALUES(figi),
  ticker=VALUES(ticker),
  class_code=VALUES(class_code),
  name=VALUES(name),
  lot=VALUES(lot),
  min_price_increment=VALUES(min_price_increment),
  currency=VALUES(currency),
  enabled=VALUES(enabled),
  fund_type=VALUES(fund_type),
  expected_commission_bps_per_side=VALUES(expected_commission_bps_per_side),
  free_order_limit_per_day=VALUES(free_order_limit_per_day),
  quarantine=VALUES(quarantine),
  quarantine_reason=VALUES(quarantine_reason),
  exclude_reason=VALUES(exclude_reason),
  updated_at=UTC_TIMESTAMP(3);

INSERT INTO risk_events (ts, severity, event_type, instrument_uid, message, raw_context_json)
VALUES (
  UTC_TIMESTAMP(3),
  'INFO',
  'tcapital_whitelist_seeded',
  NULL,
  'Seeded verified T-Capital ETF whitelist for monitoring',
  '{"tickers":["TBRU@","TDIV@","TGLD@","TLCB@","TMON@","TMOS@","TOFZ@","TPAY","TRND@","TRUR@"],"free_order_limit_per_day":15}'
);

UPDATE schema_meta SET meta_value='0012' WHERE meta_key='schema_version';

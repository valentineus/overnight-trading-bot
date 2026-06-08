ALTER TABLE instruments
  MODIFY free_order_limit_per_day INT NOT NULL DEFAULT 0 COMMENT '0 means no configured free-order cap';

UPDATE schema_meta SET meta_value='0007' WHERE meta_key='schema_version';

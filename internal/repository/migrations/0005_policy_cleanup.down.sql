ALTER TABLE risk_events
  MODIFY severity ENUM('INFO','WARN','ALERT','CRITICAL','REPORT') NOT NULL;

ALTER TABLE instruments
  MODIFY free_order_limit_per_day INT NOT NULL DEFAULT 0;

UPDATE schema_meta SET meta_value='0004' WHERE meta_key='schema_version';

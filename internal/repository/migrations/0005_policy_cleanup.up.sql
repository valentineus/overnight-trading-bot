UPDATE instruments
SET free_order_limit_per_day=0
WHERE ticker NOT IN ('TRUR', 'TGLD') AND free_order_limit_per_day=15;

ALTER TABLE instruments
  MODIFY free_order_limit_per_day INT NOT NULL DEFAULT 0 COMMENT '0 means no configured free-order cap';

UPDATE risk_events
SET
  severity='INFO',
  event_type=CASE
    WHEN event_type LIKE 'report_%' THEN event_type
    ELSE CONCAT('report_', event_type)
  END
WHERE severity='REPORT';

ALTER TABLE risk_events
  MODIFY severity ENUM('INFO','WARN','ALERT','CRITICAL') NOT NULL;

UPDATE schema_meta SET meta_value='0005' WHERE meta_key='schema_version';

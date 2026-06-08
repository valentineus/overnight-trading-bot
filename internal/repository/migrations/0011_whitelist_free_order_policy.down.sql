UPDATE instruments
SET free_order_limit_per_day=0
WHERE ticker IN ('TBRU', 'TDIV', 'TMON', 'TOFZ', 'TLCB', 'TITR', 'TRND')
  AND free_order_limit_per_day=-1;

UPDATE schema_meta SET meta_value='0010' WHERE meta_key='schema_version';

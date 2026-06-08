UPDATE instruments
SET free_order_limit_per_day=-1
WHERE ticker IN ('TBRU', 'TDIV', 'TMON', 'TOFZ', 'TLCB', 'TITR', 'TRND')
  AND free_order_limit_per_day=0;

UPDATE schema_meta SET meta_value='0011' WHERE meta_key='schema_version';

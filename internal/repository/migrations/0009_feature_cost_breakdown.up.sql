ALTER TABLE features
  ADD COLUMN cost_breakdown_json JSON AFTER expected_cost_bps;

UPDATE schema_meta SET meta_value='0009' WHERE meta_key='schema_version';

ALTER TABLE features
  DROP COLUMN cost_breakdown_json;

UPDATE schema_meta SET meta_value='0008' WHERE meta_key='schema_version';

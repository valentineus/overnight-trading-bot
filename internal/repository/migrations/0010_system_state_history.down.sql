DROP TABLE IF EXISTS system_state_history;

UPDATE schema_meta SET meta_value='0009' WHERE meta_key='schema_version';

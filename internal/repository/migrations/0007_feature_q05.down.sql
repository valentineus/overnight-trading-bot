ALTER TABLE features DROP COLUMN q05_on_60_abs;

UPDATE schema_meta SET meta_value='0006' WHERE meta_key='schema_version';

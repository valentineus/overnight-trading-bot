ALTER TABLE positions DROP COLUMN lot_size;

UPDATE schema_meta SET meta_value='0005' WHERE meta_key='schema_version';

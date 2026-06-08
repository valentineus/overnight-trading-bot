ALTER TABLE features
  ADD COLUMN q05_on_60_abs DECIMAL(20,10) NOT NULL DEFAULT 0 AFTER sigma_on_60;

UPDATE schema_meta SET meta_value='0007' WHERE meta_key='schema_version';

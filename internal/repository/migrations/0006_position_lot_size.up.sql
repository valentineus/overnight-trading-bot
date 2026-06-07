ALTER TABLE positions ADD COLUMN lot_size BIGINT NOT NULL DEFAULT 1 AFTER lots;

UPDATE positions p
JOIN instruments i ON i.instrument_uid = p.instrument_uid
SET p.lot_size = i.lot
WHERE p.lot_size = 1 AND i.lot > 1;

UPDATE schema_meta SET meta_value='0006' WHERE meta_key='schema_version';

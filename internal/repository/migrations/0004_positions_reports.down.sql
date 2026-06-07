DROP TABLE IF EXISTS daily_reports;
ALTER TABLE positions DROP INDEX ux_positions_trade;
ALTER TABLE positions DROP COLUMN exit_filled_lots;

UPDATE schema_meta SET meta_value='0003' WHERE meta_key='schema_version';

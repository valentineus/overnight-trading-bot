ALTER TABLE free_order_counters DROP FOREIGN KEY fk_free_orders_instrument;
ALTER TABLE free_order_counters ADD CONSTRAINT fk_free_orders_instrument
  FOREIGN KEY (instrument_uid) REFERENCES instruments(instrument_uid);

ALTER TABLE positions DROP FOREIGN KEY fk_positions_instrument;
ALTER TABLE positions ADD CONSTRAINT fk_positions_instrument
  FOREIGN KEY (instrument_uid) REFERENCES instruments(instrument_uid);

ALTER TABLE orders DROP FOREIGN KEY fk_orders_instrument;
ALTER TABLE orders ADD CONSTRAINT fk_orders_instrument
  FOREIGN KEY (instrument_uid) REFERENCES instruments(instrument_uid);

ALTER TABLE signals DROP FOREIGN KEY fk_signals_instrument;
ALTER TABLE signals ADD CONSTRAINT fk_signals_instrument
  FOREIGN KEY (instrument_uid) REFERENCES instruments(instrument_uid);

ALTER TABLE features DROP FOREIGN KEY fk_features_instrument;
ALTER TABLE features ADD CONSTRAINT fk_features_instrument
  FOREIGN KEY (instrument_uid) REFERENCES instruments(instrument_uid);

ALTER TABLE candles_minute DROP FOREIGN KEY fk_candles_minute_instrument;
ALTER TABLE candles_minute ADD CONSTRAINT fk_candles_minute_instrument
  FOREIGN KEY (instrument_uid) REFERENCES instruments(instrument_uid);

ALTER TABLE candles_daily DROP FOREIGN KEY fk_candles_daily_instrument;
ALTER TABLE candles_daily ADD CONSTRAINT fk_candles_daily_instrument
  FOREIGN KEY (instrument_uid) REFERENCES instruments(instrument_uid);

UPDATE schema_meta SET meta_value='0002' WHERE meta_key='schema_version';

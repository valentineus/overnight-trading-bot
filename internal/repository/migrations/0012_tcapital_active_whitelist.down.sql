UPDATE instruments
SET enabled=0,
    quarantine=1,
    quarantine_reason='rollback_tcapital_active_whitelist',
    exclude_reason='rollback_tcapital_active_whitelist',
    updated_at=UTC_TIMESTAMP(3)
WHERE instrument_uid IN (
  'e8acd2fb-6de6-4ea4-9bfb-0daad9b2ed7b',
  'd5cba263-cda7-440c-a21d-134fb5d334f6',
  'de82be66-3b9b-4612-9572-61e3c6039013',
  '2f243f46-34ce-4d50-a931-c6f8a67eb758',
  '498ec3ff-ef27-4729-9703-a5aac48d5789',
  'f509af83-6e71-462f-901f-bcb073f6773b',
  'c5049184-ded4-49d0-8e14-bffefc40a223',
  '4597c92e-128c-44de-abd2-a1d88d163b0c',
  '5293ef3c-37bb-4d6f-8d43-802c57560881',
  'd16d8124-ce0c-4869-9efb-98700332feab'
);

UPDATE schema_meta SET meta_value='0011' WHERE meta_key='schema_version';

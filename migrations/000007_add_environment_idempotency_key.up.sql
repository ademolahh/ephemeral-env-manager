ALTER TABLE environments ADD COLUMN idempotency_key text;

CREATE UNIQUE INDEX environments_caller_idempotency_key_idx ON environments (caller_id, idempotency_key);

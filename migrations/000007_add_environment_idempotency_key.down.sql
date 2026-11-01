DROP INDEX environments_caller_idempotency_key_idx;

ALTER TABLE environments DROP COLUMN idempotency_key;

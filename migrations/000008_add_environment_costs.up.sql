ALTER TABLE environments
    ADD COLUMN reserved_cost numeric NOT NULL DEFAULT 0 CHECK (reserved_cost >= 0),
    ADD COLUMN refunded_cost numeric NOT NULL DEFAULT 0 CHECK (refunded_cost >= 0),
    ADD CONSTRAINT environments_refund_within_reservation CHECK (refunded_cost <= reserved_cost);

ALTER TABLE environments
    DROP CONSTRAINT environments_refund_within_reservation,
    DROP COLUMN refunded_cost,
    DROP COLUMN reserved_cost;

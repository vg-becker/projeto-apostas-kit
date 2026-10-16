CREATE TABLE outbox_events (
    event_id        uuid        PRIMARY KEY,
    aggregate_id    text        NOT NULL,
    event_type      text        NOT NULL,
    payload         jsonb       NOT NULL,
    occurred_at     timestamptz NOT NULL,
    attempts        integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL,
    lease_until     timestamptz,
    published_at    timestamptz,
    CONSTRAINT outbox_events_type_known
        CHECK (event_type IN (
            'WagerTransactionProcessed',
            'WagerTransactionRejected',
            'WalletBalanceChanged',
            'WagerTransactionPendingReference'
        )),
    CONSTRAINT outbox_events_attempts_not_negative
        CHECK (attempts >= 0)
);

CREATE INDEX outbox_events_unpublished_idx
    ON outbox_events (next_attempt_at)
    WHERE published_at IS NULL;

CREATE FUNCTION outbox_events_guard_snapshot() RETURNS trigger
    LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.event_id IS DISTINCT FROM OLD.event_id
        OR NEW.aggregate_id IS DISTINCT FROM OLD.aggregate_id
        OR NEW.event_type IS DISTINCT FROM OLD.event_type
        OR NEW.payload IS DISTINCT FROM OLD.payload
        OR NEW.occurred_at IS DISTINCT FROM OLD.occurred_at THEN
        RAISE EXCEPTION 'outbox_events % snapshot is immutable', OLD.event_id
            USING ERRCODE = 'restrict_violation';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER outbox_events_guard_snapshot
    BEFORE UPDATE ON outbox_events
    FOR EACH ROW
    EXECUTE FUNCTION outbox_events_guard_snapshot();

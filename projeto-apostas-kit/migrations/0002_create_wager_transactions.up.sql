CREATE TABLE wager_transactions (
    id                             uuid        PRIMARY KEY,
    kind                           text        NOT NULL,
    status                         text        NOT NULL,
    wallet_id                      uuid        NOT NULL REFERENCES wallets (id),
    player_id                      uuid        NOT NULL,
    currency                       char(3)     NOT NULL,
    amount_minor_units             bigint      NOT NULL,
    provider_id                    text,
    external_transaction_id        text,
    idempotency_key                text,
    payload_hash                   char(64),
    round_id                       text,
    game_id                        text,
    reference_external_transaction_id text,
    resolved_reference_id          uuid        REFERENCES wager_transactions (id),
    failure_code                   text,
    result_balance_minor_units     bigint,
    reference_attempts             integer     NOT NULL DEFAULT 0,
    next_attempt_at                timestamptz,
    lease_until                    timestamptz,
    created_at                     timestamptz NOT NULL,
    updated_at                     timestamptz NOT NULL,

    CONSTRAINT wager_transactions_kind_known
        CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    CONSTRAINT wager_transactions_status_known
        CHECK (status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    CONSTRAINT wager_transactions_currency_known
        CHECK (currency IN ('BRL', 'USD', 'EUR')),
    CONSTRAINT wager_transactions_failure_code_known
        CHECK (failure_code IS NULL OR failure_code IN (
            'INSUFFICIENT_FUNDS',
            'REVERSAL_INSUFFICIENT_FUNDS',
            'REFERENCE_NOT_FOUND',
            'REFERENCE_NOT_PROCESSED',
            'REFERENCE_MISMATCH',
            'REFERENCE_KIND_INVALID',
            'AMOUNT_MISMATCH',
            'ALREADY_REVERSED',
            'CURRENCY_MISMATCH',
            'INFRASTRUCTURE_FAILURE'
        )),
    CONSTRAINT wager_transactions_payload_hash_format
        CHECK (payload_hash IS NULL OR payload_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT wager_transactions_attempts_not_negative
        CHECK (reference_attempts >= 0),
    CONSTRAINT wager_transactions_updated_not_before_created
        CHECK (updated_at >= created_at),

    -- OPENING é interna: nenhum metadado externo. As demais são externas: todos presentes.
    CONSTRAINT wager_transactions_origin
        CHECK (
            (kind = 'OPENING'
                AND provider_id IS NULL
                AND external_transaction_id IS NULL
                AND idempotency_key IS NULL
                AND payload_hash IS NULL
                AND round_id IS NULL
                AND game_id IS NULL
                AND reference_external_transaction_id IS NULL
                AND status = 'PROCESSED')
            OR
            (kind <> 'OPENING'
                AND provider_id IS NOT NULL
                AND external_transaction_id IS NOT NULL
                AND idempotency_key IS NOT NULL
                AND payload_hash IS NOT NULL
                AND round_id IS NOT NULL
                AND game_id IS NOT NULL)
        ),
    CONSTRAINT wager_transactions_amount_policy
        CHECK (
            (kind = 'LOSS' AND amount_minor_units = 0)
            OR (kind <> 'LOSS' AND amount_minor_units > 0)
        ),
    CONSTRAINT wager_transactions_reference_policy
        CHECK (
            (kind IN ('REFUND', 'ROLLBACK') AND reference_external_transaction_id IS NOT NULL)
            OR (kind = 'WIN')
            OR (kind IN ('BET', 'LOSS', 'OPENING') AND reference_external_transaction_id IS NULL)
        ),
    CONSTRAINT wager_transactions_no_self_reference
        CHECK (
            reference_external_transaction_id IS NULL
            OR reference_external_transaction_id <> external_transaction_id
        ),
    CONSTRAINT wager_transactions_resolved_reference_coherent
        CHECK (
            (resolved_reference_id IS NULL OR reference_external_transaction_id IS NOT NULL)
            AND (resolved_reference_id IS NULL OR resolved_reference_id <> id)
            AND (status <> 'PROCESSED'
                OR reference_external_transaction_id IS NULL
                OR resolved_reference_id IS NOT NULL)
        ),
    CONSTRAINT wager_transactions_state_coherent
        CHECK (
            (status IN ('PENDING', 'PENDING_REFERENCE')
                AND failure_code IS NULL
                AND result_balance_minor_units IS NULL)
            OR (status = 'PROCESSED'
                AND failure_code IS NULL
                AND result_balance_minor_units IS NOT NULL
                AND result_balance_minor_units >= 0)
            OR (status = 'REJECTED'
                AND failure_code IS NOT NULL
                AND failure_code <> 'INFRASTRUCTURE_FAILURE'
                AND result_balance_minor_units IS NULL)
            OR (status = 'FAILED'
                AND failure_code = 'INFRASTRUCTURE_FAILURE'
                AND result_balance_minor_units IS NULL)
        ),
    CONSTRAINT wager_transactions_pending_reference_needs_reference
        CHECK (status <> 'PENDING_REFERENCE' OR reference_external_transaction_id IS NOT NULL),

    CONSTRAINT wager_transactions_idempotency_key_unique
        UNIQUE (provider_id, idempotency_key),
    CONSTRAINT wager_transactions_external_id_unique
        UNIQUE (provider_id, external_transaction_id)
);

-- Impede crédito inicial duplicado: uma OPENING por carteira.
CREATE UNIQUE INDEX wager_transactions_one_opening_per_wallet
    ON wager_transactions (wallet_id)
    WHERE kind = 'OPENING';

-- Uma referência recebe no máximo uma reversão bem-sucedida, de qualquer tipo.
CREATE UNIQUE INDEX wager_transactions_one_reversal_per_reference
    ON wager_transactions (resolved_reference_id)
    WHERE kind IN ('REFUND', 'ROLLBACK') AND status = 'PROCESSED';

CREATE INDEX wager_transactions_wallet_idx
    ON wager_transactions (wallet_id);

CREATE INDEX wager_transactions_pending_idx
    ON wager_transactions (updated_at)
    WHERE status = 'PENDING';

CREATE INDEX wager_transactions_pending_reference_due_idx
    ON wager_transactions (next_attempt_at)
    WHERE status = 'PENDING_REFERENCE';

CREATE FUNCTION wager_transactions_guard_terminal() RETURNS trigger
    LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'wager_transactions is an audit table: delete of % is not allowed', OLD.id
            USING ERRCODE = 'restrict_violation';
    END IF;

    IF OLD.status IN ('PROCESSED', 'REJECTED', 'FAILED') THEN
        RAISE EXCEPTION 'wager_transactions % is % and cannot change', OLD.id, OLD.status
            USING ERRCODE = 'restrict_violation';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER wager_transactions_guard_terminal
    BEFORE UPDATE OR DELETE ON wager_transactions
    FOR EACH ROW
    EXECUTE FUNCTION wager_transactions_guard_terminal();

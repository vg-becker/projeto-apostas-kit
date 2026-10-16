CREATE TABLE wallet_ledger_entries (
    id                           uuid        PRIMARY KEY,
    wallet_id                    uuid        NOT NULL REFERENCES wallets (id),
    transaction_id               uuid        NOT NULL REFERENCES wager_transactions (id),
    direction                    text        NOT NULL,
    currency                     char(3)     NOT NULL,
    amount_minor_units           bigint      NOT NULL,
    balance_before_minor_units   bigint      NOT NULL,
    balance_after_minor_units    bigint      NOT NULL,
    created_at                   timestamptz NOT NULL,
    CONSTRAINT wallet_ledger_entries_direction_known
        CHECK (direction IN ('DEBIT', 'CREDIT')),
    CONSTRAINT wallet_ledger_entries_currency_known
        CHECK (currency IN ('BRL', 'USD', 'EUR')),
    CONSTRAINT wallet_ledger_entries_amount_positive
        CHECK (amount_minor_units > 0),
    CONSTRAINT wallet_ledger_entries_balances_not_negative
        CHECK (balance_before_minor_units >= 0 AND balance_after_minor_units >= 0),
    CONSTRAINT wallet_ledger_entries_arithmetic
        CHECK (
            (direction = 'CREDIT'
                AND balance_after_minor_units = balance_before_minor_units + amount_minor_units)
            OR (direction = 'DEBIT'
                AND balance_after_minor_units = balance_before_minor_units - amount_minor_units)
        ),
    CONSTRAINT wallet_ledger_entries_wallet_transaction_unique
        UNIQUE (wallet_id, transaction_id)
);

CREATE INDEX wallet_ledger_entries_wallet_order_idx
    ON wallet_ledger_entries (wallet_id, created_at, id);

CREATE FUNCTION wallet_ledger_entries_append_only() RETURNS trigger
    LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'wallet_ledger_entries is append-only: % is not allowed', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;

CREATE TRIGGER wallet_ledger_entries_no_update_delete
    BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW
    EXECUTE FUNCTION wallet_ledger_entries_append_only();

CREATE TRIGGER wallet_ledger_entries_no_truncate
    BEFORE TRUNCATE ON wallet_ledger_entries
    FOR EACH STATEMENT
    EXECUTE FUNCTION wallet_ledger_entries_append_only();

CREATE TABLE wallets (
    id                  uuid        PRIMARY KEY,
    player_id           uuid        NOT NULL,
    currency            char(3)     NOT NULL,
    balance_minor_units bigint      NOT NULL,
    version             bigint      NOT NULL,
    created_at          timestamptz NOT NULL,
    updated_at          timestamptz NOT NULL,
    CONSTRAINT wallets_currency_known
        CHECK (currency IN ('BRL', 'USD', 'EUR')),
    CONSTRAINT wallets_balance_not_negative
        CHECK (balance_minor_units >= 0),
    CONSTRAINT wallets_version_positive
        CHECK (version >= 1),
    CONSTRAINT wallets_updated_not_before_created
        CHECK (updated_at >= created_at),
    CONSTRAINT wallets_player_currency_unique
        UNIQUE (player_id, currency)
);

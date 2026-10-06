-- Core schema for the MMT wallet platform.
-- All monetary amounts are stored as BIGINT in minor units (cents).

CREATE TABLE customers (
    id                  BIGSERIAL PRIMARY KEY,
    msisdn              VARCHAR(15)  NOT NULL UNIQUE,
    full_name           VARCHAR(120) NOT NULL,
    national_id         VARCHAR(40)  NOT NULL UNIQUE,
    pin_hash            TEXT         NOT NULL,
    status              VARCHAR(16)  NOT NULL DEFAULT 'ACTIVE'
                        CHECK (status IN ('ACTIVE', 'LOCKED', 'SUSPENDED')),
    failed_pin_attempts INT          NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE wallets (
    id          BIGSERIAL PRIMARY KEY,
    account_no  VARCHAR(20)  NOT NULL UNIQUE,
    owner_type  VARCHAR(16)  NOT NULL
                CHECK (owner_type IN ('CUSTOMER', 'MERCHANT', 'AGENT', 'SYSTEM')),
    customer_id BIGINT REFERENCES customers (id),
    name        VARCHAR(120) NOT NULL,
    balance     BIGINT       NOT NULL DEFAULT 0,
    status      VARCHAR(16)  NOT NULL DEFAULT 'ACTIVE'
                CHECK (status IN ('ACTIVE', 'FROZEN', 'CLOSED')),
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    -- Only system wallets (e.g. treasury) may go negative: they represent issued e-money.
    CONSTRAINT wallets_balance_non_negative CHECK (owner_type = 'SYSTEM' OR balance >= 0)
);

CREATE UNIQUE INDEX wallets_customer_id_key ON wallets (customer_id) WHERE customer_id IS NOT NULL;

CREATE TABLE merchants (
    code       VARCHAR(10)  PRIMARY KEY,
    name       VARCHAR(120) NOT NULL,
    wallet_id  BIGINT       NOT NULL UNIQUE REFERENCES wallets (id),
    status     VARCHAR(16)  NOT NULL DEFAULT 'ACTIVE',
    created_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE agents (
    code       VARCHAR(10)  PRIMARY KEY,
    name       VARCHAR(120) NOT NULL,
    wallet_id  BIGINT       NOT NULL UNIQUE REFERENCES wallets (id),
    status     VARCHAR(16)  NOT NULL DEFAULT 'ACTIVE',
    created_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- Services drive the USSD main menu: enable/disable or reorder them here without a deploy.
CREATE TABLE services (
    code       VARCHAR(20) PRIMARY KEY,
    name       VARCHAR(40) NOT NULL,
    menu_order INT         NOT NULL,
    enabled    BOOLEAN     NOT NULL DEFAULT TRUE,
    fee_fixed  BIGINT      NOT NULL DEFAULT 0,
    fee_bps    BIGINT      NOT NULL DEFAULT 0,   -- percentage fee in basis points (100 = 1%)
    min_amount BIGINT      NOT NULL DEFAULT 0,
    max_amount BIGINT      NOT NULL DEFAULT 0    -- 0 = no limit
);

CREATE TABLE transactions (
    id               BIGSERIAL PRIMARY KEY,
    reference        VARCHAR(24) NOT NULL UNIQUE,
    type             VARCHAR(20) NOT NULL,
    channel          VARCHAR(10) NOT NULL,
    status           VARCHAR(12) NOT NULL,
    debit_wallet_id  BIGINT      NOT NULL REFERENCES wallets (id),
    credit_wallet_id BIGINT      NOT NULL REFERENCES wallets (id),
    amount           BIGINT      NOT NULL CHECK (amount > 0),
    fee              BIGINT      NOT NULL DEFAULT 0 CHECK (fee >= 0),
    description      TEXT        NOT NULL DEFAULT '',
    meta             JSONB       NOT NULL DEFAULT '{}',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX transactions_debit_wallet_idx  ON transactions (debit_wallet_id, created_at DESC);
CREATE INDEX transactions_credit_wallet_idx ON transactions (credit_wallet_id, created_at DESC);

-- Double-entry ledger: every transaction produces balanced DEBIT/CREDIT entries.
CREATE TABLE ledger_entries (
    id             BIGSERIAL PRIMARY KEY,
    transaction_id BIGINT      NOT NULL REFERENCES transactions (id),
    wallet_id      BIGINT      NOT NULL REFERENCES wallets (id),
    entry_type     VARCHAR(6)  NOT NULL CHECK (entry_type IN ('DEBIT', 'CREDIT')),
    amount         BIGINT      NOT NULL CHECK (amount > 0),
    balance_after  BIGINT      NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ledger_entries_wallet_idx ON ledger_entries (wallet_id, id DESC);
CREATE INDEX ledger_entries_transaction_idx ON ledger_entries (transaction_id);

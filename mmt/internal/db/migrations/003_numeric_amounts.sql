-- Store money as actual amounts (standard banking NUMERIC with 2 decimals)
-- instead of minor units: 1000 now means 1,000.00, not 10.00.
-- Existing values are converted from cents.

ALTER TABLE wallets
    ALTER COLUMN balance TYPE NUMERIC(18,2) USING balance / 100.0,
    ALTER COLUMN balance SET DEFAULT 0;

ALTER TABLE services
    ALTER COLUMN fee_fixed  TYPE NUMERIC(18,2) USING fee_fixed / 100.0,
    ALTER COLUMN min_amount TYPE NUMERIC(18,2) USING min_amount / 100.0,
    ALTER COLUMN max_amount TYPE NUMERIC(18,2) USING max_amount / 100.0;

ALTER TABLE transactions
    ALTER COLUMN amount TYPE NUMERIC(18,2) USING amount / 100.0,
    ALTER COLUMN fee    TYPE NUMERIC(18,2) USING fee / 100.0;

ALTER TABLE ledger_entries
    ALTER COLUMN amount        TYPE NUMERIC(18,2) USING amount / 100.0,
    ALTER COLUMN balance_after TYPE NUMERIC(18,2) USING balance_after / 100.0;

COMMENT ON COLUMN wallets.balance IS 'Actual balance in the wallet currency, e.g. 1000.00';
COMMENT ON COLUMN services.max_amount IS '0 = no limit';

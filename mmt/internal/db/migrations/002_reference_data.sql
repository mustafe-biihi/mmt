-- System wallets, services, sample merchants and agents.

INSERT INTO wallets (account_no, owner_type, name) VALUES
    ('SYS-TREASURY', 'SYSTEM', 'MMT Treasury'),
    ('SYS-FEES',     'SYSTEM', 'MMT Fee Income'),
    ('SYS-AIRTIME',  'SYSTEM', 'Airtime Settlement');

INSERT INTO services (code, name, menu_order, enabled, fee_fixed, fee_bps, min_amount, max_amount) VALUES
    ('P2P',        'Send Money',       1, TRUE, 0, 0,   100, 500000),
    ('CASHOUT',    'Cash Out',         2, TRUE, 0, 100, 100, 300000),
    ('AIRTIME',    'Airtime Recharge', 3, TRUE, 0, 0,   25,  10000),
    ('MERCHANT',   'Merchant Payment', 4, TRUE, 0, 0,   100, 1000000),
    ('BALANCE',    'Check Balance',    5, TRUE, 0, 0,   0,   0),
    ('STATEMENT',  'Mini Statement',   6, TRUE, 0, 0,   0,   0),
    ('CHANGE_PIN', 'Change PIN',       7, TRUE, 0, 0,   0,   0);

INSERT INTO wallets (account_no, owner_type, name) VALUES
    ('MER-1001', 'MERCHANT', 'Hayat Supermarket'),
    ('MER-1002', 'MERCHANT', 'City Pharmacy'),
    ('MER-1003', 'MERCHANT', 'Blue Nile Restaurant'),
    ('AGT-2001', 'AGENT',    'Bakaaro Agent'),
    ('AGT-2002', 'AGENT',    'KM4 Agent');

INSERT INTO merchants (code, name, wallet_id)
SELECT substring(account_no FROM 5), name, id FROM wallets WHERE owner_type = 'MERCHANT';

INSERT INTO agents (code, name, wallet_id)
SELECT substring(account_no FROM 5), name, id FROM wallets WHERE owner_type = 'AGENT';

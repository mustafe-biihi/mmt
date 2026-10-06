// Package store is the Postgres data-access layer for customers, wallets,
// services, merchants, agents and transaction history.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mustafe-biihi/mmt/mmt/internal/money"
)

var (
	ErrNotFound            = errors.New("not found")
	ErrDuplicateMSISDN     = errors.New("phone number already registered")
	ErrDuplicateNationalID = errors.New("national ID already registered")
)

const (
	StatusActive = "ACTIVE"
	StatusLocked = "LOCKED"

	OwnerCustomer = "CUSTOMER"
	OwnerSystem   = "SYSTEM"
)

type Customer struct {
	ID                int64     `json:"id"`
	MSISDN            string    `json:"msisdn"`
	FullName          string    `json:"fullName"`
	NationalID        string    `json:"nationalId"`
	PINHash           string    `json:"-"`
	Status            string    `json:"status"`
	FailedPINAttempts int       `json:"-"`
	CreatedAt         time.Time `json:"createdAt"`
}

type Wallet struct {
	ID         int64        `json:"id"`
	AccountNo  string       `json:"accountNo"`
	OwnerType  string       `json:"ownerType"`
	CustomerID *int64       `json:"-"`
	Name       string       `json:"name"`
	Balance    money.Amount `json:"balance"`
	Status     string       `json:"status"`
}

// Partner is a merchant or an agent: a named code that maps to a wallet.
type Partner struct {
	Code     string
	Name     string
	WalletID int64
	Status   string
}

type Service struct {
	Code      string       `json:"code"`
	Name      string       `json:"name"`
	MenuOrder int          `json:"menuOrder"`
	Enabled   bool         `json:"enabled"`
	FeeFixed  money.Amount `json:"feeFixed"`
	FeeBPS    int64        `json:"feeBps"`
	MinAmount money.Amount `json:"minAmount"`
	MaxAmount money.Amount `json:"maxAmount"`
}

// Fee returns the fee charged for the given amount.
func (s Service) Fee(amount money.Amount) money.Amount {
	return s.FeeFixed + amount.MulBPS(s.FeeBPS)
}

// StatementLine is a transaction as seen from one wallet's point of view.
type StatementLine struct {
	Reference    string       `json:"reference"`
	Type         string       `json:"type"`
	Channel      string       `json:"channel"`
	Direction    string       `json:"direction"` // DEBIT or CREDIT
	Amount       money.Amount `json:"amount"`
	Fee          money.Amount `json:"fee"`
	Counterparty string       `json:"counterparty"`
	Description  string       `json:"description"`
	CreatedAt    time.Time    `json:"createdAt"`
}

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const customerCols = `id, msisdn, full_name, national_id, pin_hash, status, failed_pin_attempts, created_at`

func scanCustomer(row pgx.Row) (Customer, error) {
	var c Customer
	err := row.Scan(&c.ID, &c.MSISDN, &c.FullName, &c.NationalID, &c.PINHash, &c.Status, &c.FailedPINAttempts, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// CreateCustomer inserts the customer and opens their wallet in a single transaction.
func (s *Store) CreateCustomer(ctx context.Context, c Customer) (Customer, Wallet, error) {
	var w Wallet
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return c, w, err
	}
	defer tx.Rollback(ctx)

	c, err = scanCustomer(tx.QueryRow(ctx,
		`INSERT INTO customers (msisdn, full_name, national_id, pin_hash)
		 VALUES ($1, $2, $3, $4) RETURNING `+customerCols,
		c.MSISDN, c.FullName, c.NationalID, c.PINHash))
	if err != nil {
		return c, w, mapUniqueViolation(err)
	}

	w, err = scanWallet(tx.QueryRow(ctx,
		`INSERT INTO wallets (account_no, owner_type, customer_id, name)
		 VALUES ($1, 'CUSTOMER', $2, $3) RETURNING `+walletCols,
		c.MSISDN, c.ID, c.FullName))
	if err != nil {
		return c, w, err
	}
	return c, w, tx.Commit(ctx)
}

func mapUniqueViolation(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		switch pgErr.ConstraintName {
		case "customers_msisdn_key":
			return ErrDuplicateMSISDN
		case "customers_national_id_key":
			return ErrDuplicateNationalID
		}
	}
	return err
}

func (s *Store) CustomerByMSISDN(ctx context.Context, msisdn string) (Customer, error) {
	return scanCustomer(s.pool.QueryRow(ctx, `SELECT `+customerCols+` FROM customers WHERE msisdn = $1`, msisdn))
}

func (s *Store) CustomerByID(ctx context.Context, id int64) (Customer, error) {
	return scanCustomer(s.pool.QueryRow(ctx, `SELECT `+customerCols+` FROM customers WHERE id = $1`, id))
}

// RecordPINFailure increments the failure counter and locks the customer once maxAttempts is reached.
func (s *Store) RecordPINFailure(ctx context.Context, id int64, maxAttempts int) (attempts int, status string, err error) {
	err = s.pool.QueryRow(ctx,
		`UPDATE customers
		    SET failed_pin_attempts = failed_pin_attempts + 1,
		        status = CASE WHEN failed_pin_attempts + 1 >= $2 THEN 'LOCKED' ELSE status END,
		        updated_at = now()
		  WHERE id = $1
		RETURNING failed_pin_attempts, status`, id, maxAttempts).Scan(&attempts, &status)
	return attempts, status, err
}

func (s *Store) ResetPINFailures(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE customers SET failed_pin_attempts = 0, updated_at = now() WHERE id = $1 AND failed_pin_attempts > 0`, id)
	return err
}

func (s *Store) UpdatePIN(ctx context.Context, id int64, pinHash string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE customers SET pin_hash = $2, failed_pin_attempts = 0, updated_at = now() WHERE id = $1`, id, pinHash)
	return err
}

const walletCols = `id, account_no, owner_type, customer_id, name, balance, status`

func scanWallet(row pgx.Row) (Wallet, error) {
	var w Wallet
	err := row.Scan(&w.ID, &w.AccountNo, &w.OwnerType, &w.CustomerID, &w.Name, &w.Balance, &w.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return w, ErrNotFound
	}
	return w, err
}

func (s *Store) WalletByCustomer(ctx context.Context, customerID int64) (Wallet, error) {
	return scanWallet(s.pool.QueryRow(ctx, `SELECT `+walletCols+` FROM wallets WHERE customer_id = $1`, customerID))
}

func (s *Store) WalletByAccountNo(ctx context.Context, accountNo string) (Wallet, error) {
	return scanWallet(s.pool.QueryRow(ctx, `SELECT `+walletCols+` FROM wallets WHERE account_no = $1`, accountNo))
}

func (s *Store) partner(ctx context.Context, table, code string) (Partner, error) {
	var p Partner
	err := s.pool.QueryRow(ctx,
		`SELECT code, name, wallet_id, status FROM `+table+` WHERE code = $1`, code).
		Scan(&p.Code, &p.Name, &p.WalletID, &p.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

func (s *Store) MerchantByCode(ctx context.Context, code string) (Partner, error) {
	return s.partner(ctx, "merchants", code)
}

func (s *Store) AgentByCode(ctx context.Context, code string) (Partner, error) {
	return s.partner(ctx, "agents", code)
}

const serviceCols = `code, name, menu_order, enabled, fee_fixed, fee_bps, min_amount, max_amount`

func scanService(row pgx.Row) (Service, error) {
	var sv Service
	err := row.Scan(&sv.Code, &sv.Name, &sv.MenuOrder, &sv.Enabled, &sv.FeeFixed, &sv.FeeBPS, &sv.MinAmount, &sv.MaxAmount)
	if errors.Is(err, pgx.ErrNoRows) {
		return sv, ErrNotFound
	}
	return sv, err
}

// EnabledServices returns the services shown on the USSD main menu, in menu order.
func (s *Store) EnabledServices(ctx context.Context) ([]Service, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+serviceCols+` FROM services WHERE enabled ORDER BY menu_order`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Service
	for rows.Next() {
		sv, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sv)
	}
	return out, rows.Err()
}

func (s *Store) ServiceByCode(ctx context.Context, code string) (Service, error) {
	return scanService(s.pool.QueryRow(ctx, `SELECT `+serviceCols+` FROM services WHERE code = $1`, code))
}

// Statement returns the most recent transactions touching the wallet.
func (s *Store) Statement(ctx context.Context, walletID int64, limit int) ([]StatementLine, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.reference, t.type, t.channel,
		       CASE WHEN t.debit_wallet_id = $1 THEN 'DEBIT' ELSE 'CREDIT' END,
		       t.amount,
		       CASE WHEN t.debit_wallet_id = $1 THEN t.fee ELSE 0 END,
		       CASE WHEN t.debit_wallet_id = $1 THEN cw.name ELSE dw.name END,
		       t.description, t.created_at
		  FROM transactions t
		  JOIN wallets dw ON dw.id = t.debit_wallet_id
		  JOIN wallets cw ON cw.id = t.credit_wallet_id
		 WHERE t.debit_wallet_id = $1 OR t.credit_wallet_id = $1
		 ORDER BY t.created_at DESC, t.id DESC
		 LIMIT $2`, walletID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StatementLine{}
	for rows.Next() {
		var l StatementLine
		if err := rows.Scan(&l.Reference, &l.Type, &l.Channel, &l.Direction, &l.Amount, &l.Fee,
			&l.Counterparty, &l.Description, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

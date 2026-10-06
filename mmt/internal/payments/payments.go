// Package payments is the MMT transaction engine. Every money movement is posted
// atomically with row-level locks and recorded in a double-entry ledger.
package payments

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mustafe-biihi/mmt/mmt/internal/money"
	"github.com/mustafe-biihi/mmt/mmt/internal/store"
)

// Transaction types.
const (
	TypeP2P      = "P2P"
	TypeCashOut  = "CASHOUT"
	TypeCashIn   = "CASHIN"
	TypeAirtime  = "AIRTIME"
	TypeMerchant = "MERCHANT"
)

// Channels.
const (
	ChannelUSSD  = "USSD"
	ChannelWeb   = "WEB"
	ChannelAdmin = "ADMIN"
)

// Error is a business-rule failure that is safe to show to the customer.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func newErr(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

var (
	ErrInsufficientFunds = newErr("INSUFFICIENT_FUNDS", "Insufficient balance")
	ErrRecipientNotFound = newErr("RECIPIENT_NOT_FOUND", "Recipient is not registered")
	ErrSelfTransfer      = newErr("SELF_TRANSFER", "You cannot send money to yourself")
	ErrRecipientInactive = newErr("RECIPIENT_INACTIVE", "Recipient account is not active")
	ErrMerchantNotFound  = newErr("MERCHANT_NOT_FOUND", "Invalid merchant code")
	ErrAgentNotFound     = newErr("AGENT_NOT_FOUND", "Invalid agent code")
	ErrServiceDisabled   = newErr("SERVICE_DISABLED", "Service is currently unavailable")
	ErrWalletInactive    = newErr("WALLET_INACTIVE", "Wallet is not active")
)

// Receipt describes a completed transaction from the payer's point of view.
type Receipt struct {
	Reference    string       `json:"reference"`
	Type         string       `json:"type"`
	Amount       money.Amount `json:"amount"`
	Fee          money.Amount `json:"fee"`
	Counterparty string       `json:"counterparty"`
	BalanceAfter money.Amount `json:"balanceAfter"`
	CreatedAt    time.Time    `json:"createdAt"`
}

type Service struct {
	pool     *pgxpool.Pool
	store    *store.Store
	currency string

	treasuryWallet int64
	feesWallet     int64
	airtimeWallet  int64
}

func New(ctx context.Context, pool *pgxpool.Pool, st *store.Store, currency string) (*Service, error) {
	s := &Service{pool: pool, store: st, currency: currency}
	for acct, dst := range map[string]*int64{
		"SYS-TREASURY": &s.treasuryWallet,
		"SYS-FEES":     &s.feesWallet,
		"SYS-AIRTIME":  &s.airtimeWallet,
	} {
		w, err := st.WalletByAccountNo(ctx, acct)
		if err != nil {
			return nil, fmt.Errorf("load system wallet %s: %w", acct, err)
		}
		*dst = w.ID
	}
	return s, nil
}

// Quote validates the service and amount limits and returns the fee.
func (s *Service) Quote(ctx context.Context, serviceCode string, amount money.Amount) (money.Amount, error) {
	sv, err := s.store.ServiceByCode(ctx, serviceCode)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !sv.Enabled) {
		return 0, ErrServiceDisabled
	}
	if err != nil {
		return 0, err
	}
	if amount <= 0 {
		return 0, newErr("INVALID_AMOUNT", "Invalid amount")
	}
	if sv.MinAmount > 0 && amount < sv.MinAmount {
		return 0, newErr("AMOUNT_TOO_LOW", "Minimum amount is %s", money.Format(sv.MinAmount, s.currency))
	}
	if sv.MaxAmount > 0 && amount > sv.MaxAmount {
		return 0, newErr("AMOUNT_TOO_HIGH", "Maximum amount is %s", money.Format(sv.MaxAmount, s.currency))
	}
	return sv.Fee(amount), nil
}

// ResolveRecipient finds an active customer who can receive a P2P transfer.
func (s *Service) ResolveRecipient(ctx context.Context, senderID int64, msisdn string) (store.Customer, error) {
	c, err := s.store.CustomerByMSISDN(ctx, msisdn)
	if errors.Is(err, store.ErrNotFound) {
		return c, ErrRecipientNotFound
	}
	if err != nil {
		return c, err
	}
	if c.ID == senderID {
		return c, ErrSelfTransfer
	}
	if c.Status != store.StatusActive {
		return c, ErrRecipientInactive
	}
	return c, nil
}

func (s *Service) ResolveMerchant(ctx context.Context, code string) (store.Partner, error) {
	p, err := s.store.MerchantByCode(ctx, code)
	if errors.Is(err, store.ErrNotFound) || (err == nil && p.Status != store.StatusActive) {
		return p, ErrMerchantNotFound
	}
	return p, err
}

func (s *Service) ResolveAgent(ctx context.Context, code string) (store.Partner, error) {
	p, err := s.store.AgentByCode(ctx, code)
	if errors.Is(err, store.ErrNotFound) || (err == nil && p.Status != store.StatusActive) {
		return p, ErrAgentNotFound
	}
	return p, err
}

// SendMoney transfers from one customer's wallet to another's (P2P).
func (s *Service) SendMoney(ctx context.Context, channel string, senderID int64, toMSISDN string, amount money.Amount) (Receipt, error) {
	recipient, err := s.ResolveRecipient(ctx, senderID, toMSISDN)
	if err != nil {
		return Receipt{}, err
	}
	to, err := s.store.WalletByCustomer(ctx, recipient.ID)
	if err != nil {
		return Receipt{}, err
	}
	return s.pay(ctx, payment{
		service: TypeP2P, channel: channel, customerID: senderID, toWallet: to.ID, amount: amount,
		description: "Sent to " + recipient.FullName + " (" + recipient.MSISDN + ")",
		meta:        map[string]any{"recipientMsisdn": recipient.MSISDN},
	})
}

// CashOut moves e-money from the customer to an agent who hands over physical cash.
func (s *Service) CashOut(ctx context.Context, channel string, customerID int64, agentCode string, amount money.Amount) (Receipt, error) {
	agent, err := s.ResolveAgent(ctx, agentCode)
	if err != nil {
		return Receipt{}, err
	}
	return s.pay(ctx, payment{
		service: TypeCashOut, channel: channel, customerID: customerID, toWallet: agent.WalletID, amount: amount,
		description: "Cash out at " + agent.Name + " (" + agent.Code + ")",
		meta:        map[string]any{"agentCode": agent.Code},
	})
}

// PayMerchant pays a merchant by merchant code.
func (s *Service) PayMerchant(ctx context.Context, channel string, customerID int64, merchantCode string, amount money.Amount) (Receipt, error) {
	m, err := s.ResolveMerchant(ctx, merchantCode)
	if err != nil {
		return Receipt{}, err
	}
	return s.pay(ctx, payment{
		service: TypeMerchant, channel: channel, customerID: customerID, toWallet: m.WalletID, amount: amount,
		description: "Payment to " + m.Name + " (" + m.Code + ")",
		meta:        map[string]any{"merchantCode": m.Code},
	})
}

// BuyAirtime debits the customer into the airtime settlement wallet.
// Integration with the telco top-up API would be triggered from here.
func (s *Service) BuyAirtime(ctx context.Context, channel string, customerID int64, targetMSISDN string, amount money.Amount) (Receipt, error) {
	r, err := s.pay(ctx, payment{
		service: TypeAirtime, channel: channel, customerID: customerID, toWallet: s.airtimeWallet, amount: amount,
		description: "Airtime for " + targetMSISDN,
		meta:        map[string]any{"targetMsisdn": targetMSISDN},
	})
	if err == nil {
		slog.Info("airtime purchased", "ref", r.Reference, "target", targetMSISDN, "amount", amount)
	}
	return r, err
}

// CashIn credits a customer from the treasury (deposit / float issuance). Admin only.
func (s *Service) CashIn(ctx context.Context, msisdn string, amount money.Amount) (Receipt, error) {
	c, err := s.store.CustomerByMSISDN(ctx, msisdn)
	if errors.Is(err, store.ErrNotFound) {
		return Receipt{}, ErrRecipientNotFound
	}
	if err != nil {
		return Receipt{}, err
	}
	w, err := s.store.WalletByCustomer(ctx, c.ID)
	if err != nil {
		return Receipt{}, err
	}
	if amount <= 0 {
		return Receipt{}, newErr("INVALID_AMOUNT", "Invalid amount")
	}
	r, err := s.post(ctx, posting{
		txType: TypeCashIn, channel: ChannelAdmin, from: s.treasuryWallet, to: w.ID, amount: amount,
		description: "Cash in", meta: map[string]any{},
	})
	if err != nil {
		return Receipt{}, err
	}
	// Report the customer's balance, not the treasury's.
	r.BalanceAfter = r.toBalance
	return r.Receipt, nil
}

type payment struct {
	service     string
	channel     string
	customerID  int64
	toWallet    int64
	amount      money.Amount
	description string
	meta        map[string]any
}

// pay runs the shared customer-initiated flow: quote, load wallet, post.
func (s *Service) pay(ctx context.Context, p payment) (Receipt, error) {
	fee, err := s.Quote(ctx, p.service, p.amount)
	if err != nil {
		return Receipt{}, err
	}
	from, err := s.store.WalletByCustomer(ctx, p.customerID)
	if err != nil {
		return Receipt{}, err
	}
	r, err := s.post(ctx, posting{
		txType: p.service, channel: p.channel, from: from.ID, to: p.toWallet,
		amount: p.amount, fee: fee, description: p.description, meta: p.meta,
	})
	return r.Receipt, err
}

type posting struct {
	txType, channel string
	from, to        int64
	amount, fee     money.Amount
	description     string
	meta            map[string]any
}

type postResult struct {
	Receipt
	toBalance money.Amount
}

type lockedWallet struct {
	ownerType, status, name string
	balance                 money.Amount
}

// post atomically moves amount from -> to and fee from -> fees wallet.
// Wallet rows are locked in ascending id order so concurrent postings cannot deadlock.
func (s *Service) post(ctx context.Context, p posting) (postResult, error) {
	var res postResult
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)

	ids := []int64{p.from, p.to}
	if p.fee > 0 {
		ids = append(ids, s.feesWallet)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)

	rows, err := tx.Query(ctx,
		`SELECT id, owner_type, status, name, balance FROM wallets WHERE id = ANY($1) ORDER BY id FOR UPDATE`, ids)
	if err != nil {
		return res, err
	}
	wallets := map[int64]*lockedWallet{}
	for rows.Next() {
		var id int64
		w := &lockedWallet{}
		if err := rows.Scan(&id, &w.ownerType, &w.status, &w.name, &w.balance); err != nil {
			rows.Close()
			return res, err
		}
		wallets[id] = w
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}

	from, to := wallets[p.from], wallets[p.to]
	if from == nil || to == nil {
		return res, fmt.Errorf("wallet not found (from=%d to=%d)", p.from, p.to)
	}
	if from.status != store.StatusActive || to.status != store.StatusActive {
		return res, ErrWalletInactive
	}
	total := p.amount + p.fee
	if from.ownerType != store.OwnerSystem && from.balance < total {
		return res, ErrInsufficientFunds
	}

	ref, err := newReference()
	if err != nil {
		return res, err
	}
	var txID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO transactions (reference, type, channel, status, debit_wallet_id, credit_wallet_id, amount, fee, description, meta)
		 VALUES ($1, $2, $3, 'COMPLETED', $4, $5, $6, $7, $8, $9)
		 RETURNING id, created_at`,
		ref, p.txType, p.channel, p.from, p.to, p.amount, p.fee, p.description, p.meta).Scan(&txID, &res.CreatedAt); err != nil {
		return res, err
	}

	type entry struct {
		wallet    int64
		entryType string
		amount    money.Amount
	}
	entries := []entry{{p.from, "DEBIT", p.amount}, {p.to, "CREDIT", p.amount}}
	if p.fee > 0 {
		entries = append(entries, entry{p.from, "DEBIT", p.fee}, entry{s.feesWallet, "CREDIT", p.fee})
	}
	for _, e := range entries {
		w := wallets[e.wallet]
		if e.entryType == "DEBIT" {
			w.balance -= e.amount
		} else {
			w.balance += e.amount
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO ledger_entries (transaction_id, wallet_id, entry_type, amount, balance_after)
			 VALUES ($1, $2, $3, $4, $5)`, txID, e.wallet, e.entryType, e.amount, w.balance); err != nil {
			return res, err
		}
	}
	for _, id := range ids {
		if _, err := tx.Exec(ctx,
			`UPDATE wallets SET balance = $2, updated_at = now() WHERE id = $1`, id, wallets[id].balance); err != nil {
			return res, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return res, err
	}

	res.Receipt = Receipt{
		Reference: ref, Type: p.txType, Amount: p.amount, Fee: p.fee,
		Counterparty: to.name, BalanceAfter: from.balance, CreatedAt: res.CreatedAt,
	}
	res.toBalance = to.balance
	slog.Info("transaction posted", "ref", ref, "type", p.txType, "channel", p.channel,
		"from", p.from, "to", p.to, "amount", p.amount, "fee", p.fee)
	return res, nil
}

// newReference returns e.g. "MP2610051A2B3C" (date + 6 random base32 chars).
func newReference() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return "MP" + time.Now().UTC().Format("060102") + string(b), nil
}

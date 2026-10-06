// Package ussd implements the *836# USSD menu as a Redis-backed state machine.
//
// The request/response contract follows the common aggregator convention
// (e.g. Africa's Talking): the gateway posts sessionId, serviceCode, phoneNumber
// and text (all inputs so far joined by "*"); the reply starts with "CON " to
// keep the session open or "END " to close it.
package ussd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/mustafe-biihi/mmt/mmt/internal/auth"
	"github.com/mustafe-biihi/mmt/mmt/internal/money"
	"github.com/mustafe-biihi/mmt/mmt/internal/payments"
	"github.com/mustafe-biihi/mmt/mmt/internal/phone"
	"github.com/mustafe-biihi/mmt/mmt/internal/store"
)

// Service codes as configured in the services table.
const (
	svcP2P       = "P2P"
	svcCashOut   = "CASHOUT"
	svcAirtime   = "AIRTIME"
	svcMerchant  = "MERCHANT"
	svcBalance   = "BALANCE"
	svcStatement = "STATEMENT"
	svcChangePIN = "CHANGE_PIN"
)

const (
	msgLocked        = "Your account is locked due to too many wrong PIN attempts. Please contact customer care."
	msgInactive      = "Your account is not active. Please contact customer care."
	msgUnavailable   = "Service temporarily unavailable. Please try again later."
	msgActiveSession = "You already have an active MMT session. Please complete it or try again in 30 seconds."
)

type Request struct {
	SessionID   string `json:"sessionId"`
	ServiceCode string `json:"serviceCode"`
	PhoneNumber string `json:"phoneNumber"`
	Text        string `json:"text"`
}

type Response struct {
	End     bool
	Message string
}

func (r Response) String() string {
	if r.End {
		return "END " + r.Message
	}
	return "CON " + r.Message
}

func con(format string, args ...any) Response { return Response{Message: fmt.Sprintf(format, args...)} }
func end(format string, args ...any) Response {
	return Response{End: true, Message: fmt.Sprintf(format, args...)}
}

type Handler struct {
	store       *store.Store
	auth        *auth.Service
	pay         *payments.Service
	sessions    sessionStore
	serviceCode string
	currency    string
	countryCode string
}

func NewHandler(st *store.Store, au *auth.Service, pay *payments.Service, rdb *redis.Client,
	serviceCode, currency, countryCode string, sessionTTL time.Duration) *Handler {
	return &Handler{
		store: st, auth: au, pay: pay,
		sessions:    sessionStore{rdb: rdb, ttl: sessionTTL},
		serviceCode: serviceCode, currency: currency, countryCode: countryCode,
	}
}

// Handle processes one USSD hop and returns the next screen.
func (h *Handler) Handle(ctx context.Context, req Request) Response {
	if req.SessionID == "" {
		return end("Invalid request.")
	}
	if req.ServiceCode != "" && req.ServiceCode != h.serviceCode {
		return end("Unknown service code.")
	}
	msisdn, err := phone.Normalize(req.PhoneNumber, h.countryCode)
	if err != nil {
		return end("Invalid phone number.")
	}

	sess, err := h.sessions.load(ctx, req.SessionID)
	var resp Response
	switch {
	case errors.Is(err, errNoSession):
		if req.Text != "" {
			return end("Session expired. Dial %s to start again.", h.serviceCode)
		}
		sess, resp, err = h.start(ctx, req.SessionID, msisdn)
	case err != nil:
	case sess.MSISDN != msisdn:
		return end("Invalid session.")
	default:
		resp, err = h.step(ctx, sess, lastInput(req.Text))
	}
	if err != nil {
		h.sessions.delete(ctx, req.SessionID, sess)
		slog.Error("ussd error", "session", req.SessionID, "msisdn", msisdn, "err", err)
		return end(msgUnavailable)
	}

	if resp.End || sess == nil {
		err = h.sessions.delete(ctx, req.SessionID, sess)
	} else {
		err = h.sessions.save(ctx, req.SessionID, sess)
	}
	if err != nil {
		slog.Error("ussd session persist", "session", req.SessionID, "err", err)
		return end(msgUnavailable)
	}
	return resp
}

// lastInput extracts the newest user entry from the accumulated "a*b*c" text.
func lastInput(text string) string {
	return strings.TrimSpace(text[strings.LastIndex(text, "*")+1:])
}

func (h *Handler) start(ctx context.Context, sessionID, msisdn string) (*session, Response, error) {
	c, err := h.store.CustomerByMSISDN(ctx, msisdn)
	if errors.Is(err, store.ErrNotFound) {
		return nil, end("You are not registered for MMT.\nPlease register on the MMT web portal."), nil
	}
	if err != nil {
		return nil, Response{}, err
	}
	switch c.Status {
	case store.StatusActive:
	case store.StatusLocked:
		return nil, end(msgLocked), nil
	default:
		return nil, end(msgInactive), nil
	}
	// One USSD session per customer: refuse a second dial while one is still open.
	claimed, err := h.sessions.claim(ctx, c.ID, sessionID)
	if err != nil {
		return nil, Response{}, err
	}
	if !claimed {
		return nil, end(msgActiveSession), nil
	}
	first := strings.Fields(c.FullName)[0]
	s := &session{MSISDN: msisdn, CustomerID: c.ID, FirstName: first, Step: stepPIN}
	return s, con("Welcome to MMT, %s\nEnter your PIN:", first), nil
}

func (h *Handler) step(ctx context.Context, s *session, in string) (Response, error) {
	switch s.Step {
	case stepPIN:
		return h.onPIN(ctx, s, in)
	case stepMenu:
		return h.onMenu(ctx, s, in)
	case stepAirtimeChoice:
		return h.onAirtimeChoice(ctx, s, in)
	case stepTarget:
		return h.onTarget(ctx, s, in)
	case stepAmount:
		return h.onAmount(ctx, s, in)
	case stepConfirm:
		return h.onConfirm(ctx, s, in)
	case stepOldPIN, stepNewPIN, stepConfirmPIN:
		return h.onChangePIN(ctx, s, in)
	}
	return Response{}, fmt.Errorf("unknown ussd step %q", s.Step)
}

// verifyPIN returns (response, true) when the PIN check failed and the caller should stop.
func (h *Handler) verifyPIN(ctx context.Context, s *session, pin, prompt string) (Response, bool, error) {
	c, err := h.store.CustomerByID(ctx, s.CustomerID)
	if err != nil {
		return Response{}, true, err
	}
	err = h.auth.VerifyPIN(ctx, c, pin)
	var wrong *auth.WrongPINError
	switch {
	case err == nil:
		return Response{}, false, nil
	case errors.As(err, &wrong):
		return con("Wrong PIN. %d attempt(s) left.\n%s", wrong.Remaining, prompt), true, nil
	case errors.Is(err, auth.ErrAccountLocked):
		return end(msgLocked), true, nil
	case errors.Is(err, auth.ErrAccountInactive):
		return end(msgInactive), true, nil
	}
	return Response{}, true, err
}

func (h *Handler) onPIN(ctx context.Context, s *session, in string) (Response, error) {
	if resp, stop, err := h.verifyPIN(ctx, s, in, "Enter your PIN:"); stop {
		return resp, err
	}
	return h.mainMenu(ctx, s, "")
}

func (h *Handler) mainMenu(ctx context.Context, s *session, notice string) (Response, error) {
	services, err := h.store.EnabledServices(ctx)
	if err != nil {
		return Response{}, err
	}
	s.resetFlow()
	s.Step = stepMenu
	s.Menu = s.Menu[:0]

	var b strings.Builder
	if notice != "" {
		b.WriteString(notice + "\n")
	}
	b.WriteString("MMT Services\n")
	for i, sv := range services {
		s.Menu = append(s.Menu, sv.Code)
		fmt.Fprintf(&b, "%d. %s\n", i+1, sv.Name)
	}
	b.WriteString("0. Exit")
	return con("%s", b.String()), nil
}

func (h *Handler) onMenu(ctx context.Context, s *session, in string) (Response, error) {
	if in == "0" {
		return end("Thank you for using MMT."), nil
	}
	n, err := strconv.Atoi(in)
	if err != nil || n < 1 || n > len(s.Menu) {
		return h.mainMenu(ctx, s, "Invalid choice.")
	}
	s.Service = s.Menu[n-1]

	switch s.Service {
	case svcP2P, svcCashOut, svcMerchant:
		s.Step = stepTarget
		return con("%s\n0. Back", targetPrompt(s.Service)), nil
	case svcAirtime:
		s.Step = stepAirtimeChoice
		return con("Buy airtime for:\n1. My number\n2. Other number\n0. Back"), nil
	case svcBalance:
		w, err := h.store.WalletByCustomer(ctx, s.CustomerID)
		if err != nil {
			return Response{}, err
		}
		return end("Your MMT balance is %s.", money.Format(w.Balance, h.currency)), nil
	case svcStatement:
		return h.miniStatement(ctx, s)
	case svcChangePIN:
		s.Step = stepOldPIN
		return con("Enter your current PIN:"), nil
	}
	return h.mainMenu(ctx, s, "Service not available.")
}

func targetPrompt(service string) string {
	switch service {
	case svcP2P:
		return "Enter recipient phone number:"
	case svcCashOut:
		return "Enter agent code:"
	case svcMerchant:
		return "Enter merchant code:"
	default:
		return "Enter phone number to recharge:"
	}
}

func (h *Handler) amountPrompt() string {
	return fmt.Sprintf("Enter amount (%s):\n0. Back", h.currency)
}

func (h *Handler) onAirtimeChoice(ctx context.Context, s *session, in string) (Response, error) {
	switch in {
	case "0":
		return h.mainMenu(ctx, s, "")
	case "1":
		s.Target, s.TargetName = s.MSISDN, s.MSISDN
		s.Step = stepAmount
		return con("%s", h.amountPrompt()), nil
	case "2":
		s.Step = stepTarget
		return con("%s\n0. Back", targetPrompt(svcAirtime)), nil
	}
	return con("Invalid choice.\nBuy airtime for:\n1. My number\n2. Other number\n0. Back"), nil
}

// retry re-prompts with a customer-facing message for business-rule errors,
// and propagates anything else as an internal error.
func retry(err error, prompt string) (Response, error) {
	var pe *payments.Error
	if errors.As(err, &pe) {
		return con("%s.\n%s", pe.Message, prompt), nil
	}
	return Response{}, err
}

func (h *Handler) onTarget(ctx context.Context, s *session, in string) (Response, error) {
	if in == "0" {
		return h.mainMenu(ctx, s, "")
	}
	prompt := targetPrompt(s.Service) + "\n0. Back"

	switch s.Service {
	case svcP2P, svcAirtime:
		msisdn, err := phone.Normalize(in, h.countryCode)
		if err != nil {
			return con("Invalid phone number.\n%s", prompt), nil
		}
		s.Target, s.TargetName = msisdn, msisdn
		if s.Service == svcP2P {
			c, err := h.pay.ResolveRecipient(ctx, s.CustomerID, msisdn)
			if err != nil {
				return retry(err, prompt)
			}
			s.TargetName = c.FullName
		}
	case svcCashOut, svcMerchant:
		resolve := h.pay.ResolveAgent
		if s.Service == svcMerchant {
			resolve = h.pay.ResolveMerchant
		}
		p, err := resolve(ctx, in)
		if err != nil {
			return retry(err, prompt)
		}
		s.Target, s.TargetName = p.Code, p.Name
	}
	s.Step = stepAmount
	return con("%s", h.amountPrompt()), nil
}

func (h *Handler) onAmount(ctx context.Context, s *session, in string) (Response, error) {
	if in == "0" {
		return h.mainMenu(ctx, s, "")
	}
	amount, err := money.Parse(in)
	if err != nil {
		return con("Invalid amount.\n%s", h.amountPrompt()), nil
	}
	fee, err := h.pay.Quote(ctx, s.Service, amount)
	if err != nil {
		return retry(err, h.amountPrompt())
	}
	s.Amount, s.Fee = amount, fee
	s.Step = stepConfirm
	return con("%s", h.confirmPrompt(s)), nil
}

// confirmPrompt asks for the PIN to authorise the transaction.
func (h *Handler) confirmPrompt(s *session) string {
	return h.summary(s) + "\nEnter PIN to confirm:\n0. Cancel"
}

func (h *Handler) summary(s *session) string {
	amt := money.Format(s.Amount, h.currency)
	var line string
	switch s.Service {
	case svcP2P:
		line = fmt.Sprintf("Send %s to %s (%s)", amt, s.TargetName, s.Target)
	case svcCashOut:
		line = fmt.Sprintf("Withdraw %s at %s (agent %s)", amt, s.TargetName, s.Target)
	case svcMerchant:
		line = fmt.Sprintf("Pay %s to %s (merchant %s)", amt, s.TargetName, s.Target)
	case svcAirtime:
		line = fmt.Sprintf("Buy %s airtime for %s", amt, s.Target)
	}
	if s.Fee > 0 {
		line += fmt.Sprintf("\nFee: %s\nTotal: %s",
			money.Format(s.Fee, h.currency), money.Format(s.Amount+s.Fee, h.currency))
	}
	return line
}

func (h *Handler) onConfirm(ctx context.Context, s *session, in string) (Response, error) {
	if in == "0" {
		return end("Transaction cancelled."), nil
	}
	// Every money movement must be authorised with the PIN. Wrong entries count
	// towards the same lockout as login.
	if resp, stop, err := h.verifyPIN(ctx, s, in, h.confirmPrompt(s)); stop {
		return resp, err
	}

	var (
		r    payments.Receipt
		err  error
		done string
		amt  = money.Format(s.Amount, h.currency)
	)
	switch s.Service {
	case svcP2P:
		r, err = h.pay.SendMoney(ctx, payments.ChannelUSSD, s.CustomerID, s.Target, s.Amount)
		done = fmt.Sprintf("You sent %s to %s.", amt, s.TargetName)
	case svcCashOut:
		r, err = h.pay.CashOut(ctx, payments.ChannelUSSD, s.CustomerID, s.Target, s.Amount)
		done = fmt.Sprintf("You withdrew %s at %s.", amt, s.TargetName)
	case svcMerchant:
		r, err = h.pay.PayMerchant(ctx, payments.ChannelUSSD, s.CustomerID, s.Target, s.Amount)
		done = fmt.Sprintf("You paid %s to %s.", amt, s.TargetName)
	case svcAirtime:
		r, err = h.pay.BuyAirtime(ctx, payments.ChannelUSSD, s.CustomerID, s.Target, s.Amount)
		done = fmt.Sprintf("Airtime of %s sent to %s.", amt, s.Target)
	default:
		return Response{}, fmt.Errorf("confirm for unknown service %q", s.Service)
	}

	var pe *payments.Error
	if errors.As(err, &pe) {
		return end("Transaction failed: %s.", pe.Message), nil
	}
	if err != nil {
		return Response{}, err
	}
	msg := fmt.Sprintf("%s\nRef: %s\n", done, r.Reference)
	if r.Fee > 0 {
		msg += fmt.Sprintf("Fee: %s\n", money.Format(r.Fee, h.currency))
	}
	msg += "New balance: " + money.Format(r.BalanceAfter, h.currency)
	return end("%s", msg), nil
}

func (h *Handler) miniStatement(ctx context.Context, s *session) (Response, error) {
	w, err := h.store.WalletByCustomer(ctx, s.CustomerID)
	if err != nil {
		return Response{}, err
	}
	lines, err := h.store.Statement(ctx, w.ID, 5)
	if err != nil {
		return Response{}, err
	}
	if len(lines) == 0 {
		return end("No transactions yet.\nBalance: %s", money.Format(w.Balance, h.currency)), nil
	}
	var b strings.Builder
	b.WriteString("Mini Statement\n")
	for _, l := range lines {
		sign := "+"
		amount := l.Amount
		if l.Direction == "DEBIT" {
			sign = "-"
			amount += l.Fee
		}
		name := l.Counterparty
		if len(name) > 14 {
			name = name[:14]
		}
		fmt.Fprintf(&b, "%s %s%s %s\n", l.CreatedAt.Format("02/01"), sign, amount.Grouped(), name)
	}
	b.WriteString("Bal: " + money.Format(w.Balance, h.currency))
	return end("%s", b.String()), nil
}

func (h *Handler) onChangePIN(ctx context.Context, s *session, in string) (Response, error) {
	const newPrompt = "Enter new 4-digit PIN:"
	switch s.Step {
	case stepOldPIN:
		if resp, stop, err := h.verifyPIN(ctx, s, in, "Enter your current PIN:"); stop {
			return resp, err
		}
		s.Step = stepNewPIN
		return con(newPrompt), nil

	case stepNewPIN:
		if auth.ValidatePIN(in) != nil {
			return con("PIN must be exactly 4 digits.\n%s", newPrompt), nil
		}
		c, err := h.store.CustomerByID(ctx, s.CustomerID)
		if err != nil {
			return Response{}, err
		}
		if auth.MatchesHash(c.PINHash, in) {
			return con("New PIN must be different from current PIN.\n%s", newPrompt), nil
		}
		// Keep only a hash of the pending PIN in Redis.
		if s.NewPINHash, err = auth.HashPIN(in); err != nil {
			return Response{}, err
		}
		s.Step = stepConfirmPIN
		return con("Confirm new PIN:"), nil

	default: // stepConfirmPIN
		if !auth.MatchesHash(s.NewPINHash, in) {
			return end("PINs do not match. Your PIN was not changed."), nil
		}
		if err := h.auth.ChangePIN(ctx, s.CustomerID, in); err != nil {
			return Response{}, err
		}
		return end("Your PIN has been changed successfully."), nil
	}
}

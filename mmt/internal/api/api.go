// Package api exposes the MMT HTTP endpoints consumed through the API gateway.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/mustafe-biihi/mmt/mmt/internal/auth"
	"github.com/mustafe-biihi/mmt/mmt/internal/money"
	"github.com/mustafe-biihi/mmt/mmt/internal/payments"
	"github.com/mustafe-biihi/mmt/mmt/internal/phone"
	"github.com/mustafe-biihi/mmt/mmt/internal/store"
	"github.com/mustafe-biihi/mmt/mmt/internal/ussd"
)

type API struct {
	Pool        *pgxpool.Pool
	Redis       *redis.Client
	Store       *store.Store
	Auth        *auth.Service
	Payments    *payments.Service
	USSD        *ussd.Handler
	GatewayKey  string
	AdminKey    string
	Currency    string
	CountryCode string
}

type ctxKey struct{}

func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.health)

	mux.HandleFunc("POST /api/auth/register", a.register)
	mux.HandleFunc("POST /api/auth/login", a.login)
	mux.Handle("POST /api/auth/logout", a.requireSession(a.logout))
	mux.Handle("GET /api/me", a.requireSession(a.me))
	mux.Handle("GET /api/transactions", a.requireSession(a.transactions))
	mux.HandleFunc("GET /api/services", a.services)
	mux.Handle("POST /api/admin/cash-in", a.requireAdmin(a.cashIn))

	mux.HandleFunc("POST /ussd", a.ussd)

	return recoverer(logRequests(a.requireGateway(mux)))
}

// ---- middleware ----

// requireGateway rejects any request that did not come through the API gateway.
func (a *API) requireGateway(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" && !secureEqual(r.Header.Get("X-Gateway-Key"), a.GatewayKey) {
			writeError(w, http.StatusForbidden, "FORBIDDEN", "Direct access is not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) requireSession(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := a.Auth.Authenticate(r.Context(), bearerToken(r))
		if errors.Is(err, auth.ErrInvalidSession) {
			writeError(w, http.StatusUnauthorized, "SESSION_EXPIRED", "Please log in again")
			return
		}
		if err != nil {
			internalError(w, r, err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}

func (a *API) requireAdmin(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !secureEqual(r.Header.Get("X-Admin-Key"), a.AdminKey) {
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid admin key")
			return
		}
		next(w, r)
	})
}

func customerID(r *http.Request) int64 { return r.Context().Value(ctxKey{}).(int64) }

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if t, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	return ""
}

func secureEqual(a, b string) bool {
	return b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/healthz" {
			return
		}
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(), "request_id", r.Header.Get("X-Request-ID"))
	})
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				slog.Error("panic", "err", v, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "INTERNAL", "Internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func internalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("internal error", "path", r.URL.Path, "request_id", r.Header.Get("X-Request-ID"), "err", err)
	writeError(w, http.StatusInternalServerError, "INTERNAL", "Internal server error")
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Invalid JSON body")
		return false
	}
	return true
}

// ---- handlers ----

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	status := map[string]string{"postgres": "ok", "redis": "ok"}
	code := http.StatusOK
	if err := a.Pool.Ping(ctx); err != nil {
		status["postgres"], code = err.Error(), http.StatusServiceUnavailable
	}
	if err := a.Redis.Ping(ctx).Err(); err != nil {
		status["redis"], code = err.Error(), http.StatusServiceUnavailable
	}
	writeJSON(w, code, status)
}

type walletView struct {
	AccountNo string       `json:"accountNo"`
	Balance   money.Amount `json:"balance"`
	Display   string       `json:"balanceDisplay"`
	Currency  string       `json:"currency"`
	Status    string       `json:"status"`
}

func (a *API) walletView(wl store.Wallet) walletView {
	return walletView{wl.AccountNo, wl.Balance, money.Format(wl.Balance, a.Currency), a.Currency, wl.Status}
}

func (a *API) register(w http.ResponseWriter, r *http.Request) {
	var in auth.RegisterInput
	if !decode(w, r, &in) {
		return
	}
	c, wl, err := a.Auth.Register(r.Context(), in)
	var ve *auth.ValidationError
	if errors.As(err, &ve) {
		writeError(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", ve.Message)
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	slog.Info("customer registered", "customer_id", c.ID, "msisdn", c.MSISDN)
	writeJSON(w, http.StatusCreated, map[string]any{"customer": c, "wallet": a.walletView(wl)})
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MSISDN string `json:"msisdn"`
		PIN    string `json:"pin"`
	}
	if !decode(w, r, &in) {
		return
	}
	token, c, err := a.Auth.Login(r.Context(), in.MSISDN, in.PIN)
	var wrong *auth.WrongPINError
	switch {
	case err == nil:
	case errors.As(err, &wrong):
		writeError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Invalid phone number or PIN. "+
			strconv.Itoa(wrong.Remaining)+" attempt(s) left.")
		return
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Invalid phone number or PIN.")
		return
	case errors.Is(err, auth.ErrAccountLocked):
		writeError(w, http.StatusLocked, "ACCOUNT_LOCKED", "Your account is locked. Please contact customer care.")
		return
	case errors.Is(err, auth.ErrAccountInactive):
		writeError(w, http.StatusForbidden, "ACCOUNT_INACTIVE", "Your account is not active.")
		return
	case errors.Is(err, auth.ErrSessionActive):
		writeError(w, http.StatusConflict, "SESSION_ACTIVE",
			"You are already logged in on another browser or device. Log out there first, or wait for that session to expire.")
		return
	default:
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":     token,
		"expiresIn": int(a.Auth.SessionTTL().Seconds()),
		"customer":  c,
	})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if err := a.Auth.Logout(r.Context(), bearerToken(r)); err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	c, err := a.Store.CustomerByID(r.Context(), customerID(r))
	if err != nil {
		internalError(w, r, err)
		return
	}
	wl, err := a.Store.WalletByCustomer(r.Context(), c.ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"customer": c, "wallet": a.walletView(wl)})
}

func (a *API) transactions(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	wl, err := a.Store.WalletByCustomer(r.Context(), customerID(r))
	if err != nil {
		internalError(w, r, err)
		return
	}
	lines, err := a.Store.Statement(r.Context(), wl.ID, limit)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"currency": a.Currency, "transactions": lines})
}

func (a *API) services(w http.ResponseWriter, r *http.Request) {
	svcs, err := a.Store.EnabledServices(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": svcs})
}

func (a *API) cashIn(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MSISDN string          `json:"msisdn"`
		Amount json.RawMessage `json:"amount"` // 1000, 1000.50 or "1,000.50"
	}
	if !decode(w, r, &in) {
		return
	}
	msisdn, err := phone.Normalize(in.MSISDN, a.CountryCode)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "Invalid phone number")
		return
	}
	amount, err := money.Parse(strings.Trim(string(in.Amount), `"`))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "Invalid amount")
		return
	}
	receipt, err := a.Payments.CashIn(r.Context(), msisdn, amount)
	var pe *payments.Error
	if errors.As(err, &pe) {
		writeError(w, http.StatusUnprocessableEntity, pe.Code, pe.Message)
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, receipt)
}

// ussd accepts the aggregator form post (or JSON) and replies in plain text.
func (a *API) ussd(w http.ResponseWriter, r *http.Request) {
	var req ussd.Request
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if !decode(w, r, &req) {
			return
		}
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "END Invalid request.", http.StatusBadRequest)
			return
		}
		req = ussd.Request{
			SessionID:   r.PostForm.Get("sessionId"),
			ServiceCode: r.PostForm.Get("serviceCode"),
			PhoneNumber: r.PostForm.Get("phoneNumber"),
			Text:        r.PostForm.Get("text"),
		}
	}
	resp := a.USSD.Handle(r.Context(), req)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(resp.String()))
}

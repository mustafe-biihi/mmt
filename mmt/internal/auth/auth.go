// Package auth handles registration, PIN verification with lockout, and
// Redis-backed web sessions.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"

	"github.com/mustafe-biihi/mmt/mmt/internal/phone"
	"github.com/mustafe-biihi/mmt/mmt/internal/store"
)

var (
	ErrInvalidCredentials = errors.New("invalid phone number or PIN")
	ErrAccountLocked      = errors.New("account locked due to too many wrong PIN attempts")
	ErrAccountInactive    = errors.New("account is not active")
	ErrInvalidSession     = errors.New("invalid or expired session")
	ErrInvalidPINFormat   = errors.New("PIN must be exactly 4 digits")
	ErrSessionActive      = errors.New("customer already has an active session")
)

// WrongPINError reports a failed PIN check that has not (yet) locked the account.
type WrongPINError struct{ Remaining int }

func (e *WrongPINError) Error() string {
	return fmt.Sprintf("wrong PIN, %d attempt(s) left", e.Remaining)
}

// ValidationError is a registration input problem safe to show to the user.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

const (
	webSessionPrefix = "mmt:websession:"
	webActivePrefix  = "mmt:websession-active:"
)

type Service struct {
	store       *store.Store
	rdb         *redis.Client
	countryCode string
	maxAttempts int
	sessionTTL  time.Duration
}

func New(st *store.Store, rdb *redis.Client, countryCode string, maxAttempts int, sessionTTL time.Duration) *Service {
	return &Service{store: st, rdb: rdb, countryCode: countryCode, maxAttempts: maxAttempts, sessionTTL: sessionTTL}
}

func ValidatePIN(pin string) error {
	if len(pin) != 4 {
		return ErrInvalidPINFormat
	}
	for _, r := range pin {
		if r < '0' || r > '9' {
			return ErrInvalidPINFormat
		}
	}
	return nil
}

func HashPIN(pin string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pin), bcrypt.DefaultCost)
	return string(h), err
}

// MatchesHash compares a PIN to a bcrypt hash without touching attempt counters.
func MatchesHash(hash, pin string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pin)) == nil
}

type RegisterInput struct {
	FullName   string `json:"fullName"`
	MSISDN     string `json:"msisdn"`
	NationalID string `json:"nationalId"`
	PIN        string `json:"pin"`
}

func (s *Service) Register(ctx context.Context, in RegisterInput) (store.Customer, store.Wallet, error) {
	name := strings.Join(strings.Fields(in.FullName), " ")
	if len(name) < 3 || len(name) > 120 {
		return store.Customer{}, store.Wallet{}, &ValidationError{"Full name must be between 3 and 120 characters"}
	}
	msisdn, err := phone.Normalize(in.MSISDN, s.countryCode)
	if err != nil {
		return store.Customer{}, store.Wallet{}, &ValidationError{"Invalid phone number"}
	}
	nid := strings.ToUpper(strings.TrimSpace(in.NationalID))
	if len(nid) < 4 || len(nid) > 40 {
		return store.Customer{}, store.Wallet{}, &ValidationError{"Invalid national ID"}
	}
	if err := ValidatePIN(in.PIN); err != nil {
		return store.Customer{}, store.Wallet{}, &ValidationError{err.Error()}
	}
	hash, err := HashPIN(in.PIN)
	if err != nil {
		return store.Customer{}, store.Wallet{}, err
	}
	c, w, err := s.store.CreateCustomer(ctx, store.Customer{
		MSISDN: msisdn, FullName: name, NationalID: nid, PINHash: hash,
	})
	if errors.Is(err, store.ErrDuplicateMSISDN) || errors.Is(err, store.ErrDuplicateNationalID) {
		return c, w, &ValidationError{err.Error()}
	}
	return c, w, err
}

// VerifyPIN checks the customer's PIN, counting failures and locking the account
// after maxAttempts consecutive wrong entries. Shared by web login and USSD.
func (s *Service) VerifyPIN(ctx context.Context, c store.Customer, pin string) error {
	switch c.Status {
	case store.StatusActive:
	case store.StatusLocked:
		return ErrAccountLocked
	default:
		return ErrAccountInactive
	}
	if MatchesHash(c.PINHash, pin) {
		if c.FailedPINAttempts > 0 {
			return s.store.ResetPINFailures(ctx, c.ID)
		}
		return nil
	}
	attempts, status, err := s.store.RecordPINFailure(ctx, c.ID, s.maxAttempts)
	if err != nil {
		return err
	}
	if status == store.StatusLocked {
		return ErrAccountLocked
	}
	return &WrongPINError{Remaining: s.maxAttempts - attempts}
}

// ChangePIN sets a new PIN for a customer who has already been authenticated.
func (s *Service) ChangePIN(ctx context.Context, customerID int64, newPIN string) error {
	if err := ValidatePIN(newPIN); err != nil {
		return err
	}
	hash, err := HashPIN(newPIN)
	if err != nil {
		return err
	}
	return s.store.UpdatePIN(ctx, customerID, hash)
}

// Login verifies credentials and opens a web session, returning its bearer token.
func (s *Service) Login(ctx context.Context, rawMSISDN, pin string) (string, store.Customer, error) {
	msisdn, err := phone.Normalize(rawMSISDN, s.countryCode)
	if err != nil {
		return "", store.Customer{}, ErrInvalidCredentials
	}
	c, err := s.store.CustomerByMSISDN(ctx, msisdn)
	if errors.Is(err, store.ErrNotFound) {
		return "", c, ErrInvalidCredentials
	}
	if err != nil {
		return "", c, err
	}
	if err := s.VerifyPIN(ctx, c, pin); err != nil {
		return "", c, err
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", c, err
	}
	token := hex.EncodeToString(raw)
	key := sessionKey(token)

	// One web session per customer: the lock and the session expire together,
	// so a customer who never logs out can log in again once it times out.
	claimed, err := s.rdb.SetNX(ctx, activeWebKey(c.ID), key, s.sessionTTL).Result()
	if err != nil {
		return "", c, fmt.Errorf("claim session: %w", err)
	}
	if !claimed {
		return "", c, ErrSessionActive
	}
	if err := s.rdb.Set(ctx, key, c.ID, s.sessionTTL).Err(); err != nil {
		s.rdb.Del(ctx, activeWebKey(c.ID))
		return "", c, fmt.Errorf("store session: %w", err)
	}
	return token, c, nil
}

// Authenticate resolves a bearer token to a customer ID and slides the session expiry.
func (s *Service) Authenticate(ctx context.Context, token string) (int64, error) {
	if token == "" {
		return 0, ErrInvalidSession
	}
	id, err := s.rdb.GetEx(ctx, sessionKey(token), s.sessionTTL).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, ErrInvalidSession
	}
	if err != nil {
		return 0, err
	}
	// Slide the customer's active-session lock along with the session.
	return id, s.rdb.Expire(ctx, activeWebKey(id), s.sessionTTL).Err()
}

func (s *Service) Logout(ctx context.Context, token string) error {
	key := sessionKey(token)
	id, err := s.rdb.GetDel(ctx, key).Int64()
	if errors.Is(err, redis.Nil) {
		return nil
	}
	if err != nil {
		return err
	}
	return releaseIfOwner.Run(ctx, s.rdb, []string{activeWebKey(id)}, key).Err()
}

func activeWebKey(customerID int64) string {
	return webActivePrefix + strconv.FormatInt(customerID, 10)
}

// releaseIfOwner deletes KEYS[1] only when it still holds ARGV[1].
var releaseIfOwner = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0`)

func (s *Service) SessionTTL() time.Duration { return s.sessionTTL }

// sessionKey stores only a hash of the token, so a Redis dump does not leak live tokens.
func sessionKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return webSessionPrefix + hex.EncodeToString(sum[:])
}

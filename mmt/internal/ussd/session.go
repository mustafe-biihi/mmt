package ussd

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/mustafe-biihi/mmt/mmt/internal/money"
)

const sessionPrefix = "mmt:ussd:"

var errNoSession = errors.New("ussd session not found")

// Session steps.
const (
	stepPIN           = "PIN"
	stepMenu          = "MENU"
	stepAirtimeChoice = "AIRTIME_CHOICE"
	stepTarget        = "TARGET"
	stepAmount        = "AMOUNT"
	stepConfirm       = "CONFIRM"
	stepOldPIN        = "OLD_PIN"
	stepNewPIN        = "NEW_PIN"
	stepConfirmPIN    = "CONFIRM_PIN"
)

// session is the per-dial state kept in Redis. It expires after the configured
// idle TTL (30s by default); every interaction refreshes it.
type session struct {
	MSISDN     string       `json:"msisdn"`
	CustomerID int64        `json:"customerId"`
	FirstName  string       `json:"firstName"`
	Step       string       `json:"step"`
	Menu       []string     `json:"menu,omitempty"` // service codes, by menu position
	Service    string       `json:"service,omitempty"`
	Target     string       `json:"target,omitempty"`
	TargetName string       `json:"targetName,omitempty"`
	Amount     money.Amount `json:"amount,omitempty"`
	Fee        money.Amount `json:"fee,omitempty"`
	NewPINHash string       `json:"newPinHash,omitempty"`
}

// resetFlow clears per-service data when going back to the main menu.
func (s *session) resetFlow() {
	s.Service, s.Target, s.TargetName, s.NewPINHash = "", "", "", ""
	s.Amount, s.Fee = 0, 0
}

type sessionStore struct {
	rdb *redis.Client
	ttl time.Duration
}

func (st sessionStore) load(ctx context.Context, id string) (*session, error) {
	raw, err := st.rdb.Get(ctx, sessionPrefix+id).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, errNoSession
	}
	if err != nil {
		return nil, err
	}
	var s session
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// save stores the session and refreshes the customer's active-session lock,
// so both expire together after the idle TTL.
func (st sessionStore) save(ctx context.Context, id string, s *session) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = st.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.Set(ctx, sessionPrefix+id, raw, st.ttl)
		p.Set(ctx, activeKey(s.CustomerID), id, st.ttl)
		return nil
	})
	return err
}

// delete removes the session and releases the customer's lock if this session holds it.
func (st sessionStore) delete(ctx context.Context, id string, s *session) error {
	if err := st.rdb.Del(ctx, sessionPrefix+id).Err(); err != nil {
		return err
	}
	if s == nil {
		return nil
	}
	return releaseIfOwner.Run(ctx, st.rdb, []string{activeKey(s.CustomerID)}, id).Err()
}

// claim marks sessionID as the customer's only active USSD session.
// It returns false if a different session is still active.
func (st sessionStore) claim(ctx context.Context, customerID int64, sessionID string) (bool, error) {
	ok, err := st.rdb.SetNX(ctx, activeKey(customerID), sessionID, st.ttl).Result()
	if err != nil || ok {
		return ok, err
	}
	owner, err := st.rdb.Get(ctx, activeKey(customerID)).Result()
	if errors.Is(err, redis.Nil) { // expired between the two calls
		return st.rdb.SetNX(ctx, activeKey(customerID), sessionID, st.ttl).Result()
	}
	return owner == sessionID, err
}

func activeKey(customerID int64) string {
	return activeSessionPrefix + strconv.FormatInt(customerID, 10)
}

const activeSessionPrefix = "mmt:ussd-active:" // must not overlap sessionPrefix+sessionId

// releaseIfOwner deletes KEYS[1] only when it still holds ARGV[1], so an old
// session can never release a lock now owned by a newer one.
var releaseIfOwner = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0`)

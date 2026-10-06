// Package money provides Amount, an exact decimal money type with 2 decimal places.
//
// In Postgres, amounts are NUMERIC(18,2) holding the actual value (1000 = 1,000.00).
// In Go they are held as an integer number of hundredths so arithmetic is exact
// (no floating point); conversion happens only at the database/JSON boundary.
package money

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var ErrInvalidAmount = errors.New("invalid amount")

// Amount is a monetary value with 2 decimal places, stored as hundredths.
type Amount int64

// FromMajor builds an Amount from whole currency units (e.g. FromMajor(100) = 100.00).
func FromMajor(units int64) Amount { return Amount(units * 100) }

// Parse converts user input such as "10", "10.5", "10.50" or "1,000.00" into a
// positive Amount. It is used for customer-entered amounts.
func Parse(s string) (Amount, error) {
	a, err := parseDecimal(strings.ReplaceAll(strings.TrimSpace(s), ",", ""))
	if err != nil || a <= 0 {
		return 0, ErrInvalidAmount
	}
	return a, nil
}

// parseDecimal accepts an optionally signed decimal with up to 2 fraction digits
// (extra trailing zeros, as Postgres may return, are allowed).
func parseDecimal(s string) (Amount, error) {
	neg := false
	if strings.HasPrefix(s, "-") {
		neg, s = true, s[1:]
	}
	whole, frac, hasDot := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	if !digits(whole) || len(whole) > 16 {
		return 0, ErrInvalidAmount
	}
	if hasDot {
		if !digits(frac) {
			return 0, ErrInvalidAmount
		}
		if len(frac) > 2 {
			if strings.Trim(frac[2:], "0") != "" {
				return 0, ErrInvalidAmount // more precision than 2 decimals
			}
			frac = frac[:2]
		}
	}
	frac = (frac + "00")[:2]
	w, _ := strconv.ParseInt(whole, 10, 64)
	f, _ := strconv.ParseInt(frac, 10, 64)
	a := Amount(w*100 + f)
	if neg {
		a = -a
	}
	return a, nil
}

// String renders the plain decimal value, e.g. "1000.50" or "-2.00".
func (a Amount) String() string {
	sign := ""
	if a < 0 {
		sign, a = "-", -a
	}
	return fmt.Sprintf("%s%d.%02d", sign, a/100, a%100)
}

// Grouped renders with thousands separators, e.g. "1,000.50".
func (a Amount) Grouped() string {
	sign := ""
	if a < 0 {
		sign, a = "-", -a
	}
	whole := strconv.FormatInt(int64(a/100), 10)
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return fmt.Sprintf("%s%s.%02d", sign, b.String(), a%100)
}

// Format renders an amount with its currency, e.g. "USD 1,000.50".
func Format(a Amount, currency string) string { return currency + " " + a.Grouped() }

// MulBPS returns a * bps / 10000, rounded half up (used for percentage fees).
func (a Amount) MulBPS(bps int64) Amount {
	return Amount((int64(a)*bps + 5000) / 10000)
}

// Value implements driver.Valuer: amounts are sent to Postgres as decimal text.
func (a Amount) Value() (driver.Value, error) { return a.String(), nil }

// Scan implements sql.Scanner for NUMERIC columns.
func (a *Amount) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*a = 0
		return nil
	case string:
		return a.scanText(v)
	case []byte:
		return a.scanText(string(v))
	case int64:
		*a = FromMajor(v)
		return nil
	}
	return fmt.Errorf("money: cannot scan %T into Amount", src)
}

func (a *Amount) scanText(s string) error {
	v, err := parseDecimal(s)
	if err != nil {
		return fmt.Errorf("money: cannot scan %q: %w", s, err)
	}
	*a = v
	return nil
}

// MarshalJSON emits the actual amount as a JSON number, e.g. 1000.50.
func (a Amount) MarshalJSON() ([]byte, error) { return []byte(a.String()), nil }

// UnmarshalJSON accepts a JSON number (10.5) or string ("10.50").
func (a *Amount) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	v, err := parseDecimal(s)
	if err != nil {
		return ErrInvalidAmount
	}
	*a = v
	return nil
}

func digits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

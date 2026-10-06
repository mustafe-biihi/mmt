// Package phone normalises mobile numbers (MSISDNs) to international format without "+".
package phone

import (
	"errors"
	"strings"
)

var ErrInvalidNumber = errors.New("invalid phone number")

// Normalize turns "+252 61 234 5678", "00252612345678", "0612345678" or "612345678"
// into "252612345678" for the given country code.
func Normalize(raw, countryCode string) (string, error) {
	s := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "").Replace(strings.TrimSpace(raw))
	switch {
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	case strings.HasPrefix(s, "00"):
		s = s[2:]
	case strings.HasPrefix(s, "0"):
		s = countryCode + s[1:]
	case !strings.HasPrefix(s, countryCode) && len(s) <= 9:
		s = countryCode + s
	}
	if len(s) < 10 || len(s) > 15 {
		return "", ErrInvalidNumber
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return "", ErrInvalidNumber
		}
	}
	return s, nil
}

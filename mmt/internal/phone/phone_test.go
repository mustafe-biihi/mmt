package phone

import "testing"

func TestNormalize(t *testing.T) {
	ok := map[string]string{
		"252612345678":     "252612345678",
		"+252 61 234 5678": "252612345678",
		"00252612345678":   "252612345678",
		"0612345678":       "252612345678",
		"612345678":        "252612345678",
	}
	for in, want := range ok {
		got, err := Normalize(in, "252")
		if err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "123", "61234abcd", "+2526123456789012"} {
		if _, err := Normalize(in, "252"); err == nil {
			t.Errorf("Normalize(%q) expected error", in)
		}
	}
}

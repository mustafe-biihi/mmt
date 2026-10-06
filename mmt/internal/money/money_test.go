package money

import (
	"encoding/json"
	"testing"
)

func TestParse(t *testing.T) {
	ok := map[string]Amount{"10": 1000, "10.5": 1050, "10.50": 1050, "0.25": 25, ".75": 75, " 3 ": 300, "1,000": 100000}
	for in, want := range ok {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "0.00", "-5", "1.234", "abc", "1e3", "12345678901234567"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) expected error", in)
		}
	}
}

func TestFormat(t *testing.T) {
	cases := map[Amount]string{0: "USD 0.00", 5: "USD 0.05", 1050: "USD 10.50", -250: "USD -2.50",
		100000: "USD 1,000.00", 123456789: "USD 1,234,567.89"}
	for in, want := range cases {
		if got := Format(in, "USD"); got != want {
			t.Errorf("Format(%d) = %q; want %q", in, got, want)
		}
	}
}

func TestScanFromPostgresNumeric(t *testing.T) {
	cases := map[string]Amount{"1000": 100000, "1000.00": 100000, "10.5": 1050, "-2.30": -230, "0.0000": 0}
	for in, want := range cases {
		var a Amount
		if err := a.Scan(in); err != nil || a != want {
			t.Errorf("Scan(%q) = %d, %v; want %d", in, a, err, want)
		}
	}
	var a Amount
	if err := a.Scan("1.005"); err == nil {
		t.Error("Scan should reject sub-cent precision")
	}
}

func TestJSON(t *testing.T) {
	b, _ := json.Marshal(struct{ A Amount }{100050})
	if string(b) != `{"A":1000.50}` {
		t.Errorf("marshal = %s", b)
	}
	var v struct{ A, B Amount }
	if err := json.Unmarshal([]byte(`{"A":10.5,"B":"1000"}`), &v); err != nil || v.A != 1050 || v.B != 100000 {
		t.Errorf("unmarshal = %+v, %v", v, err)
	}
}

func TestMulBPS(t *testing.T) {
	if got := Amount(1000).MulBPS(100); got != 10 { // 1% of 10.00
		t.Errorf("got %d", got)
	}
	if got := Amount(1050).MulBPS(100); got != 11 { // 1% of 10.50 = 0.105 -> 0.11
		t.Errorf("got %d", got)
	}
}

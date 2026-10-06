package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRateLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := newRateLimiter(1, 2, time.Minute)
	l.now = func() time.Time { return now }

	if !l.allow("a") || !l.allow("a") {
		t.Fatal("burst of 2 should be allowed")
	}
	if l.allow("a") {
		t.Fatal("third request should be limited")
	}
	if !l.allow("b") {
		t.Fatal("other clients are independent")
	}
	now = now.Add(time.Second)
	if !l.allow("a") {
		t.Fatal("token should refill after 1s")
	}
}

func newTestGateway(t *testing.T, upstream http.HandlerFunc) http.Handler {
	t.Helper()
	srv := httptest.NewServer(upstream)
	t.Cleanup(srv.Close)
	h, err := newGateway(config{
		MMTURL: srv.URL, GatewayKey: "secret", AllowedOrigins: []string{"http://portal"},
		RateRPS: 100, RateBurst: 100, MaxBodyBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestProxyInjectsGatewayKeyAndOverridesSpoofed(t *testing.T) {
	var gotKey, gotReqID string
	h := newTestGateway(t, func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotReqID = r.Header.Get("X-Gateway-Key"), r.Header.Get("X-Request-ID")
		w.Write([]byte("ok"))
	})

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("X-Gateway-Key", "spoofed")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if gotKey != "secret" {
		t.Fatalf("upstream got gateway key %q, want %q", gotKey, "secret")
	}
	if gotReqID == "" || rec.Header().Get("X-Request-ID") != gotReqID {
		t.Fatalf("request id not propagated: upstream=%q response=%q", gotReqID, rec.Header().Get("X-Request-ID"))
	}
}

func TestUnknownRoutesAreNotProxied(t *testing.T) {
	called := false
	h := newTestGateway(t, func(w http.ResponseWriter, r *http.Request) { called = true })

	for _, path := range []string{"/healthz-mmt", "/internal", "/ussd"} { // GET /ussd is not allowed
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d", path, rec.Code)
		}
	}
	if called {
		t.Fatal("upstream should not be called")
	}
}

func TestCORSPreflight(t *testing.T) {
	h := newTestGateway(t, func(w http.ResponseWriter, r *http.Request) {})
	req := httptest.NewRequest(http.MethodOptions, "/api/auth/login", nil)
	req.Header.Set("Origin", "http://portal")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "http://portal" {
		t.Fatalf("preflight failed: %d %v", rec.Code, rec.Header())
	}
}

func TestUSSDUpstreamDownReturnsEnd(t *testing.T) {
	h, _ := newGateway(config{MMTURL: "http://127.0.0.1:1", GatewayKey: "k", RateRPS: 10, RateBurst: 10, MaxBodyBytes: 1024})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ussd", strings.NewReader("sessionId=1")))
	if !strings.HasPrefix(rec.Body.String(), "END ") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

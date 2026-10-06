package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"runtime/debug"
	"slices"
	"strings"
	"time"
)

// newGateway builds the full handler chain: recover -> request ID -> log -> CORS -> rate limit -> routes.
func newGateway(cfg config) (http.Handler, error) {
	upstream, err := url.Parse(cfg.MMTURL)
	if err != nil {
		return nil, err
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
			pr.SetXForwarded()
			// Overwrite anything the client sent: only the gateway may set this.
			pr.Out.Header.Set("X-Gateway-Key", cfg.GatewayKey)
			pr.Out.Header.Set("X-Request-ID", pr.In.Header.Get("X-Request-ID"))
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("upstream error", "path", r.URL.Path, "request_id", r.Header.Get("X-Request-ID"), "err", err)
			if r.URL.Path == "/ussd" {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusBadGateway)
				w.Write([]byte("END Service temporarily unavailable. Please try again later."))
				return
			}
			writeError(w, http.StatusBadGateway, "UPSTREAM_UNAVAILABLE", "Service temporarily unavailable")
		},
	}

	limited := http.MaxBytesHandler(proxy, cfg.MaxBodyBytes)
	healthClient := &http.Client{Timeout: 2 * time.Second}

	mux := http.NewServeMux()
	mux.Handle("/api/", limited)
	mux.Handle("POST /ussd", limited)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		status := map[string]string{"gateway": "ok", "mmt": "ok"}
		code := http.StatusOK
		resp, err := healthClient.Get(strings.TrimRight(cfg.MMTURL, "/") + "/healthz")
		if err != nil || resp.StatusCode != http.StatusOK {
			status["mmt"], code = "unavailable", http.StatusServiceUnavailable
		}
		if resp != nil {
			resp.Body.Close()
		}
		writeJSON(w, code, status)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Route not found")
	})

	limiter := newRateLimiter(cfg.RateRPS, cfg.RateBurst, 3*time.Minute)
	var h http.Handler = mux
	h = rateLimit(limiter, cfg.TrustProxy, h)
	h = cors(cfg.AllowedOrigins, h)
	h = logRequests(cfg.TrustProxy, h)
	h = requestID(h)
	h = recoverer(h)
	return h, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

type ctxKey struct{}

func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" || len(id) > 64 {
			b := make([]byte, 8)
			rand.Read(b)
			id = hex.EncodeToString(b)
		}
		r.Header.Set("X-Request-ID", id)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func logRequests(trustProxy bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/healthz" {
			return
		}
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(), "client_ip", clientIP(r, trustProxy),
			"request_id", r.Header.Get("X-Request-ID"))
	})
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				slog.Error("panic", "err", v, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "INTERNAL", "Internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func cors(allowed []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && slices.Contains(allowed, origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
			h.Set("Access-Control-Max-Age", "600")
			h.Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func rateLimit(l *rateLimiter, trustProxy bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" && !l.allow(clientIP(r, trustProxy)) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "Too many requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP uses the left-most X-Forwarded-For entry only when the gateway sits
// behind a trusted reverse proxy (e.g. the portal's nginx); otherwise RemoteAddr.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			return strings.TrimSpace(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Command gateway is the single entry point for all web and USSD traffic.
// It applies CORS, rate limiting, request IDs and body limits, then proxies
// allow-listed routes to the MMT core service with a shared gateway key.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type config struct {
	ListenAddr     string
	MMTURL         string
	GatewayKey     string
	AllowedOrigins []string
	RateRPS        float64
	RateBurst      int
	MaxBodyBytes   int64
	TrustProxy     bool
}

func loadConfig() config {
	return config{
		ListenAddr:     env("LISTEN_ADDR", ":8080"),
		MMTURL:         env("MMT_URL", "http://localhost:8081"),
		GatewayKey:     env("GATEWAY_KEY", "dev-gateway-key"),
		AllowedOrigins: splitList(env("ALLOWED_ORIGINS", "http://localhost:5173,http://localhost:3000")),
		RateRPS:        envFloat("RATE_LIMIT_RPS", 10),
		RateBurst:      int(envFloat("RATE_LIMIT_BURST", 30)),
		MaxBodyBytes:   int64(envFloat("MAX_BODY_BYTES", 1<<20)),
		TrustProxy:     env("TRUST_PROXY", "false") == "true",
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "gateway"))
	cfg := loadConfig()

	handler, err := newGateway(cfg)
	if err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("gateway listening", "addr", cfg.ListenAddr, "upstream", cfg.MMTURL)
		errCh <- srv.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			slog.Error("fatal", "err", err)
			os.Exit(1)
		}
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if f, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil {
		return f
	}
	return def
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

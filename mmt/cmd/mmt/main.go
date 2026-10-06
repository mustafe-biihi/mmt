// Command mmt runs the core MMT service: login, sessions, USSD and transaction processing.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/mustafe-biihi/mmt/mmt/internal/api"
	"github.com/mustafe-biihi/mmt/mmt/internal/auth"
	"github.com/mustafe-biihi/mmt/mmt/internal/config"
	"github.com/mustafe-biihi/mmt/mmt/internal/db"
	"github.com/mustafe-biihi/mmt/mmt/internal/money"
	"github.com/mustafe-biihi/mmt/mmt/internal/payments"
	"github.com/mustafe-biihi/mmt/mmt/internal/store"
	"github.com/mustafe-biihi/mmt/mmt/internal/ussd"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "mmt"))
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.GatewayKey == "dev-gateway-key" || cfg.AdminKey == "dev-admin-key" {
		slog.Warn("using development secrets; set GATEWAY_KEY and ADMIN_KEY in production")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword, DB: cfg.RedisDB})
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return err
	}

	st := store.New(pool)
	authSvc := auth.New(st, rdb, cfg.CountryCode, cfg.MaxPINAttempts, cfg.WebSessionTTL)
	paySvc, err := payments.New(ctx, pool, st, cfg.Currency)
	if err != nil {
		return err
	}
	ussdHandler := ussd.NewHandler(st, authSvc, paySvc, rdb,
		cfg.USSDServiceCode, cfg.Currency, cfg.CountryCode, cfg.USSDSessionTTL)

	if cfg.SeedDemo {
		seedDemo(ctx, authSvc, paySvc)
	}

	a := &api.API{
		Pool: pool, Redis: rdb, Store: st, Auth: authSvc, Payments: paySvc, USSD: ussdHandler,
		GatewayKey: cfg.GatewayKey, AdminKey: cfg.AdminKey, Currency: cfg.Currency, CountryCode: cfg.CountryCode,
	}
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           a.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("mmt listening", "addr", cfg.HTTPAddr, "ussd_code", cfg.USSDServiceCode, "ussd_ttl", cfg.USSDSessionTTL.String())
		errCh <- srv.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// seedDemo creates two funded demo customers (PIN 1234) for local testing.
func seedDemo(ctx context.Context, au *auth.Service, pay *payments.Service) {
	demo := []auth.RegisterInput{
		{FullName: "Amina Yusuf", MSISDN: "252610000001", NationalID: "DEMO0001", PIN: "1234"},
		{FullName: "Abdi Hassan", MSISDN: "252610000002", NationalID: "DEMO0002", PIN: "1234"},
	}
	for _, in := range demo {
		_, _, err := au.Register(ctx, in)
		var ve *auth.ValidationError
		if errors.As(err, &ve) {
			continue // already seeded
		}
		if err != nil {
			slog.Error("seed demo customer", "msisdn", in.MSISDN, "err", err)
			continue
		}
		if _, err := pay.CashIn(ctx, in.MSISDN, money.FromMajor(100)); err != nil {
			slog.Error("seed demo cash-in", "msisdn", in.MSISDN, "err", err)
			continue
		}
		slog.Info("seeded demo customer", "msisdn", in.MSISDN, "pin", "1234", "balance", "100.00")
	}
}

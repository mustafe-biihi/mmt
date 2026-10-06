// Package config loads MMT service configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr      string
	DatabaseURL   string
	RedisAddr     string
	RedisPassword string
	RedisDB       int

	// GatewayKey is the shared secret the API gateway attaches to every request.
	// Requests without it are rejected, so MMT can only be reached through the gateway.
	GatewayKey string
	AdminKey   string

	Currency    string
	CountryCode string

	USSDServiceCode string
	USSDSessionTTL  time.Duration
	WebSessionTTL   time.Duration
	MaxPINAttempts  int

	SeedDemo bool
}

func Load() (Config, error) {
	c := Config{
		HTTPAddr:        env("HTTP_ADDR", ":8081"),
		DatabaseURL:     env("DATABASE_URL", "postgres://mmt:mmt@localhost:5433/mmt?sslmode=disable"),
		RedisAddr:       env("REDIS_ADDR", "localhost:6379"),
		RedisPassword:   env("REDIS_PASSWORD", ""),
		GatewayKey:      env("GATEWAY_KEY", "dev-gateway-key"),
		AdminKey:        env("ADMIN_KEY", "dev-admin-key"),
		Currency:        env("CURRENCY", "USD"),
		CountryCode:     env("COUNTRY_CODE", "252"),
		USSDServiceCode: env("USSD_SERVICE_CODE", "*836#"),
	}
	var err error
	if c.RedisDB, err = envInt("REDIS_DB", 0); err != nil {
		return c, err
	}
	if c.MaxPINAttempts, err = envInt("MAX_PIN_ATTEMPTS", 3); err != nil {
		return c, err
	}
	if c.USSDSessionTTL, err = envDuration("USSD_SESSION_TTL", 30*time.Second); err != nil {
		return c, err
	}
	if c.WebSessionTTL, err = envDuration("WEB_SESSION_TTL", 15*time.Minute); err != nil {
		return c, err
	}
	c.SeedDemo = env("SEED_DEMO", "false") == "true"
	return c, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v := env(key, "")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := env(key, "")
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

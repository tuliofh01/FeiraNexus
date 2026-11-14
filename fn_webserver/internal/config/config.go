// Package config loads environment configuration exactly once at startup.
//
// DIDACTIC: config is read once in main() and passed DOWN to constructors —
// no global mutable state, no repeated os.Getenv sprinkled around the code.
// If a required secret is missing, we fail AT BOOT, not on the first request.
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	PORT                string
	GIN_MODE            string
	DATABASE_URL        string
	JWT_SECRET          string
	AES_MASTER_KEY_HEX  string
	STRIPE_SECRET_KEY   string
	STRIPE_WEBHOOK_SECRET string
}

// Load reads .env (if present) then the process environment.
// In production (DigitalOcean) the .env file doesn't exist — values are
// injected by the platform, so a missing .env is NOT an error.
func Load() (*Config, error) {
	_ = godotenv.Load() // ignore error: .env is optional

	cfg := &Config{
		PORT:                  getEnv("PORT", "8080"),
		GIN_MODE:              getEnv("GIN_MODE", "debug"),
		DATABASE_URL:          os.Getenv("DATABASE_URL"),
		JWT_SECRET:            os.Getenv("JWT_SECRET"),
		AES_MASTER_KEY_HEX:    os.Getenv("AES_MASTER_KEY_HEX"),
		STRIPE_SECRET_KEY:     os.Getenv("STRIPE_SECRET_KEY"),
		STRIPE_WEBHOOK_SECRET: os.Getenv("STRIPE_WEBHOOK_SECRET"),
	}

	// Required: fail fast with a message that tells you HOW to fix it.
	var missing []string
	if cfg.DATABASE_URL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	if cfg.JWT_SECRET == "" {
		missing = append(missing, "JWT_SECRET")
	}
	if cfg.AES_MASTER_KEY_HEX == "" {
		missing = append(missing, "AES_MASTER_KEY_HEX")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf(
			"config: missing required env vars: %s (run ./scripts/gen_env.sh to create .env)",
			strings.Join(missing, ", "),
		)
	}
	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Package config reads runtime configuration from environment variables. Lambdas
// call MustLoad() at package init; missing required vars cause Init failure
// (better than a nil-pointer crash on first request).
package config

import (
	"fmt"
	"os"
)

type Config struct {
	Environment string // ENVIRONMENT — set by Terraform
	ItemsTable  string // ITEMS_TABLE — set by Terraform
	LogLevel    string // LOG_LEVEL  — set by Terraform; INFO|DEBUG
}

func MustLoad() *Config {
	cfg := &Config{
		Environment: os.Getenv("ENVIRONMENT"),
		ItemsTable:  os.Getenv("ITEMS_TABLE"),
		LogLevel:    getEnv("LOG_LEVEL", "INFO"),
	}
	if cfg.Environment == "" {
		panic(fmt.Errorf("ENVIRONMENT env var is required"))
	}
	if cfg.ItemsTable == "" {
		panic(fmt.Errorf("ITEMS_TABLE env var is required"))
	}
	return cfg
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

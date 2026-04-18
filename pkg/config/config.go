package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds runtime configuration sourced from environment variables.
type Config struct {
	HTTPAddr        string
	DB              DBConfig
	Sonarr          SonarrConfig
	RefreshInterval time.Duration
	FeedURL         string
	UserAgent       string
	// MaxSeasonCount is the upper bound on regular (non-special) seasons
	// a show may have to qualify for the Sonarr list. 1 means "strict
	// single-season only"; 2 includes shows with one sequel; etc. 0 or
	// negative disables the filter entirely.
	MaxSeasonCount int
	// TopN is how many of the weekly ranking's highest-ranked entries
	// are scraped, stored, and considered for the Sonarr list. Anime
	// Corner publishes a full top-30 each week; the default of 10
	// matches the original product rule ("top 10 of the blog").
	TopN int
}

// DBConfig holds Postgres connection parameters. Every field accepts
// arbitrary characters; they are URL-encoded when building the DSN so
// passwords with special characters do not break the connection string.
type DBConfig struct {
	Host    string
	Port    int
	User    string
	Pass    string
	Name    string
	SSLMode string
}

// SonarrConfig is the address of an existing Sonarr instance used as a
// TVDB-lookup proxy (via /api/v3/series/lookup).
type SonarrConfig struct {
	BaseURL string
	APIKey  string
}

// DSN builds a libpq-compatible URL with every user-supplied component
// percent-encoded.
func (d DBConfig) DSN() string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(d.User, d.Pass),
		Host:   fmt.Sprintf("%s:%d", d.Host, d.Port),
		Path:   "/" + d.Name,
	}
	q := u.Query()
	if d.SSLMode != "" {
		q.Set("sslmode", d.SSLMode)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// FromEnv loads configuration from environment variables, returning an
// error if any required variable is missing or malformed.
func FromEnv() (*Config, error) {
	cfg := &Config{
		HTTPAddr:        getEnv("HTTP_ADDR", ":8080"),
		FeedURL:         getEnv("FEED_URL", "https://animecorner.me/category/anime-corner/rankings/anime-of-the-week/feed"),
		UserAgent:       getEnv("USER_AGENT", "animelisterr/1.0 (+https://github.com/abs3ntdev/animelisterr)"),
		RefreshInterval: mustDuration(getEnv("REFRESH_INTERVAL", "1h")),
		MaxSeasonCount:  mustInt(getEnv("MAX_SEASON_COUNT", "1"), 1),
		TopN:            mustInt(getEnv("TOP_N", "10"), 10),
	}

	portStr := getEnv("DB_PORT", "5432")
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("DB_PORT must be an integer: %w", err)
	}

	// Defaults target a vanilla local Postgres (postgres/postgres on
	// localhost:5432) so `go run ./cmd/animelisterr` works out of the box.
	// In container/prod deployments every value should be explicitly set.
	cfg.DB = DBConfig{
		Host:    getEnv("DB_HOST", "localhost"),
		Port:    port,
		User:    getEnv("DB_USER", "postgres"),
		Pass:    getEnv("DB_PASS", "postgres"),
		Name:    getEnv("DB_NAME", "animelisterr"),
		SSLMode: getEnv("DB_SSLMODE", "disable"),
	}

	cfg.Sonarr = SonarrConfig{
		BaseURL: strings.TrimRight(os.Getenv("SONARR_URL"), "/"),
		APIKey:  os.Getenv("SONARR_API_KEY"),
	}
	if cfg.Sonarr.BaseURL == "" || cfg.Sonarr.APIKey == "" {
		return nil, fmt.Errorf("SONARR_URL and SONARR_API_KEY are required")
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func mustDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		return time.Hour
	}
	return d
}

// mustInt parses an integer env value, returning fallback on any error so
// a malformed value does not crash startup. Callers should log the fallback
// in practice; for now we trust the built-in default.
func mustInt(s string, fallback int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}

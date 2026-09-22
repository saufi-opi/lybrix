// Package config: single source of truth for all lybrix configuration
// (PRD §12). Every configurable value is read from the environment through
// this package — nothing else reads os.Getenv directly or hardcodes a
// host/port/model name.
//
// Defaults mirror 1.0's libs/core config.py *code* defaults; compose env
// overrides (e.g. EMBED_BATCH_SIZE=16, MAX_PARSE_BACKLOG=20000) keep
// landing on the same env names, so the compose file needs no change.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Settings carries every configurable value. Fields map 1:1 to the 1.0
// pydantic Settings env names (case-insensitive env lookup).
type Settings struct {
	// control plane
	DatabaseURL string
	RedisURL    string
	S3Endpoint  string
	S3AccessKey string
	S3SecretKey string
	S3BucketRaw string

	S3BucketParsed string

	// embedding plane (PRD §4.3: ingest and query TEIs never share one)
	TEIIngestURL        string
	TEIQueryURL         string
	EmbedBackend        string // tei | ollama
	EmbedModel          string
	EmbedDim            int
	EmbedBatchSize      int
	EmbedCtxBudget      int
	EmbedConcurrency    int
	EmbedTruncateChars  int
	EmbedQueryPrefix    string
	EmbedMaxAttempts    int
	EmbedRetryWindowSec int

	// splitting (PRD §6.2)
	ShardPages int

	// parsing (PRD §6.3 memory discipline)
	ParserSoftRSSMB      int // retained for compose parity; Go GC makes it a no-op
	ParserRecycleAfter   int
	ShardLeaseSeconds    int
	ParserPDFCacheDir    string
	OCRMinCharsPerPage   int
	DoclingURL           string
	AnyDocEnabled        bool
	MinYieldCharsPerPage int

	// queues / backpressure (PRD §6.1)
	MaxParseBacklog   int
	MaxDocumentPages  int
	WorkerPrefetch    int
	WorkerConcurrency int

	// janitor (PRD §6.6)
	JanitorInterval  time.Duration
	StuckMinutes     int
	MaxShardAttempts int

	// MCP / retrieval (PRD §7)
	MCPHost           string
	MCPPort           int
	APIHost           string
	APIPort           int
	SearchDefaultTopK int
	SearchMaxTopK     int
	ReadPagesMax      int
	RerankEnabled     bool
	TEIRerankURL      string
	RerankCandidates  int
	RerankTimeout     time.Duration

	// misc
	LogLevel string
}

// SettingsError names the offending variable — same contract as 1.0's
// SettingsError.
type SettingsError struct {
	Var   string
	Topic string
}

func (e *SettingsError) Error() string {
	return fmt.Sprintf("%s %s", e.Var, e.Topic)
}

func getenv(env map[string]string, key, def string) string {
	if v, ok := env[key]; ok && v != "" {
		return v
	}
	return def
}

func getint(env map[string]string, key string, def int) int {
	v, ok := env[key]
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return n
}

func getbool(env map[string]string, key string, def bool) bool {
	v, ok := env[key]
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return b
}

func getduration(env map[string]string, key string, def time.Duration) time.Duration {
	v, ok := env[key]
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		if d, derr := time.ParseDuration(strings.TrimSpace(v)); derr == nil {
			return d
		}
		return def
	}
	return time.Duration(n) * time.Second
}

// DefaultSettings returns the struct pre-populated with 1.0 code defaults.
func DefaultSettings() *Settings {
	return &Settings{
		DatabaseURL:          "postgres://rag:rag@localhost:5432/rag",
		RedisURL:             "redis://localhost:6379/0",
		S3Endpoint:           "http://localhost:9000",
		S3AccessKey:          "minioadmin",
		S3SecretKey:          "minioadmin",
		S3BucketRaw:          "raw",
		S3BucketParsed:       "parsed",
		TEIIngestURL:         "http://localhost:8081",
		TEIQueryURL:          "http://localhost:8082",
		EmbedBackend:         "tei",
		EmbedModel:           "BAAI/bge-m3",
		EmbedDim:             1024,
		EmbedBatchSize:       48,
		EmbedCtxBudget:       1900,
		EmbedConcurrency:     6,
		EmbedTruncateChars:   6000,
		EmbedQueryPrefix:     "search_query: ",
		EmbedMaxAttempts:     5,
		EmbedRetryWindowSec:  6 * 3600,
		ShardPages:           20,
		ParserSoftRSSMB:      6144,
		ParserRecycleAfter:   10,
		ShardLeaseSeconds:    600,
		OCRMinCharsPerPage:   20,
		DoclingURL:           "http://localhost:5001",
		AnyDocEnabled:        true,
		MinYieldCharsPerPage: 50,
		MaxParseBacklog:      2000,
		MaxDocumentPages:     800,
		WorkerPrefetch:       1,
		WorkerConcurrency:    1,
		JanitorInterval:      30 * time.Second,
		StuckMinutes:         60,
		MaxShardAttempts:     4,
		MCPHost:              "0.0.0.0",
		MCPPort:              8430,
		APIHost:              "0.0.0.0",
		APIPort:              8000,
		SearchDefaultTopK:    8,
		SearchMaxTopK:        25,
		ReadPagesMax:         30,
		RerankEnabled:        false,
		TEIRerankURL:         "http://localhost:8083",
		RerankCandidates:     30,
		RerankTimeout:        5 * time.Second,
		LogLevel:             "info",
	}
}

// Load reads the process environment on top of the defaults.
func Load() (*Settings, error) {
	return fromEnv(os.Environ())
}

func fromEnv(environ []string) (*Settings, error) {
	env := map[string]string{}
	for _, kv := range environ {
		if i := strings.IndexByte(kv, '='); i > 0 {
			env[kv[:i]] = kv[i+1:]
		}
	}
	s := DefaultSettings()
	s.DatabaseURL = getenv(env, "DATABASE_URL", s.DatabaseURL)
	s.RedisURL = getenv(env, "REDIS_URL", s.RedisURL)
	s.S3Endpoint = getenv(env, "S3_ENDPOINT", s.S3Endpoint)
	s.S3AccessKey = getenv(env, "S3_ACCESS_KEY", s.S3AccessKey)
	s.S3SecretKey = getenv(env, "S3_SECRET_KEY", s.S3SecretKey)
	s.S3BucketRaw = getenv(env, "S3_BUCKET_RAW", s.S3BucketRaw)
	s.S3BucketParsed = getenv(env, "S3_BUCKET_PARSED", s.S3BucketParsed)
	s.TEIIngestURL = getenv(env, "TEI_INGEST_URL", s.TEIIngestURL)
	s.TEIQueryURL = getenv(env, "TEI_QUERY_URL", s.TEIQueryURL)
	s.EmbedBackend = strings.ToLower(getenv(env, "EMBED_BACKEND", s.EmbedBackend))
	s.EmbedModel = getenv(env, "EMBED_MODEL", s.EmbedModel)
	s.EmbedDim = getint(env, "EMBED_DIM", s.EmbedDim)
	s.EmbedBatchSize = getint(env, "EMBED_BATCH_SIZE", s.EmbedBatchSize)
	s.EmbedCtxBudget = getint(env, "EMBED_CTX_BUDGET", s.EmbedCtxBudget)
	s.EmbedConcurrency = getint(env, "EMBED_CONCURRENCY", s.EmbedConcurrency)
	s.EmbedTruncateChars = getint(env, "EMBED_TRUNCATE_CHARS", s.EmbedTruncateChars)
	s.EmbedQueryPrefix = getenv(env, "EMBED_QUERY_PREFIX", s.EmbedQueryPrefix)
	s.EmbedMaxAttempts = getint(env, "EMBED_MAX_ATTEMPTS", s.EmbedMaxAttempts)
	s.EmbedRetryWindowSec = getint(env, "EMBED_RETRY_WINDOW_S", s.EmbedRetryWindowSec)
	s.ShardPages = getint(env, "SHARD_PAGES", s.ShardPages)
	s.ParserSoftRSSMB = getint(env, "PARSER_SOFT_RSS_MB", s.ParserSoftRSSMB)
	s.ParserRecycleAfter = getint(env, "PARSER_RECYCLE_AFTER", s.ParserRecycleAfter)
	s.ShardLeaseSeconds = getint(env, "SHARD_LEASE_SECONDS", s.ShardLeaseSeconds)
	s.ParserPDFCacheDir = getenv(env, "PARSER_PDF_CACHE_DIR", "")
	s.OCRMinCharsPerPage = getint(env, "OCR_MIN_CHARS_PER_PAGE", s.OCRMinCharsPerPage)
	s.DoclingURL = getenv(env, "DOCLING_URL", s.DoclingURL)
	s.AnyDocEnabled = getbool(env, "ANYDOC_ENABLED", s.AnyDocEnabled)
	s.MinYieldCharsPerPage = getint(env, "MIN_YIELD_CHARS_PER_PAGE", s.MinYieldCharsPerPage)
	s.MaxParseBacklog = getint(env, "MAX_PARSE_BACKLOG", s.MaxParseBacklog)
	s.MaxDocumentPages = getint(env, "MAX_DOCUMENT_PAGES", s.MaxDocumentPages)
	s.WorkerPrefetch = getint(env, "WORKER_PREFETCH", s.WorkerPrefetch)
	s.WorkerConcurrency = getint(env, "WORKER_CONCURRENCY", s.WorkerConcurrency)
	s.JanitorInterval = getduration(env, "JANITOR_INTERVAL", s.JanitorInterval)
	s.StuckMinutes = getint(env, "STUCK_MINUTES", s.StuckMinutes)
	s.MaxShardAttempts = getint(env, "MAX_SHARD_ATTEMPTS", s.MaxShardAttempts)
	s.MCPHost = getenv(env, "MCP_HOST", s.MCPHost)
	s.MCPPort = getint(env, "MCP_PORT", s.MCPPort)
	s.APIHost = getenv(env, "API_HOST", s.APIHost)
	s.APIPort = getint(env, "API_PORT", s.APIPort)
	s.SearchDefaultTopK = getint(env, "SEARCH_DEFAULT_TOP_K", s.SearchDefaultTopK)
	s.SearchMaxTopK = getint(env, "SEARCH_MAX_TOP_K", s.SearchMaxTopK)
	s.ReadPagesMax = getint(env, "READ_PAGES_MAX", s.ReadPagesMax)
	s.RerankEnabled = getbool(env, "RERANK_ENABLED", s.RerankEnabled)
	s.TEIRerankURL = getenv(env, "TEI_RERANK_URL", s.TEIRerankURL)
	s.RerankCandidates = getint(env, "RERANK_CANDIDATES", s.RerankCandidates)
	s.RerankTimeout = getduration(env, "RERANK_TIMEOUT_S", s.RerankTimeout)
	s.LogLevel = strings.ToLower(getenv(env, "LOG_LEVEL", s.LogLevel))
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return s, nil
}

// Validate enforces the same invariants as the 1.0 pydantic validator.
func (s *Settings) Validate() error {
	switch s.EmbedBackend {
	case "tei", "ollama":
	default:
		return &SettingsError{"EMBED_BACKEND", `must be "tei" or "ollama"`}
	}
	if s.EmbedBatchSize < 1 {
		return &SettingsError{"EMBED_BATCH_SIZE", "must be >= 1"}
	}
	if s.EmbedTruncateChars < 0 {
		return &SettingsError{"EMBED_TRUNCATE_CHARS", "must be >= 0"}
	}
	if s.SearchDefaultTopK > s.SearchMaxTopK {
		return &SettingsError{"SEARCH_DEFAULT_TOP_K",
			fmt.Sprintf("(%d) must be <= SEARCH_MAX_TOP_K (%d)", s.SearchDefaultTopK, s.SearchMaxTopK)}
	}
	if s.RerankEnabled && s.TEIRerankURL == "" {
		return &SettingsError{"RERANK_ENABLED", "=true requires TEI_RERANK_URL"}
	}
	if s.ShardPages < 1 {
		return &SettingsError{"SHARD_PAGES", "must be >= 1"}
	}
	return nil
}

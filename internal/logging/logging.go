// Package logging: slog setup honoring LOG_LEVEL. slog is the 1:1
// replacement for 1.0's std logging; every worker/API surface logs through
// it so log levels stay centrally configurable.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// Configure installs a slog default logger at the requested level and
// returns the level it actually applied (unknown levels fall back to info).
func Configure(level string) slog.Level {
	lvl := parseLevel(level)
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	slog.SetDefault(slog.New(handler))
	return lvl
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

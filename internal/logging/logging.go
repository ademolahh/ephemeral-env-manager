// Package logging configures the process-wide slog logger.
package logging

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Setup installs the default slog logger for service. LOG_FORMAT is text
// (default) or json, and LOG_LEVEL is debug, info (default), warn or error.
// The standard log package writes through the same handler.
func Setup(service string) error {
	var level slog.Level
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		if err := level.UnmarshalText([]byte(v)); err != nil {
			return fmt.Errorf("LOG_LEVEL %q must be debug, info, warn or error", v)
		}
	}

	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	switch strings.ToLower(os.Getenv("LOG_FORMAT")) {
	case "", "text":
		h = slog.NewTextHandler(os.Stderr, opts)
	case "json":
		h = slog.NewJSONHandler(os.Stderr, opts)
	default:
		return fmt.Errorf("LOG_FORMAT %q must be text or json", os.Getenv("LOG_FORMAT"))
	}

	slog.SetDefault(slog.New(h).With("service", service))
	return nil
}

// Fatal logs msg, and err when there is one, at error level and exits.
func Fatal(msg string, err error) {
	if err != nil {
		slog.Error(msg, "err", err)
	} else {
		slog.Error(msg)
	}
	os.Exit(1)
}

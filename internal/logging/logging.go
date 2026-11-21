package logging

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

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

func Fatal(msg string, err error) {
	if err != nil {
		slog.Error(msg, "err", err)
	} else {
		slog.Error(msg)
	}
	os.Exit(1)
}

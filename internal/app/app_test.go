package app

import (
	"context"
	"flag"
	"io"
	"log/slog"
	"testing"
)

func TestConfig_RoutingProviderBinding(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected string
		osrmURL  string
	}{
		{
			name:     "default",
			args:     []string{},
			expected: "tomtom",
			osrmURL:  "http://localhost:5000",
		},
		{
			name:     "osrm provider",
			args:     []string{"-routing", "osrm", "-osrm-url", "http://osrm:5000"},
			expected: "osrm",
			osrmURL:  "http://osrm:5000",
		},
		{
			name:     "haversine provider",
			args:     []string{"-routing", "haversine"},
			expected: "haversine",
			osrmURL:  "http://localhost:5000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg Config
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			cfg.Bind(fs)
			if err := fs.Parse(tt.args); err != nil {
				t.Fatalf("flag parse error: %v", err)
			}
			if cfg.RoutingProvider != tt.expected {
				t.Errorf("got RoutingProvider=%q, expected %q", cfg.RoutingProvider, tt.expected)
			}
			if cfg.OSRMURL != tt.osrmURL {
				t.Errorf("got OSRMURL=%q, expected %q", cfg.OSRMURL, tt.osrmURL)
			}
		})
	}
}

func TestConfig_TrafficAwareBinding(t *testing.T) {
	tests := []struct {
		name     string
		env      string
		args     []string
		expected bool
	}{
		{
			name:     "default is true",
			expected: true,
		},
		{
			name:     "flag false",
			args:     []string{"-traffic-aware=false"},
			expected: false,
		},
		{
			name:     "flag true",
			args:     []string{"-traffic-aware=true"},
			expected: true,
		},
		{
			name:     "env false",
			env:      "false",
			expected: false,
		},
		{
			name:     "env 0",
			env:      "0",
			expected: false,
		},
		{
			name:     "env true",
			env:      "true",
			expected: true,
		},
		{
			name:     "env overridden by flag",
			env:      "true",
			args:     []string{"-traffic-aware=false"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.env != "" {
				t.Setenv("TRAFFIC_AWARE", tt.env)
			}
			var cfg Config
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			cfg.Bind(fs)
			if err := fs.Parse(tt.args); err != nil {
				t.Fatalf("flag parse error: %v", err)
			}
			if cfg.TrafficAware != tt.expected {
				t.Errorf("got TrafficAware=%v, expected %v", cfg.TrafficAware, tt.expected)
			}
		})
	}
}

func TestConfig_BuildRoutingSelection(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	// 1. Unknown provider should error
	cfgUnknown := Config{
		RoutingProvider: "unknown-provider",
	}
	_, _, err := cfgUnknown.Build(ctx, log)
	if err == nil {
		t.Fatal("expected error for unknown routing provider, got nil")
	}

	// 2. TomTom without key should error
	cfgTomTom := Config{
		RoutingProvider: "tomtom",
	}
	_, _, err = cfgTomTom.Build(ctx, log)
	if err == nil {
		t.Fatal("expected error for tomtom without TOMTOM_API_KEY, got nil")
	}
}

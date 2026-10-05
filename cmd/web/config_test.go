package main

import (
	"strings"
	"testing"
)

// envMap turns a map into the getenv func loadConfig takes.
func envMap(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestLoadConfigRequiresDatabaseURL(t *testing.T) {
	t.Parallel()

	if _, err := loadConfig(envMap(nil)); err == nil {
		t.Fatal("loadConfig without DATABASE_URL returned no error")
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := loadConfig(envMap(map[string]string{"DATABASE_URL": "postgres://example/db"}))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.baseURL != defaultBaseURL {
		t.Errorf("baseURL = %q, want the default %q", cfg.baseURL, defaultBaseURL)
	}
	if cfg.cookieSecure {
		t.Error("cookieSecure = true without SLOTWISE_COOKIE_SECURE, want false")
	}
	if cfg.bootstrap.set() {
		t.Error("bootstrap is set without any SLOTWISE_BOOTSTRAP_* variable")
	}

	cfg, err = loadConfig(envMap(map[string]string{
		"DATABASE_URL":           "postgres://example/db",
		"SLOTWISE_BASE_URL":      "https://slotwise.example",
		"SLOTWISE_COOKIE_SECURE": "1",
	}))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.baseURL != "https://slotwise.example" {
		t.Errorf("baseURL = %q, want the configured value", cfg.baseURL)
	}
	if !cfg.cookieSecure {
		t.Error("cookieSecure = false with SLOTWISE_COOKIE_SECURE=1, want true")
	}
}

// TestLoadConfigBootstrapGroup pins the whole provisioning contract: absent is
// fine, complete is fine, and anything in between stops startup instead of
// quietly serving without a tenant.
func TestLoadConfigBootstrapGroup(t *testing.T) {
	t.Parallel()

	complete := map[string]string{
		"DATABASE_URL":                      "postgres://example/db",
		"SLOTWISE_BOOTSTRAP_SLUG":           "demo",
		"SLOTWISE_BOOTSTRAP_NAME":           "Demo Salon",
		"SLOTWISE_BOOTSTRAP_TIMEZONE":       "Europe/Berlin",
		"SLOTWISE_BOOTSTRAP_OWNER_EMAIL":    "owner@example.com",
		"SLOTWISE_BOOTSTRAP_OWNER_PASSWORD": "correct-horse-battery",
	}

	tests := []struct {
		name    string
		values  map[string]string
		wantErr string
	}{
		{name: "complete group", values: complete},
		{
			name:    "only the slug",
			values:  map[string]string{"SLOTWISE_BOOTSTRAP_SLUG": "demo"},
			wantErr: "SLOTWISE_BOOTSTRAP_NAME",
		},
		{
			name:    "slug without a password",
			values:  map[string]string{"SLOTWISE_BOOTSTRAP_SLUG": "demo", "SLOTWISE_BOOTSTRAP_NAME": "Demo", "SLOTWISE_BOOTSTRAP_TIMEZONE": "UTC", "SLOTWISE_BOOTSTRAP_OWNER_EMAIL": "owner@example.com"},
			wantErr: "SLOTWISE_BOOTSTRAP_OWNER_PASSWORD",
		},
		{
			name:    "a group without its slug",
			values:  map[string]string{"SLOTWISE_BOOTSTRAP_NAME": "Demo Salon"},
			wantErr: "SLOTWISE_BOOTSTRAP_SLUG",
		},
		{
			name:    "a timezone without its slug",
			values:  map[string]string{"SLOTWISE_BOOTSTRAP_TIMEZONE": "Europe/Berlin"},
			wantErr: "SLOTWISE_BOOTSTRAP_SLUG",
		},
		{
			name:    "an owner password without its slug",
			values:  map[string]string{"SLOTWISE_BOOTSTRAP_OWNER_PASSWORD": "correct-horse-battery"},
			wantErr: "SLOTWISE_BOOTSTRAP_SLUG",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			values := map[string]string{"DATABASE_URL": "postgres://example/db"}
			for key, value := range tt.values {
				values[key] = value
			}

			cfg, err := loadConfig(envMap(values))
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("loadConfig: %v", err)
			case tt.wantErr == "":
				if !cfg.bootstrap.set() {
					t.Error("the bootstrap group was not loaded")
				}
			case err == nil:
				t.Fatalf("loadConfig accepted a partial bootstrap group (%v), want an error naming %s", tt.values, tt.wantErr)
			case !strings.Contains(err.Error(), tt.wantErr):
				t.Errorf("error %q does not name %s", err, tt.wantErr)
			}
		})
	}
}

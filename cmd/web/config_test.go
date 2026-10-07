package main

import (
	"strings"
	"testing"
)

// envMap turns a map into the getenv func loadConfig takes.
func envMap(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

// testCancelKey is long enough for app.NewSigner, so loadConfig
// accepts it wherever a test needs the rest of the environment.
const testCancelKey = "config-test-cancel-secret-32-bytes-plus"

// requiredEnv is the environment every configuration needs, whatever else it
// sets.
func requiredEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":           "postgres://example/db",
		"SLOTWISE_CANCEL_SECRET": testCancelKey,
	}
}

func TestLoadConfigRequiresDatabaseURL(t *testing.T) {
	t.Parallel()

	if _, err := loadConfig(envMap(map[string]string{"SLOTWISE_CANCEL_SECRET": testCancelKey})); err == nil {
		t.Fatal("loadConfig without DATABASE_URL returned no error")
	}
}

// TestLoadConfigRequiresCancelSecret pins the other required setting: the
// public cancel routes are signed, so a deployment without the secret cannot
// serve them.
func TestLoadConfigRequiresCancelSecret(t *testing.T) {
	t.Parallel()

	_, err := loadConfig(envMap(map[string]string{"DATABASE_URL": "postgres://example/db"}))
	if err == nil {
		t.Fatal("loadConfig without SLOTWISE_CANCEL_SECRET returned no error")
	}
	if !strings.Contains(err.Error(), "SLOTWISE_CANCEL_SECRET") {
		t.Errorf("error %q does not name SLOTWISE_CANCEL_SECRET", err)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := loadConfig(envMap(requiredEnv()))
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
	if cfg.cancelSecret != testCancelKey {
		t.Errorf("cancelSecret = %q, want the configured value", cfg.cancelSecret)
	}

	cfg, err = loadConfig(envMap(map[string]string{
		"DATABASE_URL":           "postgres://example/db",
		"SLOTWISE_CANCEL_SECRET": testCancelKey,
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
		"SLOTWISE_CANCEL_SECRET":            testCancelKey,
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
			values := requiredEnv()
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

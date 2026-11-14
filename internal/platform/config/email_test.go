package config

import (
	"strings"
	"testing"
)

func emailEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{"EMAIL_ENABLED", "SMTP_HOST", "SMTP_PORT", "SMTP_FROM", "SMTP_USERNAME", "SMTP_PASSWORD", "SMTP_TLS_MODE"} {
		t.Setenv(key, "")
	}
}
func TestEmailDisabledDoesNotRequireSMTP(t *testing.T) {
	emailEnvironment(t)
	c, err := LoadEmail()
	if err != nil || c.Enabled {
		t.Fatalf("disabled config: enabled=%v err=%v", c.Enabled, err)
	}
	t.Setenv("EMAIL_ENABLED", "false")
	t.Setenv("SMTP_PORT", "invalid")
	if _, err := LoadEmail(); err != nil {
		t.Fatal("disabled delivery should not validate unused SMTP settings")
	}
}
func TestEmailEnabledValidatesConfiguration(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"valid", "", ""},
		{"missing host", "SMTP_HOST", ""},
		{"host with port", "SMTP_HOST", "localhost:587"},
		{"missing port", "SMTP_PORT", ""},
		{"invalid port", "SMTP_PORT", "65536"},
		{"missing sender", "SMTP_FROM", ""},
		{"invalid sender", "SMTP_FROM", "invalid"},
		{"sender header injection", "SMTP_FROM", "sender@example.test\r\nBcc: other@example.test"},
		{"plaintext forbidden", "SMTP_TLS_MODE", "none"},
		{"missing TLS mode", "SMTP_TLS_MODE", ""},
		{"implicit TLS", "SMTP_TLS_MODE", "tls"},
		{"missing password", "SMTP_PASSWORD", ""},
		{"missing username", "SMTP_USERNAME", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			emailEnvironment(t)
			for key, value := range map[string]string{"EMAIL_ENABLED": "true", "SMTP_HOST": "smtp.example.test", "SMTP_PORT": "587", "SMTP_FROM": "University Events <sender@example.test>", "SMTP_TLS_MODE": "starttls", "SMTP_USERNAME": "test-user", "SMTP_PASSWORD": "private-password"} {
				t.Setenv(key, value)
			}
			if tc.key != "" {
				t.Setenv(tc.key, tc.value)
			}
			_, err := LoadEmail()
			valid := tc.name == "valid" || tc.name == "implicit TLS"
			if (err == nil) != valid {
				t.Fatalf("valid=%v err=%v", valid, err)
			}
			if err != nil && strings.Contains(err.Error(), "private-password") {
				t.Fatal("configuration error disclosed credentials")
			}
		})
	}
}
func TestEmailEnabledAllowsUnauthenticatedTLSRelay(t *testing.T) {
	emailEnvironment(t)
	for key, value := range map[string]string{"EMAIL_ENABLED": "true", "SMTP_HOST": "localhost", "SMTP_PORT": "465", "SMTP_FROM": "sender@example.test", "SMTP_TLS_MODE": "tls"} {
		t.Setenv(key, value)
	}
	c, err := LoadEmail()
	if err != nil || !c.Enabled {
		t.Fatalf("relay config: %v", err)
	}
}
func TestLocalEmailRequiresLoopbackLiteralWithoutCredentials(t *testing.T) {
	for _, tc := range []struct {
		host, username, password string
		valid                    bool
	}{
		{"127.0.0.1", "", "", true},
		{"::1", "", "", true},
		{"localhost", "", "", false},
		{"mailpit", "", "", false},
		{"192.168.1.10", "", "", false},
		{"0.0.0.0", "", "", false},
		{"smtp.example.test", "", "", false},
		{"127.0.0.1", "user", "password", false},
		{"127.0.0.1", "user", "", false},
	} {
		t.Run(tc.host+tc.username, func(t *testing.T) {
			emailEnvironment(t)
			for key, value := range map[string]string{"EMAIL_ENABLED": "true", "SMTP_HOST": tc.host, "SMTP_PORT": "1025", "SMTP_FROM": "demo@example.test", "SMTP_TLS_MODE": "local", "SMTP_USERNAME": tc.username, "SMTP_PASSWORD": tc.password} {
				t.Setenv(key, value)
			}
			_, err := LoadEmail()
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestEmailRejectsInvalidEnableFlag(t *testing.T) {
	emailEnvironment(t)
	t.Setenv("EMAIL_ENABLED", "perhaps")
	if _, err := LoadEmail(); err == nil {
		t.Fatal("invalid enable flag accepted")
	}
}

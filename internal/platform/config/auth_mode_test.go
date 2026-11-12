package config

import "testing"

func TestLoadAuthMode(t *testing.T) {
	for _, tc := range []struct {
		value, want string
		valid       bool
	}{
		{"", "google", true},
		{"google", "google", true},
		{"local", "local", true},
		{"other", "", false},
		{" local", "", false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("AUTH_MODE", tc.value)
			mode, err := LoadAuthMode()
			if mode != tc.want || (err == nil) != tc.valid {
				t.Fatalf("mode=%q err=%v", mode, err)
			}
		})
	}
}

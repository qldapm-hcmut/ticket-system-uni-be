package config

import (
	"fmt"
	"net"
	"net/mail"
	"os"
	"strconv"
	"strings"
)

// Email is worker-only configuration. It contains credentials: never log it.
// Disabled delivery leaves the outbox untouched.
type Email struct {
	Enabled  bool
	Host     string
	Port     int
	From     string
	Username string
	Password string
	TLSMode  string
}

func LoadEmail() (Email, error) {
	enabled := os.Getenv("EMAIL_ENABLED")
	if enabled == "" || enabled == "false" {
		return Email{}, nil
	}
	if enabled != "true" {
		return Email{}, fmt.Errorf("EMAIL_ENABLED must be true or false")
	}
	c := Email{Enabled: true, Host: os.Getenv("SMTP_HOST"), From: os.Getenv("SMTP_FROM"), Username: os.Getenv("SMTP_USERNAME"), Password: os.Getenv("SMTP_PASSWORD"), TLSMode: os.Getenv("SMTP_TLS_MODE")}
	if c.Host == "" || (net.ParseIP(c.Host) == nil && strings.ContainsAny(c.Host, " \t\r\n/:@")) {
		return Email{}, fmt.Errorf("SMTP_HOST must be a hostname")
	}
	port, err := strconv.Atoi(os.Getenv("SMTP_PORT"))
	if err != nil || port < 1 || port > 65535 {
		return Email{}, fmt.Errorf("SMTP_PORT must be between 1 and 65535")
	}
	c.Port = port
	if strings.ContainsAny(c.From, "\r\n") {
		return Email{}, fmt.Errorf("SMTP_FROM must be one valid mailbox")
	}
	if _, err := mail.ParseAddress(c.From); err != nil {
		return Email{}, fmt.Errorf("SMTP_FROM must be one valid mailbox")
	}
	if c.TLSMode == "local" {
		ip := net.ParseIP(c.Host)
		if ip == nil || !ip.IsLoopback() || c.Username != "" || c.Password != "" {
			return Email{}, fmt.Errorf("SMTP local mode requires a loopback IP literal and no credentials")
		}
	} else if c.TLSMode != "starttls" && c.TLSMode != "tls" {
		return Email{}, fmt.Errorf("SMTP_TLS_MODE must be starttls, tls or local")
	}
	if (c.Username == "") != (c.Password == "") {
		return Email{}, fmt.Errorf("SMTP_USERNAME and SMTP_PASSWORD must both be set or both be empty")
	}
	return c, nil
}

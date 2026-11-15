// Command dev-token creates local demo accounts and prints a signed access token.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/auth/adapter/token"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/platform/config"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/shared/kernel"
)

func main() {
	role := flag.String("role", "student", "student, organizer or admin")
	flag.Parse()
	if err := run(*role); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(role string) error {
	emails := map[string]string{
		"student":   "student.demo@hcmut.edu.vn",
		"organizer": "organizer.demo@hcmut.edu.vn",
		"admin":     "admin.demo@hcmut.edu.vn",
	}
	email, ok := emails[role]
	if !ok {
		return fmt.Errorf("role must be student, organizer or admin")
	}
	mode, err := config.LoadAuthMode()
	if err != nil {
		return err
	}
	if mode != "local" {
		return fmt.Errorf("dev-token requires AUTH_MODE=local")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	dbConfig, err := pgx.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("invalid database configuration")
	}
	switch dbConfig.Host {
	case "localhost", "127.0.0.1", "::1":
	default:
		return fmt.Errorf("dev-token requires a loopback database host")
	}
	auth, err := config.LoadAuthToken()
	if err != nil {
		return err
	}
	tokens, err := token.New(auth.JWTSecret, kernel.SystemClock{})
	if err != nil {
		return err
	}
	seed, err := os.ReadFile("db/seed/demo.sql")
	if err != nil {
		return fmt.Errorf("read demo seed (run from backend root): %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, dbConfig)
	if err != nil {
		return fmt.Errorf("local database unavailable")
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, string(seed)); err != nil {
		return fmt.Errorf("seed demo accounts: %w", err)
	}
	var userID string
	if err := conn.QueryRow(ctx, "SELECT id::text FROM users WHERE email=$1 AND is_active", email).Scan(&userID); err != nil {
		return fmt.Errorf("active demo account unavailable")
	}
	access, err := tokens.AccessToken(userID, time.Now().Add(8*time.Hour))
	if err != nil {
		return err
	}
	fmt.Println(access)
	return nil
}

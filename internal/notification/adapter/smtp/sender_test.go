package smtp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"io"
	"math/big"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"testing"
	"time"

	a "github.com/qldapm-hcmut/ticket-system-uni/internal/notification/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/platform/config"
)

type smtpCapture struct {
	message, from, to, auth string
	err                     error
}

// All servers bind loopback and use an ephemeral test-only certificate.
func smtpCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	raw, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}))
	return cert, pool
}
func fakeSMTP(t *testing.T, mode string, offerTLS bool, behavior string) (config.Email, *x509.CertPool, <-chan smtpCapture) {
	t.Helper()
	cert, pool := smtpCertificate(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	result := make(chan smtpCapture, 1)
	go func() {
		capture := smtpCapture{}
		defer func() { result <- capture }()
		conn, err := listener.Accept()
		if err != nil {
			capture.err = err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		if mode == "tls" {
			conn = tls.Server(conn, tlsConfig)
		}
		wire := textproto.NewConn(conn)
		defer wire.Close()
		if err = wire.PrintfLine("220 localhost ESMTP"); err != nil {
			capture.err = err
			return
		}
		encrypted := mode == "tls"
		for {
			line, err := wire.ReadLine()
			if err != nil {
				return
			}
			verb, rest, _ := strings.Cut(line, " ")
			switch verb {
			case "EHLO":
				wire.PrintfLine("250-localhost")
				if !encrypted && offerTLS {
					wire.PrintfLine("250-STARTTLS")
				}
				if encrypted {
					wire.PrintfLine("250-AUTH PLAIN")
				}
				wire.PrintfLine("250 OK")
			case "STARTTLS":
				wire.PrintfLine("220 begin TLS")
				conn = tls.Server(conn, tlsConfig)
				wire = textproto.NewConn(conn)
				encrypted = true
			case "AUTH":
				parts := strings.Fields(rest)
				if len(parts) != 2 {
					capture.err = io.ErrUnexpectedEOF
					return
				}
				decoded, err := base64.StdEncoding.DecodeString(parts[1])
				if err != nil {
					capture.err = err
					return
				}
				capture.auth = string(decoded)
				wire.PrintfLine("235 authenticated")
			case "MAIL":
				capture.from = rest
				wire.PrintfLine("250 sender accepted")
			case "RCPT":
				capture.to = rest
				if behavior == "reject-recipient" {
					wire.PrintfLine("550 private recipient student@example.test rejected")
				} else {
					wire.PrintfLine("250 recipient accepted")
				}
			case "DATA":
				wire.PrintfLine("354 send message")
				data, err := io.ReadAll(wire.DotReader())
				if err != nil {
					capture.err = err
					return
				}
				capture.message = string(data)
				if behavior == "reject-data" {
					wire.PrintfLine("554 private message for student@example.test rejected")
				} else {
					wire.PrintfLine("250 queued")
				}
			case "QUIT":
				if behavior == "disconnect-on-quit" {
					return
				}
				wire.PrintfLine("221 bye")
				return
			default:
				capture.err = io.ErrUnexpectedEOF
				return
			}
		}
	}()
	return config.Email{Enabled: true, Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, From: "University Events <sender@example.test>", Username: "test-user", Password: "test-password", TLSMode: mode}, pool, result
}
func TestSenderDeliversUTF8OverVerifiedTLS(t *testing.T) {
	for _, mode := range []string{"starttls", "tls"} {
		t.Run(mode, func(t *testing.T) {
			c, pool, result := fakeSMTP(t, mode, true, "")
			sender := New(c)
			sender.tlsConfig.RootCAs = pool
			delivery := a.EmailDelivery{ID: "notification-one", Recipient: "student@example.test", Title: "Bạn đã có chỗ", Body: "Thông báo sự kiện.\nDòng thứ hai."}
			if err := sender.Send(context.Background(), delivery); err != nil {
				t.Fatal(err)
			}
			capture := <-result
			if capture.err != nil {
				t.Fatal(capture.err)
			}
			if capture.auth != "\x00test-user\x00test-password" || capture.from != "FROM:<sender@example.test>" || capture.to != "TO:<student@example.test>" {
				t.Fatalf("wrong SMTP envelope/auth")
			}
			message, err := mail.ReadMessage(strings.NewReader(capture.message))
			if err != nil {
				t.Fatal(err)
			}
			subject, err := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
			if err != nil || subject != "Bạn đã có chỗ" {
				t.Fatalf("subject=%q err=%v", subject, err)
			}
			body, err := io.ReadAll(quotedprintable.NewReader(message.Body))
			if err != nil || strings.ReplaceAll(string(body), "\r\n", "\n") != "Thông báo sự kiện.\nDòng thứ hai.\n" {
				t.Fatalf("body=%q err=%v", body, err)
			}
			if message.Header.Get("Message-ID") == "" || !strings.Contains(message.Header.Get("Content-Type"), "UTF-8") {
				t.Fatal("missing MIME headers")
			}
		})
	}
}
func TestSenderRejectsUntrustedTLSBeforeAuthentication(t *testing.T) {
	for _, mode := range []string{"starttls", "tls"} {
		t.Run(mode, func(t *testing.T) {
			c, _, result := fakeSMTP(t, mode, true, "")
			err := New(c).Send(context.Background(), a.EmailDelivery{ID: "one", Recipient: "student@example.test", Title: "Test", Body: "body"})
			if err == nil || strings.Contains(err.Error(), "student@example.test") {
				t.Fatalf("unsafe delivery error: %v", err)
			}
			capture := <-result
			if capture.auth != "" || capture.from != "" {
				t.Fatal("unverified TLS sent credentials or mail")
			}
		})
	}
}
func TestSenderRequiresSTARTTLS(t *testing.T) {
	c, _, result := fakeSMTP(t, "starttls", false, "")
	if err := New(c).Send(context.Background(), a.EmailDelivery{Recipient: "student@example.test", Title: "Test"}); err == nil {
		t.Fatal("plaintext relay accepted")
	}
	capture := <-result
	if capture.auth != "" || capture.from != "" {
		t.Fatal("credentials or mail sent without TLS")
	}
}
func TestSenderBoundsAndCancelsBlockedSMTPGreeting(t *testing.T) {
	for _, cancelRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "cancel"}[cancelRun], func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			connected := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				close(connected)
				io.Copy(io.Discard, conn)
			}()
			sender := New(config.Email{Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, From: "sender@example.test", TLSMode: "starttls"})
			sender.timeout = 100 * time.Millisecond
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelRun {
				sender.timeout = time.Minute
				go func() { <-connected; cancel() }()
			}
			start := time.Now()
			err = sender.Send(ctx, a.EmailDelivery{Recipient: "student@example.test", Title: "Test"})
			if err == nil || time.Since(start) > time.Second {
				t.Fatalf("unbounded send: %v", err)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("SMTP connection not closed")
			}
		})
	}
}
func TestSenderRejectsRelayFailuresWithoutLeakingResponse(t *testing.T) {
	for _, behavior := range []string{"reject-recipient", "reject-data", "disconnect-on-quit"} {
		t.Run(behavior, func(t *testing.T) {
			c, pool, result := fakeSMTP(t, "starttls", true, behavior)
			sender := New(c)
			sender.tlsConfig.RootCAs = pool
			err := sender.Send(context.Background(), a.EmailDelivery{ID: "one", Recipient: "student@example.test", Title: "Test", Body: "body"})
			if behavior == "disconnect-on-quit" {
				if err != nil {
					t.Fatalf("accepted message incorrectly failed: %v", err)
				}
			} else if err == nil || strings.Contains(err.Error(), "student@example.test") {
				t.Fatalf("unsafe relay error: %v", err)
			}
			<-result
		})
	}
}

func TestSenderRejectsWrongTLSHostname(t *testing.T) {
	c, pool, result := fakeSMTP(t, "tls", true, "")
	sender := New(c)
	sender.tlsConfig.RootCAs = pool
	sender.tlsConfig.ServerName = "wrong.example.test"
	if err := sender.Send(context.Background(), a.EmailDelivery{Recipient: "student@example.test", Title: "Test"}); err == nil {
		t.Fatal("wrong TLS hostname accepted")
	}
	capture := <-result
	if capture.auth != "" || capture.from != "" {
		t.Fatal("credentials sent to mismatched TLS identity")
	}
}

func TestSenderLocalDeliveryWithoutTLSOrAuthentication(t *testing.T) {
	c, _, result := fakeSMTP(t, "local", false, "")
	c.Username = ""
	c.Password = ""
	if err := New(c).Send(context.Background(), a.EmailDelivery{ID: "local-one", Recipient: "student@example.test", Title: "Local demo", Body: "Synthetic demo body"}); err != nil {
		t.Fatal(err)
	}
	capture := <-result
	if capture.err != nil || capture.auth != "" || capture.message == "" {
		t.Fatalf("local delivery failed: %v", capture.err)
	}
}

func TestSenderRejectsUnsafeLocalModeBeforeDial(t *testing.T) {
	for _, tc := range []struct{ host, user, password string }{
		{"smtp.example.test", "", ""},
		{"localhost", "", ""},
		{"mailpit", "", ""},
		{"127.0.0.1", "user", "password"},
		{"127.0.0.1", "", "password"},
	} {
		sender := New(config.Email{Host: tc.host, Port: 1025, From: "sender@example.test", TLSMode: "local", Username: tc.user, Password: tc.password})
		if err := sender.Send(context.Background(), a.EmailDelivery{Recipient: "student@example.test", Title: "Test"}); err == nil {
			t.Fatal("unsafe local configuration accepted")
		}
	}
}

func TestSenderRejectsHeaderInjectionBeforeDial(t *testing.T) {
	sender := New(config.Email{Host: "invalid.example.test", Port: 587, From: "sender@example.test", TLSMode: "starttls"})
	for _, d := range []a.EmailDelivery{
		{Recipient: "student@example.test\r\nBcc: other@example.test", Title: "Test"},
		{Recipient: "student@example.test", Title: "Test\r\nBcc: other@example.test"},
	} {
		if err := sender.Send(context.Background(), d); err == nil {
			t.Fatal("header injection accepted")
		}
	}
}

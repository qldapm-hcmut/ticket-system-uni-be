// Package smtp delivers notifications through verified TLS, or an explicitly
// selected loopback-only, unauthenticated local mail catcher.
package smtp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	stdsmtp "net/smtp"
	"strconv"
	"strings"
	"time"

	a "github.com/qldapm-hcmut/ticket-system-uni/internal/notification/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/platform/config"
)

// Errors deliberately omit relay responses, which may contain PII or secrets.
var errDelivery = errors.New("SMTP delivery failed")
var errMessage = errors.New("invalid email message")

type Sender struct {
	config    config.Email
	tlsConfig *tls.Config
	timeout   time.Duration
}

// New consumes validated worker-only configuration. Remote relays require TLS.
func New(c config.Email) *Sender {
	return &Sender{config: c, tlsConfig: &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}, timeout: 15 * time.Second}
}

func (s *Sender) Send(ctx context.Context, d a.EmailDelivery) error {
	// Recheck the local boundary here so even direct construction cannot send
	// credentials in plaintext or connect to a non-loopback mail relay.
	if s.config.TLSMode == "local" {
		ip := net.ParseIP(s.config.Host)
		if ip == nil || !ip.IsLoopback() || s.config.Username != "" || s.config.Password != "" {
			return errDelivery
		}
	}
	message, from, to, err := s.message(d)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	address := net.JoinHostPort(s.config.Host, strconv.Itoa(s.config.Port))
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return errDelivery
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return errDelivery
		}
	}
	// Closing the connection interrupts greeting, TLS, AUTH, DATA and QUIT waits.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	var transport net.Conn = conn
	switch s.config.TLSMode {
	case "tls":
		encrypted := tls.Client(conn, s.tlsConfig)
		if err := encrypted.HandshakeContext(ctx); err != nil {
			return errDelivery
		}
		transport = encrypted
	case "starttls", "local":
	default:
		return errDelivery
	}
	client, err := stdsmtp.NewClient(transport, s.config.Host)
	if err != nil {
		return errDelivery
	}
	defer client.Close()
	if s.config.TLSMode == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errDelivery
		}
		if err := client.StartTLS(s.tlsConfig); err != nil {
			return errDelivery
		}
	}
	if s.config.Username != "" {
		auth := stdsmtp.PlainAuth("", s.config.Username, s.config.Password, s.config.Host)
		if err := client.Auth(auth); err != nil {
			return errDelivery
		}
	}
	if err := client.Mail(from); err != nil {
		return errDelivery
	}
	if err := client.Rcpt(to); err != nil {
		return errDelivery
	}
	data, err := client.Data()
	if err != nil {
		return errDelivery
	}
	if _, err := data.Write(message); err != nil {
		return errDelivery
	}
	if err := data.Close(); err != nil {
		return errDelivery
	}
	// DATA acceptance is the delivery boundary. A subsequent QUIT failure must
	// not turn an already-accepted message into another retry.
	_ = client.Quit()
	return nil
}

func (s *Sender) message(d a.EmailDelivery) ([]byte, string, string, error) {
	if strings.ContainsAny(s.config.From+d.Recipient+d.Title, "\r\n") {
		return nil, "", "", errMessage
	}
	from, err := mail.ParseAddress(s.config.From)
	if err != nil {
		return nil, "", "", errMessage
	}
	to, err := mail.ParseAddress(d.Recipient)
	if err != nil {
		return nil, "", "", errMessage
	}
	// A stable ID helps clients recognize retries but does not guarantee deduplication.
	id := sha256.Sum256([]byte(d.ID))
	var message bytes.Buffer
	fmt.Fprintf(&message, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%x@notifications.local>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", from.String(), to.String(), mime.QEncoding.Encode("UTF-8", d.Title), time.Now().UTC().Format(time.RFC1123Z), id)
	writer := quotedprintable.NewWriter(&message)
	if _, err := writer.Write([]byte(d.Body)); err != nil {
		return nil, "", "", errMessage
	}
	if err := writer.Close(); err != nil {
		return nil, "", "", errMessage
	}
	return message.Bytes(), from.Address, to.Address, nil
}

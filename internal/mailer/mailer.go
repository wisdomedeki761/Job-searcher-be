// Package mailer sends outbound HTML email over SMTP. It is used to deliver
// generated credentials when the super admin creates a user account.
//
// Delivery uses STARTTLS on the configured port (587 by default), which is
// what Gmail app passwords, Mailgun, Postmark, AWS SES SMTP and most
// providers expect. When SMTPHost is empty the mailer is disabled and callers
// fall back to returning credentials in the API response.
package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Mailer sends HTML emails. A zero/disabled mailer (empty host) is a no-op.
type Mailer struct {
	host         string
	port         int
	fallbackPort int
	username     string
	password     string
	from         string // display form, e.g. `QX Scraper <no-reply@example.com>`
	log          *slog.Logger
}

// New creates a Mailer. host == "" disables sending (Enabled() == false).
func New(host string, port int, username, password, from string, log *slog.Logger) *Mailer {
	return NewWithFallback(host, port, 0, username, password, from, log)
}

// NewWithFallback creates a mailer that tries fallbackPort if port fails.
func NewWithFallback(host string, port, fallbackPort int, username, password, from string, log *slog.Logger) *Mailer {
	if port == 0 {
		port = 587
	}
	return &Mailer{
		host:         strings.TrimSpace(host),
		port:         port,
		fallbackPort: fallbackPort,
		username:     username,
		password:     password,
		from:         from,
		log:          log,
	}
}

// Enabled reports whether outbound email is configured.
func (m *Mailer) Enabled() bool {
	return m != nil && m.host != ""
}

// SendWelcome emails a new (or reset) account its generated credentials.
func (m *Mailer) SendWelcome(to, tempPassword, appURL string) error {
	subject := "Your QX Scraper login details"
	return m.SendHTML(to, subject, welcomeHTML(to, tempPassword, appURL))
}

// SendHTML delivers a raw HTML message.
func (m *Mailer) SendHTML(to, subject, htmlBody string) error {
	if !m.Enabled() {
		return fmt.Errorf("smtp not configured")
	}

	fromAddr := m.fromAddress()

	var auth smtp.Auth
	if m.username != "" {
		auth = smtp.PlainAuth("", m.username, m.password, m.host)
	}

	// RFC 5322 headers + single-part HTML body.
	msg := strings.Join([]string{
		"From: " + m.from,
		"To: " + to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=UTF-8",
		"",
		htmlBody,
	}, "\r\n")

	ports := []int{m.port}
	if m.fallbackPort != 0 && m.fallbackPort != m.port {
		ports = append(ports, m.fallbackPort)
	}
	var sendErrors []error
	for i, port := range ports {
		if err := m.send(port, auth, fromAddr, to, []byte(msg)); err != nil {
			sendErrors = append(sendErrors, fmt.Errorf("port %d: %w", port, err))
			if i < len(ports)-1 {
				m.log.Warn("smtp send failed; trying fallback port", "port", port, "fallback_port", ports[i+1], "err", err)
			}
			continue
		}
		if port != m.port {
			m.log.Info("smtp fallback port succeeded", "port", port)
		}
		m.log.Info("email sent", "to", to, "subject", subject)
		return nil
	}
	return fmt.Errorf("smtp send failed: %w", errors.Join(sendErrors...))
}

func (m *Mailer) send(port int, auth smtp.Auth, from, to string, msg []byte) error {
	addr := net.JoinHostPort(m.host, strconv.Itoa(port))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return err
	}

	if port == 465 {
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName: m.host,
			MinVersion: tls.VersionTLS12,
		})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return err
		}
		conn = tlsConn
	}

	client, err := smtp.NewClient(conn, m.host)
	if err != nil {
		return err
	}
	defer client.Close()

	if port != 465 {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{
				ServerName: m.host,
				MinVersion: tls.VersionTLS12,
			}); err != nil {
				return err
			}
		}
	}
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(msg); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return nil
}

// fromAddress extracts the bare address from `Name <addr>` for MAIL FROM.
func (m *Mailer) fromAddress() string {
	if a, err := mail.ParseAddress(m.from); err == nil && a.Address != "" {
		return a.Address
	}
	return m.from
}

// welcomeHTML renders the credentials email in the product's design
// language: Inter type, the #20243F / #72778F / #6253E8 / #EBE8FF palette,
// and the site's hero copy + chip ("Fresh roles, without the noise.").
// Kept dependency-free with inline styles + table layout so every mail
// client renders it (Outlook falls back from Inter to Segoe UI gracefully).
func welcomeHTML(to, tempPassword, appURL string) string {
	loginURL := html.EscapeString(appURL + "/login")
	return `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600&amp;display=swap" rel="stylesheet">
</head>
<body style="margin:0;padding:0;background:#f5f4ff;font-family:'Inter',-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;-webkit-font-smoothing:antialiased;">
  <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f5f4ff;padding:32px 16px;">
    <tr><td align="center">
      <table role="presentation" width="100%" style="max-width:520px;background:#ffffff;border-radius:24px;padding:40px;box-shadow:0 8px 32px rgba(98,83,232,.12);">
        <tr><td>

          <!-- Hero: mirrors the site's feed introduction -->
          <p style="margin:0 0 12px;font-size:30px;line-height:1.3em;font-weight:600;color:#20243f;">
            Fresh roles, without the noise.
          </p>
          <p style="margin:0 0 16px;font-size:14px;line-height:1.5;color:#72778f;">
            Your next move starts with a better signal.
          </p>
          <div style="display:inline-block;margin:0 0 28px;padding:5px 9px;border-radius:8px;background:#ebe8ff;">
            <span style="font-size:11px;font-weight:500;color:#6253e8;">&#10022; AI-curated opportunities</span>
          </div>

          <!-- Invitation -->
          <p style="margin:0 0 6px;font-size:18px;font-weight:600;color:#20243f;">
            Your QX Scraper account is ready
          </p>
          <p style="margin:0 0 20px;font-size:14px;line-height:1.6;color:#72778f;">
            An account has been created for <strong style="color:#20243f;">` + html.EscapeString(to) + `</strong>.
            Use these details to sign in:
          </p>

          <!-- Credentials -->
          <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f8f7ff;border:1px solid #ebe8ff;border-radius:16px;margin:0 0 24px;">
            <tr><td style="padding:16px 20px;">
              <p style="margin:0;font-size:11px;letter-spacing:.08em;text-transform:uppercase;color:#72778f;">Email</p>
              <p style="margin:4px 0 0;font-size:15px;font-weight:600;color:#20243f;">` + html.EscapeString(to) + `</p>
            </td></tr>
            <tr><td style="padding:0 20px 16px;">
              <p style="margin:0;font-size:11px;letter-spacing:.08em;text-transform:uppercase;color:#72778f;">Temporary password</p>
              <p style="margin:4px 0 0;font-size:18px;font-weight:700;color:#6253e8;font-family:ui-monospace,Menlo,Consolas,monospace;letter-spacing:.04em;">` + html.EscapeString(tempPassword) + `</p>
            </td></tr>
          </table>

          <!-- Sign-in button -->
          <table role="presentation" cellpadding="0" cellspacing="0" style="margin:0 0 24px;">
            <tr><td style="background:#6253e8;border-radius:999px;">
              <a href="` + loginURL + `" style="display:inline-block;padding:14px 32px;font-size:15px;font-weight:600;color:#ffffff;text-decoration:none;">
                Sign in to QX Scraper
              </a>
            </td></tr>
          </table>

          <p style="margin:0 0 8px;font-size:13px;line-height:1.6;color:#72778f;">
            For your security, please change this password after your first
            sign-in (<strong style="color:#20243f;">Account &rarr; Change password</strong>).
            The password was generated for you and is stored only as a secure hash.
          </p>
          <p style="margin:0;font-size:13px;line-height:1.6;color:#72778f;">
            Best,<br>The QX Scraper team
          </p>

        </td></tr>
      </table>
      <p style="margin:16px 0 0;font-size:11px;color:#9aa1b4;text-align:center;">
        If you weren&rsquo;t expecting this email, you can safely ignore it.
      </p>
    </td></tr>
  </table>
</body>
</html>`
}

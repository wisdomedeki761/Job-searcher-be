// Package mailer sends outbound HTML email over SMTP. It is used to deliver
// generated credentials when the super admin creates a user account.
//
// Delivery uses STARTTLS on the configured port (587 by default), which is
// what Gmail app passwords, Mailgun, Postmark, AWS SES SMTP and most
// providers expect. When SMTPHost is empty the mailer is disabled and callers
// fall back to returning credentials in the API response.
package mailer

import (
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
)

// Mailer sends HTML emails. A zero/disabled mailer (empty host) is a no-op.
type Mailer struct {
	host     string
	port     int
	username string
	password string
	from     string // display form, e.g. `QX Scraper <no-reply@example.com>`
	log      *slog.Logger
}

// New creates a Mailer. host == "" disables sending (Enabled() == false).
func New(host string, port int, username, password, from string, log *slog.Logger) *Mailer {
	if port == 0 {
		port = 587
	}
	return &Mailer{
		host:     strings.TrimSpace(host),
		port:     port,
		username: username,
		password: password,
		from:     from,
		log:      log,
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
	addr := net.JoinHostPort(m.host, strconv.Itoa(m.port))

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

	if err := smtp.SendMail(addr, auth, fromAddr, []string{to}, []byte(msg)); err != nil {
		return fmt.Errorf("smtp send: %w", err)
	}
	m.log.Info("email sent", "to", to, "subject", subject)
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

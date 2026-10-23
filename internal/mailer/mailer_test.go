package mailer

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

func testMailer(host string) *Mailer {
	return New(host, 587, "u", "p", `QX Scraper <no-reply@example.com>`, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestWelcomeHTMLDesign guards the invitation layout: hero copy + chip from
// the product design, palette colors, credentials block and the sign-in
// button pointing at APP_URL/login.
func TestWelcomeHTMLDesign(t *testing.T) {
	html := welcomeHTML("jane@example.com", "kT7mQx9LpWz2Ra", "https://qx-job-searcher.vercel.app")

	want := []string{
		// Design language (matches ui-design/DESIGN-SPEC.md)
		"Fresh roles, without the noise.",
		"Your next move starts with a better signal.",
		"AI-curated opportunities",
		"#20243f", // heading
		"#72778f", // muted
		"#6253e8", // accent
		"#ebe8ff", // chip background
		"Inter",   // font
		// Payload
		"jane@example.com",
		"kT7mQx9LpWz2Ra",
		// Sign-in button → deployed frontend
		`href="https://qx-job-searcher.vercel.app/login"`,
		"Sign in to QX Scraper",
	}
	for _, w := range want {
		if !strings.Contains(html, w) {
			t.Errorf("welcome email missing %q", w)
		}
	}

	// APP_URL trailing slashes must not leak into the button link.
	if strings.Contains(html, ".app//login") {
		t.Errorf("double slash in login URL")
	}
}

// TestWelcomeHTMLEscapes keeps user-controlled values (email, generated
// password, APP_URL) from injecting markup into the email.
func TestWelcomeHTMLEscapes(t *testing.T) {
	html := welcomeHTML(`<b>x@y.com</b>`, `pw"<>`, `https://x.example/"onload`)
	if strings.Contains(html, "<b>x@y.com</b>") {
		t.Errorf("email not escaped")
	}
	if strings.Contains(html, `pw"<>`) {
		t.Errorf("password not escaped")
	}
}

// TestEnabled checks the SMTP-disabled fallback: with no host the mailer is
// disabled and SendHTML reports it instead of dialing nowhere.
func TestEnabled(t *testing.T) {
	on := testMailer("smtp.example.com")
	if !on.Enabled() {
		t.Errorf("Enabled() = false with host set")
	}
	off := testMailer("")
	if off.Enabled() {
		t.Errorf("Enabled() = true with empty host")
	}
	if err := off.SendHTML("a@b.co", "s", "<p>x</p>"); err == nil {
		t.Errorf("SendHTML() error = nil, want not-configured error")
	}
}

// TestFromAddress parses the "Name <addr>" display form for MAIL FROM.
func TestFromAddress(t *testing.T) {
	m := testMailer("smtp.example.com")
	if got := m.fromAddress(); got != "no-reply@example.com" {
		t.Errorf("fromAddress() = %q, want no-reply@example.com", got)
	}
	plain := New("h", 587, "", "", "bare@example.com", m.log)
	if got := plain.fromAddress(); got != "bare@example.com" {
		t.Errorf("fromAddress() = %q, want bare@example.com", got)
	}
}

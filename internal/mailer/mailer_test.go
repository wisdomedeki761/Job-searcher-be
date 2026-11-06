package mailer

import (
	"bufio"
	"io"
	"log/slog"
	"net"
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

func TestSendHTMLFallsBackToSecondPort(t *testing.T) {
	primary, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	primaryPort := primary.Addr().(*net.TCPAddr).Port
	if err := primary.Close(); err != nil {
		t.Fatal(err)
	}

	fallback, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer fallback.Close()
	fallbackPort := fallback.Addr().(*net.TCPAddr).Port

	go func() {
		conn, err := fallback.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		_, _ = io.WriteString(conn, "220 localhost ESMTP\r\n")
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			switch {
			case strings.HasPrefix(strings.ToUpper(line), "DATA"):
				_, _ = io.WriteString(conn, "354 end with dot\r\n")
				for {
					line, err = reader.ReadString('\n')
					if err != nil {
						return
					}
					if line == ".\r\n" {
						_, _ = io.WriteString(conn, "250 accepted\r\n")
						break
					}
				}
			default:
				_, _ = io.WriteString(conn, "250 localhost\r\n")
			}
		}
	}()

	m := NewWithFallback(
		"127.0.0.1",
		primaryPort,
		fallbackPort,
		"",
		"",
		"sender@example.com",
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err := m.SendHTML("recipient@example.com", "test", "<p>hello</p>"); err != nil {
		t.Fatalf("SendHTML() with fallback port %d failed: %v", fallbackPort, err)
	}
}

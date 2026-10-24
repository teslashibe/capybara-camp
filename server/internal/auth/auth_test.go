package auth

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/teslashibe/capybara-camp/server/internal/testdb"
)

type mailbox struct {
	mu   sync.Mutex
	code string
}

func (m *mailbox) Send(_ context.Context, _, _, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.code = regexp.MustCompile(`<strong>([0-9]+)</strong>`).FindStringSubmatch(body)[1]
	return nil
}
func TestCodeOneUseAndDeviceRevocation(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	mail := &mailbox{}
	s := New(pool, strings.Repeat("test", 10), "https://camp.example", mail)
	if err := s.Send(ctx, "member@example.com"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	out := make(chan Login, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := s.Verify(ctx, "member@example.com", mail.code, "Test Mac")
			if err == nil {
				out <- value
			} else if !errors.Is(err, ErrUnauthorized) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	close(out)
	var login Login
	count := 0
	for value := range out {
		count++
		login = value
	}
	if count != 1 {
		t.Fatalf("one code authenticated %d devices", count)
	}
	actor, err := s.Authenticate(ctx, login.Token)
	if err != nil || actor.UserID != login.Actor.UserID {
		t.Fatalf("authenticate: %v", err)
	}
	if err = s.Revoke(ctx, actor); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(ctx, login.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked device authenticated: %v", err)
	}
	if _, err = s.Renew(ctx, actor); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked device renewed: %v", err)
	}
}
func TestWrongAttemptsAndResendLimit(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	mail := &mailbox{}
	s := New(pool, strings.Repeat("test", 10), "https://camp.example", mail)
	if err := s.Send(ctx, "member@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(ctx, "member@example.com"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("resend not limited: %v", err)
	}
	wrong := "000000"
	if mail.code == wrong {
		wrong = "111111"
	}
	for i := 0; i < 5; i++ {
		if _, err := s.Verify(ctx, "member@example.com", wrong, "Test Mac"); !errors.Is(err, ErrUnauthorized) {
			t.Fatal(err)
		}
	}
	if _, err := s.Verify(ctx, "member@example.com", mail.code, "Test Mac"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("exhausted code accepted")
	}
	var hash []byte
	if err := pool.QueryRow(ctx, `SELECT code_hash FROM auth_challenges`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(hash), mail.code) {
		t.Fatal("stored plaintext code")
	}
}
func TestEmailNormalization(t *testing.T) {
	for _, email := range []string{"bad", "Name <member@example.com>", "x@example.com\r\nBcc: attacker@example.com"} {
		if _, err := NormalizeEmail(email); err == nil {
			t.Fatalf("accepted %q", email)
		}
	}
	if email, err := NormalizeEmail(" MEMBER@EXAMPLE.COM "); err != nil || email != "member@example.com" {
		t.Fatalf("normalization %q %v", email, err)
	}
}

func TestCodeFailuresAccumulateAcrossResends(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	mail := &mailbox{}
	s := New(pool, strings.Repeat("test", 10), "https://camp.example", mail)
	const email = "target@example.com"
	wrongFor := func() string {
		if mail.code == "000000" {
			return "111111"
		}
		return "000000"
	}
	for round := 0; round*5 < maxDailyCodeFailures; round++ {
		if round > 0 {
			// Each resend issues a fresh code with a fresh per-code count.
			if _, err := pool.Exec(ctx, `UPDATE auth_challenges SET sent_at=now()-interval '2 minutes',sent_count=0 WHERE email=$1`, email); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Send(ctx, email); err != nil {
			t.Fatal(err)
		}
		for range 5 {
			if _, err := s.Verify(ctx, email, wrongFor(), "Test Mac"); !errors.Is(err, ErrUnauthorized) {
				t.Fatal(err)
			}
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE auth_challenges SET sent_at=now()-interval '2 minutes',sent_count=0 WHERE email=$1`, email); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(ctx, email); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(ctx, email, mail.code, "Test Mac"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("a fresh code reset the daily guess limit", err)
	}
}

func TestRestrictedSignInRefusesOtherAddresses(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	mail := &mailbox{}
	s := New(pool, strings.Repeat("test", 10), "https://camp.example", mail)
	// A device created before the restriction stops working once it applies.
	if err := s.Send(ctx, "outsider@example.com"); err != nil {
		t.Fatal(err)
	}
	earlier, err := s.Verify(ctx, "outsider@example.com", mail.code, "Old Mac")
	if err != nil {
		t.Fatal(err)
	}
	s.RestrictTo([]string{"member@example.com"})
	if _, err = s.Authenticate(ctx, earlier.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("outsider device authenticated: %v", err)
	}
	mail.code = ""
	if err = s.Send(ctx, "other@example.com"); !errors.Is(err, ErrUnauthorized) || mail.code != "" {
		t.Fatalf("code sent to outsider: %v %q", err, mail.code)
	}
	if _, err = s.Verify(ctx, "other@example.com", "000000", "Mac"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("outsider verified: %v", err)
	}
	if err = s.Send(ctx, "member@example.com"); err != nil || mail.code == "" {
		t.Fatalf("member code: %v", err)
	}
	login, err := s.Verify(ctx, "member@example.com", mail.code, "Mac")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(ctx, login.Token); err != nil {
		t.Fatal(err)
	}
}

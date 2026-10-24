package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	magiclink "github.com/teslashibe/magiclink-auth-go"
)

var ErrUnauthorized = errors.New("sign in required")
var ErrRateLimited = errors.New("try again later")

type Actor struct {
	UserID   string `json:"user_id"`
	DeviceID string `json:"device_id"`
	Email    string `json:"email"`
	Name     string `json:"name"`
}
type Login struct {
	Actor     Actor     `json:"actor"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}
type Service struct {
	pool   *pgxpool.Pool
	secret []byte
	login  *magiclink.Service
	// Nil admits any address; otherwise only these normalized addresses.
	allowed map[string]bool
}

func New(pool *pgxpool.Pool, secret, publicURL string, sender magiclink.EmailSender) *Service {
	s := &Service{pool: pool, secret: []byte(secret)}
	s.login = magiclink.New(magiclink.Config{JWTSecret: secret, AppURL: publicURL, AppName: "Capybara Camp"}, s, s, sender, codeEmail{})
	return s
}

// RestrictTo limits sign-in to normalized addresses. Codes are not sent to
// other addresses, and existing device tokens for them stop authenticating.
func (s *Service) RestrictTo(emails []string) {
	if len(emails) == 0 {
		return
	}
	s.allowed = make(map[string]bool, len(emails))
	for _, email := range emails {
		s.allowed[email] = true
	}
}

func (s *Service) admits(email string) bool { return s.allowed == nil || s.allowed[email] }

func NormalizeEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value || len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("enter a valid email address")
	}
	return value, nil
}

// Limit stores an HMAC of the scope rather than raw network addresses.
func (s *Service) Limit(ctx context.Context, scope string, maximum int) error {
	var hits int
	err := s.pool.QueryRow(ctx, `INSERT INTO auth_limits(key_hash,window_start,hits) VALUES($1,now(),1)
	 ON CONFLICT(key_hash) DO UPDATE SET
	 hits=CASE WHEN auth_limits.window_start < now()-interval '10 minutes' THEN 1 ELSE auth_limits.hits+1 END,
	 window_start=CASE WHEN auth_limits.window_start < now()-interval '10 minutes' THEN now() ELSE auth_limits.window_start END
	 RETURNING hits`, s.digest(scope)).Scan(&hits)
	if err != nil {
		return err
	}
	if hits > maximum {
		return ErrRateLimited
	}
	return nil
}

func (s *Service) Send(ctx context.Context, email string) error {
	if !s.admits(email) {
		return ErrUnauthorized
	}
	return s.login.Send(ctx, email)
}

func (s *Service) Verify(ctx context.Context, email, code, deviceName string) (Login, error) {
	deviceName = strings.TrimSpace(deviceName)
	if len(deviceName) == 0 || len(deviceName) > 80 {
		return Login{}, errors.New("device name must be 1 to 80 characters")
	}
	if !s.admits(email) {
		return Login{}, ErrUnauthorized
	}
	result, err := s.login.VerifyCode(ctx, email, code)
	if err != nil {
		return Login{}, ErrUnauthorized
	}
	token, err := Token()
	if err != nil {
		return Login{}, err
	}
	a := Actor{UserID: result.UserID, DeviceID: uuid.NewString(), Email: result.Email, Name: result.DisplayName}
	var expiry time.Time
	err = s.pool.QueryRow(ctx, `INSERT INTO devices(id,user_id,name,token_hash,expires_at) VALUES($1,$2,$3,$4,now()+interval '30 days') RETURNING expires_at`, a.DeviceID, a.UserID, deviceName, HashToken(token)).Scan(&expiry)
	return Login{Actor: a, Token: token, ExpiresAt: expiry}, err
}

func (s *Service) Authenticate(ctx context.Context, token string) (Actor, error) {
	if len(token) != 48 || !strings.HasPrefix(token, "camp_") {
		return Actor{}, ErrUnauthorized
	}
	var a Actor
	err := s.pool.QueryRow(ctx, `SELECT u.id,d.id,u.email,u.display_name FROM devices d JOIN users u ON u.id=d.user_id WHERE d.token_hash=$1 AND d.revoked_at IS NULL AND d.expires_at>now()`, HashToken(token)).Scan(&a.UserID, &a.DeviceID, &a.Email, &a.Name)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !s.admits(a.Email) {
		return Actor{}, ErrUnauthorized
	}
	return a, err
}

func (s *Service) Revoke(ctx context.Context, a Actor) error {
	_, err := s.pool.Exec(ctx, `UPDATE devices SET revoked_at=now() WHERE id=$1 AND user_id=$2`, a.DeviceID, a.UserID)
	return err
}

func (s *Service) Renew(ctx context.Context, a Actor) (time.Time, error) {
	var expiry time.Time
	err := s.pool.QueryRow(ctx, `UPDATE devices SET expires_at=LEAST(now()+interval '30 days',created_at+interval '90 days'),last_seen_at=now()
	 WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL AND expires_at>now() AND created_at>now()-interval '90 days' RETURNING expires_at`, a.DeviceID, a.UserID).Scan(&expiry)
	if errors.Is(err, pgx.ErrNoRows) {
		return expiry, ErrUnauthorized
	}
	return expiry, err
}

func Token() (string, error) {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return "camp_" + base64.RawURLEncoding.EncodeToString(bytes[:]), nil
}
func HashToken(token string) []byte { sum := sha256.Sum256([]byte(token)); return sum[:] }
func (s *Service) digest(value string) []byte {
	h := hmac.New(sha256.New, s.secret)
	h.Write([]byte(value))
	return h.Sum(nil)
}

// The magiclink engine owns code generation and validation sequencing. Camp's
// store adds atomic consumption, keyed hashes and a persistent attempt limit.
func (s *Service) Create(ctx context.Context, email, code, _ string, expires time.Time) error {
	var one int
	err := s.pool.QueryRow(ctx, `INSERT INTO auth_challenges(email,code_hash,expires_at) VALUES($1,$2,$3)
	 ON CONFLICT(email) DO UPDATE SET code_hash=EXCLUDED.code_hash,expires_at=EXCLUDED.expires_at,
	 consumed_at=NULL,attempts=0,sent_at=now(),
	 sent_count=CASE WHEN auth_challenges.window_start<now()-interval '1 hour' THEN 1 ELSE auth_challenges.sent_count+1 END,
	 window_start=CASE WHEN auth_challenges.window_start<now()-interval '1 hour' THEN now() ELSE auth_challenges.window_start END
	 WHERE auth_challenges.sent_at<now()-interval '60 seconds' AND (auth_challenges.sent_count<5 OR auth_challenges.window_start<now()-interval '1 hour') RETURNING 1`, email, s.digest(email+"\x00"+code), expires).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRateLimited
	}
	return err
}

// About one in 50,000 chance per address per day of guessing a six-digit code.
const maxDailyCodeFailures = 20

func (s *Service) ConsumeByCode(ctx context.Context, email, code string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var hash []byte
	var expired, consumed bool
	var attempts int
	err = tx.QueryRow(ctx, `SELECT code_hash,expires_at<=now(),consumed_at IS NOT NULL,attempts FROM auth_challenges WHERE email=$1 FOR UPDATE`, email).Scan(&hash, &expired, &consumed, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return magiclink.ErrInvalidCode
	}
	if err != nil {
		return err
	}
	if expired || consumed || attempts >= 5 {
		return magiclink.ErrInvalidCode
	}
	// Each new code resets its own attempt count, so failures are also
	// counted per address across codes for a day.
	failures := s.digest("verify-fail:" + email)
	var failed int
	if err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT hits FROM auth_limits WHERE key_hash=$1 AND window_start>=now()-interval '24 hours'),0)`, failures).Scan(&failed); err != nil {
		return err
	}
	if failed >= maxDailyCodeFailures {
		return magiclink.ErrInvalidCode
	}
	if subtle.ConstantTimeCompare(hash, s.digest(email+"\x00"+code)) != 1 {
		if _, err = tx.Exec(ctx, `UPDATE auth_challenges SET attempts=attempts+1 WHERE email=$1`, email); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO auth_limits(key_hash,window_start,hits) VALUES($1,now(),1)
		 ON CONFLICT(key_hash) DO UPDATE SET
		 hits=CASE WHEN auth_limits.window_start < now()-interval '24 hours' THEN 1 ELSE auth_limits.hits+1 END,
		 window_start=CASE WHEN auth_limits.window_start < now()-interval '24 hours' THEN now() ELSE auth_limits.window_start END`, failures); err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		return magiclink.ErrInvalidCode
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_challenges SET consumed_at=now() WHERE email=$1`, email); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Service) LookupByToken(context.Context, string) (string, string, error) {
	return "", "", magiclink.ErrInvalidToken
}
func (s *Service) UpsertUser(ctx context.Context, _ string, email, name string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `INSERT INTO users(id,email,display_name) VALUES($1,$2,$3) ON CONFLICT(email) DO UPDATE SET email=EXCLUDED.email RETURNING id`, uuid.NewString(), email, name).Scan(&id)
	return id, err
}
func (s *Service) GetUserByEmail(ctx context.Context, email string) (string, string, error) {
	var id, name string
	err := s.pool.QueryRow(ctx, `SELECT id,display_name FROM users WHERE email=$1`, email).Scan(&id, &name)
	return id, name, err
}

type codeEmail struct{}

func (codeEmail) Render(code, _, _ string) (string, string) {
	return "Your Capybara Camp sign-in code", fmt.Sprintf(`<html><body><h1>Sign in to Capybara Camp</h1><p>Your code is <strong>%s</strong></p><p>It expires in ten minutes. If you did not request it, ignore this email.</p></body></html>`, html.EscapeString(code))
}

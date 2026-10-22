package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/database"
	"github.com/teslashibe/capybara-camp/server/internal/gitprovider"
)

type API struct {
	Pool             *pgxpool.Pool
	Auth             *auth.Service
	Camps            *camp.Store
	Git              *camp.GitStore
	GitWebhookSecret string
	Origin           string
	streamMu         sync.Mutex
	streams          map[string]int
	streamCount      int
}
type authorizedHandler func(http.ResponseWriter, *http.Request, auth.Actor)

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]string{"status": "alive"}) })
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if database.Ready(ctx, a.Pool) != nil {
			problem(w, 503, "not_ready")
			return
		}
		write(w, 200, map[string]any{"status": "ready", "protocol": 1})
	})
	mux.HandleFunc("POST /v1/auth/code", a.sendCode)
	mux.HandleFunc("POST /v1/auth/verify", a.verify)
	mux.HandleFunc("GET /v1/me", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) { write(w, 200, u) }))
	mux.HandleFunc("POST /v1/auth/renew", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		exp, err := a.Auth.Renew(r.Context(), u)
		if fail(w, err) {
			return
		}
		write(w, 200, map[string]any{"expires_at": exp})
	}))
	mux.HandleFunc("POST /v1/auth/logout", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		if fail(w, a.Auth.Revoke(r.Context(), u)) {
			return
		}
		w.WriteHeader(204)
	}))
	mux.HandleFunc("GET /v1/camps", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		value, err := a.Camps.List(r.Context(), u)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("POST /v1/camps", a.authorize(a.createCamp))
	mux.HandleFunc("GET /v1/camps/{camp}/snapshot", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		value, err := a.Camps.Snapshot(r.Context(), u, r.PathValue("camp"))
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/projects", a.authorize(a.createProject))
	a.projectRoutes(mux)
	mux.HandleFunc("POST /v1/camps/{camp}/invitations", a.authorize(a.invite))
	mux.HandleFunc("POST /v1/invitations/accept", a.authorize(a.accept))
	mux.HandleFunc("DELETE /v1/camps/{camp}/members/{user}", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		if !fail(w, a.Camps.RemoveMember(r.Context(), u, r.PathValue("camp"), r.Header.Get("Idempotency-Key"), r.PathValue("user"))) {
			w.WriteHeader(204)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/events", a.authorize(a.events))
	a.sessionRoutes(mux)
	a.requestRoutes(mux)
	a.planningRoutes(mux)
	a.notificationRoutes(mux)
	a.accountRoutes(mux)
	a.gitRoutes(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if origin := r.Header.Get("Origin"); origin != "" && origin != a.Origin {
			problem(w, 403, "origin_denied")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Body != nil {
			maximum := int64(64 << 10)
			if strings.HasSuffix(r.URL.Path, "/transcript") {
				maximum = 2 << 20
			}
			if strings.HasSuffix(r.URL.Path, "/requests") {
				maximum = 128 << 10
			}
			if strings.HasPrefix(r.URL.Path, "/v1/runner/logins/") && strings.HasSuffix(r.URL.Path, "/report") {
				maximum = 384 << 10
			}
			if r.URL.Path == "/v1/runner/credentials" || strings.HasSuffix(r.URL.Path, "/events") || strings.HasSuffix(r.URL.Path, "/controls") {
				maximum = 384 << 10
			}
			r.Body = http.MaxBytesReader(w, r.Body, maximum)
			if r.ContentLength != 0 && strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
				problem(w, 415, "json_required")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func problem(w http.ResponseWriter, status int, code string) {
	write(w, status, map[string]string{"error": code})
}
func fail(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, gitprovider.ErrAuthorization):
		problem(w, 409, "git_sign_in_required")
	case errors.Is(err, gitprovider.ErrAccess):
		problem(w, 403, "git_access_unavailable")
	case errors.Is(err, gitprovider.ErrUnavailable):
		problem(w, 503, "git_unavailable")
	case errors.Is(err, gitprovider.ErrResponse):
		problem(w, 502, "git_response_invalid")
	case errors.Is(err, camp.ErrCloudUnavailable):
		problem(w, 503, "cloud_accounts_unavailable")
	case errors.Is(err, auth.ErrUnauthorized):
		problem(w, 401, "sign_in_required")
	case errors.Is(err, auth.ErrRateLimited):
		problem(w, 429, "try_later")
	case errors.Is(err, camp.ErrDenied):
		problem(w, 404, "not_available")
	case errors.Is(err, camp.ErrConflict):
		problem(w, 409, "request_conflict")
	case errors.Is(err, camp.ErrCursor):
		problem(w, 409, "snapshot_required")
	case errors.Is(err, camp.ErrRevision):
		problem(w, 409, "revision_conflict")
	case errors.Is(err, camp.ErrTransition):
		problem(w, 409, "request_state_changed")
	case errors.Is(err, camp.ErrPlanningBudget):
		problem(w, 409, "token_ceiling_unavailable")
	case errors.Is(err, camp.ErrInput):
		problem(w, 400, "invalid_input")
	default:
		problem(w, 503, "temporarily_unavailable")
	}
	return true
}
func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			problem(w, 413, "body_too_large")
			return false
		}
		problem(w, 400, "invalid_json")
		return false
	}
	if err := d.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		problem(w, 400, "invalid_json")
		return false
	}
	return true
}
func token(r *http.Request) string {
	scheme, value, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || scheme != "Bearer" {
		return ""
	}
	return value
}
func (a *API) authorize(next authorizedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := a.Auth.Authenticate(r.Context(), token(r))
		if fail(w, err) {
			return
		}
		if r.Method != "GET" {
			maximum := 100
			scope := "write:" + u.UserID
			if strings.HasSuffix(r.URL.Path, "/transcript") || (r.Method == "PUT" && strings.Contains(r.URL.Path, "/sessions/")) {
				maximum = 12000
				scope = "sync:" + u.DeviceID
			}
			if strings.Contains(r.URL.Path, "/requests/") {
				maximum = 1200
				scope = "requests:" + u.DeviceID
			}
			if fail(w, a.Auth.Limit(r.Context(), scope, maximum)) {
				return
			}
		}
		next(w, r, u)
	}
}
func (a *API) authLimit(w http.ResponseWriter, r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	// Forwarded headers are deliberately ignored without a configured trusted proxy.
	return !fail(w, a.Auth.Limit(r.Context(), "login:"+host, 30))
}
func (a *API) sendCode(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email string `json:"email"`
	}
	if !decode(w, r, &input) {
		return
	}
	email, err := auth.NormalizeEmail(input.Email)
	if err != nil {
		problem(w, 400, "invalid_email")
		return
	}
	if !a.authLimit(w, r) {
		return
	}
	// Identical response for accepted, throttled and failed deliveries. A retry
	// cannot reveal whether this email already belongs to a Camp member.
	_ = a.Auth.Send(r.Context(), email)
	write(w, 202, map[string]string{"status": "check_email"})
}
func (a *API) verify(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email      string `json:"email"`
		Code       string `json:"code"`
		DeviceName string `json:"device_name"`
	}
	if !decode(w, r, &input) {
		return
	}
	email, err := auth.NormalizeEmail(input.Email)
	if err != nil || len(input.Code) != 6 || len(strings.TrimSpace(input.DeviceName)) == 0 || len(input.DeviceName) > 80 {
		problem(w, 400, "invalid_input")
		return
	}
	if !a.authLimit(w, r) {
		return
	}
	value, err := a.Auth.Verify(r.Context(), email, input.Code, input.DeviceName)
	if !fail(w, err) {
		write(w, 200, value)
	}
}
func (a *API) createCamp(w http.ResponseWriter, r *http.Request, u auth.Actor) {
	var input struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &input) {
		return
	}
	value, err := a.Camps.Create(r.Context(), u, r.Header.Get("Idempotency-Key"), input.Name)
	if !fail(w, err) {
		write(w, 201, value)
	}
}
func (a *API) createProject(w http.ResponseWriter, r *http.Request, u auth.Actor) {
	var input struct {
		Name         string   `json:"name"`
		Repositories []string `json:"repositories"`
	}
	if !decode(w, r, &input) {
		return
	}
	value, err := a.Camps.CreateProjectWithRepositories(r.Context(), u, r.PathValue("camp"), r.Header.Get("Idempotency-Key"), input.Name, input.Repositories)
	if !fail(w, err) {
		write(w, 201, value)
	}
}
func (a *API) invite(w http.ResponseWriter, r *http.Request, u auth.Actor) {
	var input struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if !decode(w, r, &input) {
		return
	}
	value, err := a.Camps.Invite(r.Context(), u, r.PathValue("camp"), r.Header.Get("Idempotency-Key"), input.Email, input.Role)
	if !fail(w, err) {
		write(w, 201, value)
	}
}
func (a *API) accept(w http.ResponseWriter, r *http.Request, u auth.Actor) {
	var input struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &input) {
		return
	}
	id, err := a.Camps.Accept(r.Context(), u, r.Header.Get("Idempotency-Key"), input.Token)
	if !fail(w, err) {
		write(w, 200, map[string]string{"camp_id": id})
	}
}
func (a *API) events(w http.ResponseWriter, r *http.Request, u auth.Actor) {
	if !a.reserveStream(u.DeviceID) {
		problem(w, 429, "too_many_streams")
		return
	}
	defer a.releaseStream(u.DeviceID)
	after := int64(0)
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("after")
	}
	if raw != "" {
		var err error
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || after < 0 {
			problem(w, 400, "invalid_cursor")
			return
		}
	}
	ctx := r.Context()
	items, err := a.Camps.Events(ctx, u, r.PathValue("camp"), after)
	if fail(w, err) {
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	timer := time.NewTicker(200 * time.Millisecond)
	defer timer.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	lifetime := time.NewTimer(5 * time.Minute)
	defer lifetime.Stop()
	for {
		for _, event := range items {
			data, err := json.Marshal(event)
			if err != nil {
				return
			}
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err = fmt.Fprintf(w, "id: %d\nevent: update\ndata: %s\n\n", event.Cursor, data); err != nil {
				return
			}
			after = event.Cursor
		}
		if err = controller.Flush(); err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-lifetime.C:
			return
		case <-heartbeat.C:
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err = io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			if controller.Flush() != nil {
				return
			}
		case <-timer.C:
		}
		if _, err = a.Auth.Authenticate(ctx, token(r)); err != nil {
			return
		}
		items, err = a.Camps.Events(ctx, u, r.PathValue("camp"), after)
		if err != nil {
			return
		}
	}
}

func (a *API) reserveStream(device string) bool {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	if a.streams == nil {
		a.streams = make(map[string]int)
	}
	if a.streamCount >= 128 || a.streams[device] >= 4 {
		return false
	}
	a.streams[device]++
	a.streamCount++
	return true
}
func (a *API) releaseStream(device string) {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	if a.streams[device] <= 1 {
		delete(a.streams, device)
	} else {
		a.streams[device]--
	}
	a.streamCount--
}

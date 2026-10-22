package httpapi

import (
	"net/http"
	"strconv"

	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
)

func (a *API) sessionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/camps/{camp}/sessions/{session}/children", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		revision, ok := queryInt(w, r, "revision")
		if !ok {
			return
		}
		value, err := a.Camps.ChildHistories(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), revision)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/sessions/{session}/children/{child}/transcript", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.ChildHistoryInput
		if !decode(w, r, &input) {
			return
		}
		value, err := a.Camps.WriteChildHistory(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.PathValue("child"), r.Header.Get("Idempotency-Key"), input)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/sessions/{session}/children/{child}/transcript", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		limit, ok := pageLimit(w, r)
		if !ok {
			return
		}
		revision, ok := queryInt(w, r, "revision")
		if !ok {
			return
		}
		before, ok := queryInt(w, r, "before")
		if !ok {
			return
		}
		value, err := a.Camps.ChildTranscript(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.PathValue("child"), revision, before, limit)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/sessions/{session}/message", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.SourceRef
		if !decode(w, r, &input) {
			return
		}
		value, err := a.Camps.ReadMessage(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), input)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/sessions/{session}/context", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.SourceRef
		if !decode(w, r, &input) {
			return
		}
		value, err := a.Camps.ReadContext(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), input)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/sessions/{session}/ownership", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.OwnershipInput
		if !decode(w, r, &input) {
			return
		}
		value, err := a.Camps.AdoptSession(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.Header.Get("Idempotency-Key"), input)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("PATCH /v1/camps/{camp}/sessions/{session}/board", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input struct {
			Stage            string `json:"stage"`
			ExpectedRevision int64  `json:"expected_revision"`
		}
		if !decode(w, r, &input) {
			return
		}
		value, err := a.Camps.MoveSession(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.Header.Get("Idempotency-Key"), input.Stage, input.ExpectedRevision)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("POST /v1/profiles", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.ProfileInput
		if !decode(w, r, &input) {
			return
		}
		value, err := a.Camps.RegisterProfile(r.Context(), u, r.Header.Get("Idempotency-Key"), input)
		if !fail(w, err) {
			write(w, 201, value)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/sessions", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		limit, ok := pageLimit(w, r)
		if !ok {
			return
		}

		value, err := a.Camps.ListSessions(r.Context(), u, r.PathValue("camp"), r.URL.Query().Get("q"), r.URL.Query().Get("before"), limit)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/sessions/{session}", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		value, err := a.Camps.Session(r.Context(), u, r.PathValue("camp"), r.PathValue("session"))
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("PUT /v1/camps/{camp}/sessions/{session}", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.SessionInput
		if !decode(w, r, &input) {
			return
		}
		if input.ID != "" && input.ID != r.PathValue("session") {
			problem(w, 400, "invalid_input")
			return
		}
		input.ID = r.PathValue("session")
		value, err := a.Camps.PublishSession(r.Context(), u, r.PathValue("camp"), r.Header.Get("Idempotency-Key"), input)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/sessions/{session}/transcript", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.TranscriptInput
		if !decode(w, r, &input) {
			return
		}
		value, err := a.Camps.WriteTranscript(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.Header.Get("Idempotency-Key"), input)
		if !fail(w, err) {
			write(w, 200, map[string]int64{"revision": value})
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/sessions/{session}/transcript", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		limit, ok := pageLimit(w, r)
		if !ok {
			return
		}
		revision, ok := queryInt(w, r, "revision")
		if !ok {
			return
		}
		before, ok := queryInt(w, r, "before")
		if !ok {
			return
		}
		value, err := a.Camps.Transcript(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), revision, before, limit)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
}
func pageLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	limit := int64(100)
	if r.URL.Query().Has("limit") {
		var ok bool
		limit, ok = queryInt(w, r, "limit")
		if !ok {
			return 0, false
		}
	}
	if limit < 1 || limit > 200 {
		problem(w, 400, "invalid_limit")
		return 0, false
	}
	return int(limit), true
}
func queryInt(w http.ResponseWriter, r *http.Request, key string) (int64, bool) {
	if !r.URL.Query().Has(key) {
		return 0, true
	}
	n, err := strconv.ParseInt(r.URL.Query().Get(key), 10, 64)
	if err != nil || n < 0 {
		problem(w, 400, "invalid_cursor")
		return 0, false
	}
	return n, true
}

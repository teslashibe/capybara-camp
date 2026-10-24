package httpapi

import (
	"net/http"
	"strconv"

	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
)

func (a *API) requestRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/camps/{camp}/requests/{request}/continuation-events", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var in camp.LocalPlanningEvent
		if !decode(w, r, &in) {
			return
		}
		seq, err := a.Camps.AppendLocalContinuationEvent(r.Context(), u, r.PathValue("camp"), r.PathValue("request"), in)
		if !fail(w, err) {
			write(w, 200, map[string]int64{"seq": seq})
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/requests/{request}/planning-events", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.LocalPlanningEvent
		if !decode(w, r, &input) {
			return
		}
		seq, err := a.Camps.AppendLocalPlanningEvent(r.Context(), u, r.PathValue("camp"), r.PathValue("request"), input)
		if !fail(w, err) {
			write(w, 200, map[string]int64{"seq": seq})
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/requests/{request}/controls/{control}", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		out, err := a.Camps.ControlStatus(r.Context(), u, r.PathValue("camp"), r.PathValue("request"), r.PathValue("control"))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/sessions/{session}/stream", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		out, err := a.Camps.SessionStream(r.Context(), u, r.PathValue("camp"), r.PathValue("session"))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/requests/{request}/controls", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var in camp.ControlInput
		if !decode(w, r, &in) {
			return
		}
		out, err := a.Camps.SendControl(r.Context(), u, r.PathValue("camp"), r.PathValue("request"), in)
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/requests/{request}/events", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		after := int64(0)
		var err error
		if value := r.URL.Query().Get("after"); value != "" {
			after, err = strconv.ParseInt(value, 10, 64)
		}
		if err != nil {
			problem(w, 400, "invalid_input")
			return
		}
		out, err := a.Camps.RequestStream(r.Context(), u, r.PathValue("camp"), r.PathValue("request"), after)
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/sessions/{session}/requests", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.RequestInput
		if !decode(w, r, &input) {
			return
		}
		value, err := a.Camps.CreateRequest(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.Header.Get("Idempotency-Key"), input)
		if !fail(w, err) {
			write(w, 201, value)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/requests", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		value, err := a.Camps.ListRequests(r.Context(), u, r.PathValue("camp"))
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/requests/{request}", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		value, err := a.Camps.Request(r.Context(), u, r.PathValue("camp"), r.PathValue("request"))
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/requests/claim", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input struct {
			SessionIDs            *[]string `json:"session_ids"`
			IncludePlanning       bool      `json:"include_planning"`
			IncludeImplementation bool      `json:"include_implementation"`
		}
		if (r.ContentLength != 0 || len(r.TransferEncoding) != 0) && !decode(w, r, &input) {
			return
		}
		var value *camp.Request
		var err error
		if input.IncludeImplementation {
			if input.SessionIDs == nil {
				problem(w, 400, "invalid_input")
				return
			}
			value, err = a.Camps.ClaimImplementationWork(r.Context(), u, r.PathValue("camp"), r.Header.Get("Idempotency-Key"), *input.SessionIDs)
		} else if input.IncludePlanning {
			if input.SessionIDs == nil {
				problem(w, 400, "invalid_input")
				return
			}
			value, err = a.Camps.ClaimAvailableWork(r.Context(), u, r.PathValue("camp"), r.Header.Get("Idempotency-Key"), *input.SessionIDs)
		} else if input.SessionIDs == nil {
			value, err = a.Camps.ClaimRequest(r.Context(), u, r.PathValue("camp"), r.Header.Get("Idempotency-Key"))
		} else {
			value, err = a.Camps.ClaimAvailableRequest(r.Context(), u, r.PathValue("camp"), r.Header.Get("Idempotency-Key"), *input.SessionIDs)
		}
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/requests/{request}/actions", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.RequestAction
		if !decode(w, r, &input) {
			return
		}
		value, err := a.Camps.ActOnRequest(r.Context(), u, r.PathValue("camp"), r.PathValue("request"), r.Header.Get("Idempotency-Key"), input)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
}

package httpapi

import (
	"net/http"
	"strconv"

	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
)

func (a *API) handoffRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/camps/{camp}/sessions/{session}/transfers/{transfer}/handoffs", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var in camp.HandoffInput
		if !decode(w, r, &in) {
			return
		}
		out, err := a.Camps.BeginHandoff(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.PathValue("transfer"), r.Header.Get("Idempotency-Key"), in)
		if !fail(w, err) {
			write(w, 201, out)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/sessions/{session}/handoffs/{handoff}", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		out, err := a.Camps.Handoff(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.PathValue("handoff"))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/sessions/{session}/handoffs/{handoff}/release", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		if !decode(w, r, &struct{}{}) {
			return
		}
		out, err := a.Camps.ReleaseHandoff(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.PathValue("handoff"))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/handoffs/next", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		id, err := a.Camps.NextLocalHandoff(r.Context(), u, r.PathValue("camp"))
		if !fail(w, err) {
			if id == "" {
				w.WriteHeader(204)
			} else {
				write(w, 200, map[string]string{"id": id})
			}
		}
	}))
	mux.HandleFunc("GET /v1/runner/handoffs/next", a.runnerAuthorize(func(w http.ResponseWriter, r *http.Request, _ camp.RunnerAccountState) {
		id, err := a.Camps.NextRunnerHandoff(r.Context(), token(r))
		if !fail(w, err) {
			if id == "" {
				w.WriteHeader(204)
			} else {
				write(w, 200, map[string]string{"id": id})
			}
		}
	}))
	for _, runner := range []bool{false, true} {
		prefix := "/v1/handoffs/{handoff}/"
		if runner {
			prefix = "/v1/runner/handoffs/{handoff}/"
		}
		for _, operation := range []string{"claim", "source", "chunks/{ordinal}", "ack", "reject"} {
			handle := func(w http.ResponseWriter, r *http.Request, u auth.Actor, grant string) {
				var value any
				var err error
				if operation == "ack" {
					var in camp.HandoffAck
					if !decode(w, r, &in) {
						return
					}
					value, err = a.Camps.AcknowledgeHandoff(r.Context(), u, grant, r.PathValue("handoff"), in)
				} else {
					var in struct {
						ClaimID string `json:"claim_id"`
					}
					if !decode(w, r, &in) {
						return
					}
					switch operation {
					case "claim":
						value, err = a.Camps.ClaimHandoff(r.Context(), u, grant, r.PathValue("handoff"), in.ClaimID)
					case "source":
						value, err = a.Camps.HandoffSource(r.Context(), u, grant, r.PathValue("handoff"), in.ClaimID)
					case "reject":
						value, err = a.Camps.RejectHandoff(r.Context(), u, grant, r.PathValue("handoff"), in.ClaimID)
					default:
						index, parseErr := strconv.Atoi(r.PathValue("ordinal"))
						if parseErr != nil || index < 0 {
							fail(w, camp.ErrInput)
							return
						}
						value, err = a.Camps.HandoffChunk(r.Context(), u, grant, r.PathValue("handoff"), in.ClaimID, index)
					}
				}
				if !fail(w, err) {
					write(w, 200, value)
				}
			}
			if runner {
				mux.HandleFunc("POST "+prefix+operation, a.runnerAuthorize(func(w http.ResponseWriter, r *http.Request, _ camp.RunnerAccountState) {
					handle(w, r, auth.Actor{}, token(r))
				}))
			} else {
				mux.HandleFunc("POST "+prefix+operation, a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) { handle(w, r, u, "") }))
			}
		}
	}
}

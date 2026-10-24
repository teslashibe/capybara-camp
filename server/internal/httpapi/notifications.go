package httpapi

import (
	"net/http"
	"strconv"

	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
)

func (a *API) notificationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/camps/{camp}/notifications", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		limit := 50
		if value := r.URL.Query().Get("limit"); value != "" {
			var err error
			limit, err = strconv.Atoi(value)
			if err != nil {
				problem(w, 400, "invalid_input")
				return
			}
		}
		value, err := a.Camps.Notifications(r.Context(), u, r.PathValue("camp"), r.URL.Query().Get("before"), limit)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/notifications/{notification}", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		value, err := a.Camps.Notification(r.Context(), u, r.PathValue("camp"), r.PathValue("notification"))
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/notifications/actions", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.NotificationAction
		if !decode(w, r, &input) {
			return
		}
		if !fail(w, a.Camps.ActOnNotifications(r.Context(), u, r.PathValue("camp"), r.Header.Get("Idempotency-Key"), input)) {
			w.WriteHeader(http.StatusNoContent)
		}
	}))
}

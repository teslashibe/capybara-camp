package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeferredMemoryRoutesAreUnavailable(t *testing.T) {
	handler := (&API{}).Handler()
	for _, path := range []string{
		"/v1/camps/fixture/memory/jobs", "/v1/camps/fixture/memory/jobs/fixture",
		"/v1/camps/fixture/memory/jobs/fixture/cancel", "/v1/camps/fixture/memory/memories",
		"/v1/camps/fixture/memory/memories/fixture", "/v1/camps/fixture/memory/observations",
		"/v1/camps/fixture/memory/observations/fixture",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, httptest.NewRequest(method, path, nil))
			if res.Code != http.StatusNotFound {
				t.Fatalf("%s %s: %d", method, path, res.Code)
			}
		}
	}
}

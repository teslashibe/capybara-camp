package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

func TestBoundedJSONRejectsElementFloods(t *testing.T) {
	array := func(n int) string { return `{"versions":[` + strings.TrimSuffix(strings.Repeat(`{},`, n), ",") + `]}` }
	if _, err := boundedJSON(strings.NewReader(array(maxDecodedArray))); err != nil {
		t.Fatal("largest accepted array was refused", err)
	}
	if _, err := boundedJSON(strings.NewReader(array(maxDecodedArray + 1))); !errors.Is(err, errDecodeLimit) {
		t.Fatal("element flood was accepted", err)
	}
	nested := strings.Repeat("[", maxDecodedDepth+1) + strings.Repeat("]", maxDecodedDepth+1)
	if _, err := boundedJSON(strings.NewReader(nested)); !errors.Is(err, errDecodeLimit) {
		t.Fatal("deep nesting was accepted", err)
	}
	// Elements of nested objects count once, at their own array.
	mixed := `{"a":[{"b":[1,2,3],"c":{"d":[4]}},[5,6]]}`
	if _, err := boundedJSON(strings.NewReader(mixed)); err != nil {
		t.Fatal(err)
	}
}

func TestStreamAppendsUseTheirOwnBucket(t *testing.T) {
	for _, path := range []string{"/v1/camps/c/requests/r/implementation-events", "/v1/camps/c/requests/r/continuation-events", "/v1/camps/c/requests/r/planning-events", "/v1/runner/workspaces/w/requests/r/events"} {
		if !streamAppend(path) {
			t.Fatal("stream path not recognised", path)
		}
	}
	for _, path := range []string{"/v1/camps/c/requests/r/actions", "/v1/camps/c/requests/claim", "/v1/camps/c/sessions/s/requests"} {
		if streamAppend(path) {
			t.Fatal("ordinary request path treated as a stream", path)
		}
	}
}

func TestStreamsAndAttachmentsAreCappedPerPerson(t *testing.T) {
	a := &API{}
	for i := range 8 {
		if !a.reserveStream(auth.Actor{UserID: "one", DeviceID: "device-" + string(rune('a'+i/4)) + string(rune('0'+i%4))}) {
			t.Fatal("stream refused below the per-user cap", i)
		}
	}
	if a.reserveStream(auth.Actor{UserID: "one", DeviceID: "fresh-device"}) {
		t.Fatal("one user exceeded eight streams with extra devices")
	}
	if !a.reserveStream(auth.Actor{UserID: "two", DeviceID: "other"}) {
		t.Fatal("another user was blocked")
	}
	a.releaseStream(auth.Actor{UserID: "one", DeviceID: "device-a0"})
	if !a.reserveStream(auth.Actor{UserID: "one", DeviceID: "fresh-device"}) {
		t.Fatal("released stream was not reusable")
	}
	if !a.acquireAttachment("one") || a.acquireAttachment("one") {
		t.Fatal("one person held both attachment slots")
	}
	if !a.acquireAttachment("two") || a.acquireAttachment("three") {
		t.Fatal("global attachment cap changed")
	}
	a.releaseAttachment("one")
	if !a.acquireAttachment("three") {
		t.Fatal("released attachment slot was not reusable")
	}
}

func TestClientAddressTrustsOnlyConfiguredProxies(t *testing.T) {
	request := func(remote, forwarded string) *http.Request {
		r := httptest.NewRequest("POST", "/v1/auth/code", nil)
		r.RemoteAddr = remote
		if forwarded != "" {
			r.Header.Set("X-Forwarded-For", forwarded)
		}
		return r
	}
	plain := &API{}
	if got := plain.clientAddress(request("10.42.0.7:443", "198.51.100.9")); got != "10.42.0.7" {
		t.Fatal("forwarded header trusted without configuration", got)
	}
	proxied := &API{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.42.0.0/16")}}
	if got := proxied.clientAddress(request("10.42.0.7:443", "203.0.113.5, 198.51.100.9")); got != "198.51.100.9" {
		t.Fatal("right-most untrusted hop not used", got)
	}
	if got := proxied.clientAddress(request("10.42.0.7:443", "198.51.100.9, 10.42.0.3")); got != "198.51.100.9" {
		t.Fatal("trusted inner proxy hop not skipped", got)
	}
	if got := proxied.clientAddress(request("192.0.2.4:443", "198.51.100.9")); got != "192.0.2.4" {
		t.Fatal("untrusted peer could choose its own address", got)
	}
	if got := proxied.clientAddress(request("10.42.0.7:443", "not-an-ip")); got != "10.42.0.7" {
		t.Fatal("malformed hop was used", got)
	}
}

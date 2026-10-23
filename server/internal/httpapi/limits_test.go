package httpapi

import (
	"errors"
	"strings"
	"testing"
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

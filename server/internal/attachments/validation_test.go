package attachments

import (
	"strings"
	"testing"
)

func TestReferenceWireValidation(t *testing.T) {
	base := Reference{ID: "a:0/1", Kind: "image", Source: "inline", SourceDigest: strings.Repeat("a", 64), State: "embedded", MediaType: "image/png", SHA256: strings.Repeat("b", 64), Size: 8}
	if !base.Valid() {
		t.Fatal(base)
	}
	for _, change := range []func(*Reference){
		func(r *Reference) { r.ID = "a:../0" }, func(r *Reference) { r.SourceDigest = "https://example.test" }, func(r *Reference) { r.Kind = "executable" },
		func(r *Reference) { r.MediaType = "text/html" }, func(r *Reference) { r.Size = 0 }, func(r *Reference) { r.Size = MaxBytes + 1 },
		func(r *Reference) { r.State = "reference" }, func(r *Reference) { r.Source = "remote" }, func(r *Reference) { r.SHA256 = "not a hash" },
	} {
		r := base
		change(&r)
		if r.Valid() {
			t.Fatal("Accepted invalid descriptor", r)
		}
	}
	for _, state := range []string{"reference", "unsupported", "invalid", "too_large"} {
		r := base
		r.State = state
		r.Size = 0
		r.SHA256 = ""
		if state == "reference" {
			r.Source = "local"
			r.MediaType = ""
		}
		if state == "unsupported" {
			r.MediaType = ""
		}
		if !r.Valid() {
			t.Fatal("Lost unavailable descriptor", r)
		}
	}
}

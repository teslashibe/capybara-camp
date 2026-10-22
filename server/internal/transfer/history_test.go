package transfer

import (
	"strings"
	"testing"
)

func TestHistoryBindingDigestAndLimits(t *testing.T) {
	h := HistoryBinding{Version: 1, ParentID: strings.Repeat("a", 64), MainRevision: strings.Repeat("b", 64), TranscriptRevision: 7, Children: []HistoryChild{{strings.Repeat("d", 64), strings.Repeat("e", 64)}, {strings.Repeat("c", 64), strings.Repeat("f", 64)}}}
	first, err := h.Digest()
	if err != nil {
		t.Fatal(err)
	}
	h.Children[0], h.Children[1] = h.Children[1], h.Children[0]
	second, err := h.Digest()
	if err != nil || first != second {
		t.Fatal("Source ordering changed the binding")
	}
	h.TranscriptRevision++
	second, _ = h.Digest()
	if first == second {
		t.Fatal("Cloud revision did not bind the review")
	}
	h.Children[1] = h.Children[0]
	if h.Validate() == nil {
		t.Fatal("Duplicate child accepted")
	}
	h.Children = nil
	h.MainRevision = "invalid"
	if h.Validate() == nil {
		t.Fatal("Invalid content revision accepted")
	}
}

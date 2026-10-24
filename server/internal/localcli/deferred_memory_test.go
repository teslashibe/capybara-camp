package localcli

import (
	"strings"
	"testing"
)

func TestDeferredMemoryCommandIsUnavailable(t *testing.T) {
	app := providerFixture(t, "conversation-codex")
	_, err := app.Run(t.Context(), "memory-inference", strings.NewReader(`{}`))
	if err == nil || err.Error() != "Unsupported Go helper action: memory-inference" {
		t.Fatal("removed command was not rejected", err)
	}
}

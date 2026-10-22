package localcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHistoryContextUsesPrivateReadOnlyChannel(t *testing.T) {
	history := []HistorySource{{ID: "camp:fixture", Title: "Full conversation", Revision: "17"}}
	var calls atomic.Int32
	reader := func(ctx context.Context, in HistoryRead) (HistoryPage, error) {
		calls.Add(1)
		if in.SessionID != "camp:fixture" {
			t.Error("Foreign session reached reader")
		}
		return HistoryPage{SessionID: in.SessionID, Title: "Full conversation", Revision: "17", Messages: []HistoryMessage{{ID: "original", Ordinal: in.Offset, Role: "assistant", Text: "Pinned contents", TextOffset: in.TextOffset, Complete: true}}}, nil
	}
	grant, err := newContextGrantWithHistory([]contextSource{}, history, reader, t.TempDir(), "/fixture/camp-local")
	if err != nil {
		t.Fatal(err)
	}
	defer grant.Close()
	path := filepath.Join(grant.directory, "sources.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("Pinned contents")) || bytes.Contains(raw, []byte("campr_")) {
		t.Fatal("Contents or runner grant exposed in manifest")
	}
	var manifest contextManifest
	if json.Unmarshal(raw, &manifest) != nil || manifest.Version != 2 || len(manifest.Proxy.Token) != 43 {
		t.Fatal("Invalid private channel descriptor")
	}
	requests := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_session","arguments":{"session_id":"camp:fixture","offset":120}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_session","arguments":{"session_id":"other"}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err = fixture(t).ServeContext(t.Context(), path, strings.NewReader(requests), &output); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || !strings.Contains(output.String(), "Pinned contents") || !strings.Contains(output.String(), "message=original") {
		t.Fatal("History was not read with provenance", calls.Load(), output.String())
	}
	if !strings.Contains(output.String(), `\"isError\":true`) && !strings.Contains(output.String(), `"isError":true`) {
		t.Fatal("Unattached read was accepted")
	}
	bad := manifest.Proxy
	bad.Token = strings.Repeat("a", 43)
	if _, err = bad.reader(t.Context(), HistoryRead{SessionID: "camp:fixture", Limit: 1}); err == nil {
		t.Fatal("Incorrect read capability accepted")
	}
	grant.Close()
	if _, err = manifest.Proxy.reader(t.Context(), HistoryRead{SessionID: "camp:fixture", Limit: 1}); err == nil {
		t.Fatal("Closed conversation retained history access")
	}
}

func TestHistoryReaderBoundsConcurrentWorkAndClosesInFlightReads(t *testing.T) {
	source := HistorySource{ID: "source", Revision: "1"}
	started := make(chan struct{}, 2)
	proxy, server, err := startContextProxy([]HistorySource{source}, func(ctx context.Context, _ HistoryRead) (HistoryPage, error) {
		started <- struct{}{}
		<-ctx.Done()
		return HistoryPage{}, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := proxy.reader(t.Context(), HistoryRead{SessionID: "source", Limit: 1}); done <- err }()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("Reader did not start")
		}
	}
	if _, err = proxy.reader(t.Context(), HistoryRead{SessionID: "source", Limit: 1}); err == nil {
		t.Fatal("Unbounded reader concurrency")
	}
	if err = server.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("Closed reader returned history")
			}
		case <-time.After(time.Second):
			t.Fatal("Read leaked after close")
		}
	}
}

func TestHistoryPageRejectsWrongRevisionAndNonadvancingCursor(t *testing.T) {
	source := HistorySource{ID: "source", Revision: "1"}
	if err := validateHistoryCatalog(nil, []HistorySource{{ID: "source", Revision: "1", Title: strings.Repeat("😀", 500)}}); err != nil {
		t.Fatal("Valid Unicode title was rejected", err)
	}
	in := HistoryRead{SessionID: "source", Limit: 1}
	page := HistoryPage{SessionID: "source", Revision: "1", Messages: []HistoryMessage{{ID: "one", Ordinal: 0, Role: "assistant", Text: "😀", Complete: false}}, NextOffset: new(int64), NextTextOffset: 4}
	if err := validateHistoryPage(source, in, page); err != nil {
		t.Fatal(err)
	}
	page.NextTextOffset = 0
	if err := validateHistoryPage(source, in, page); err == nil {
		t.Fatal("Nonadvancing text cursor accepted")
	}
	page.NextTextOffset = 4
	page.Revision = "2"
	if err := validateHistoryPage(source, in, page); err == nil {
		t.Fatal("Changed revision accepted")
	}
	for _, args := range []string{`{"session_id":"source","offset":-1}`, `{"session_id":"source","limit":121}`, `{"session_id":"source","text_offset":true}`, `{"session_id":"source","path":"/etc/passwd"}`} {
		if _, err := historyInput(contextArguments(t, args)); err == nil {
			t.Fatal("Invalid arguments accepted", args)
		}
	}
	if _, _, err := startContextProxy([]HistorySource{source}, nil); err == nil {
		t.Fatal("Missing reader accepted")
	}
	if err := validateHistoryCatalog(contextFixture(), []HistorySource{{ID: "local-source", Revision: "1"}}); err == nil {
		t.Fatal("Duplicate context identity accepted")
	}
	for _, endpoint := range []string{"https://example.com/read-session", "http://localhost:80/read-session", "http://127.0.0.1:80/wrong", "http://127.0.0.1:80/read-session?query=1"} {
		if _, err := (contextProxy{Endpoint: endpoint, Token: strings.Repeat("a", 43)}).reader(t.Context(), in); err == nil {
			t.Fatal("Unsafe endpoint accepted")
		}
	}
	proxy, server, err := startContextProxy([]HistorySource{source}, func(context.Context, HistoryRead) (HistoryPage, error) { return HistoryPage{}, errors.New("revoked") })
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if _, err = proxy.reader(t.Context(), in); err == nil {
		t.Fatal("Revoked reader remained available")
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestUIAuditProcess(t *testing.T) {
	if os.Getenv("CAMP_FIXTURE_CHILD") != "1" {
		return
	}
	if err := run("ui-audit", nil); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func auditEvents(t *testing.T, controls ...message) []message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var input bytes.Buffer
	for _, value := range controls {
		if err := json.NewEncoder(&input).Encode(value); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestUIAuditProcess$")
	cmd.Env = append(os.Environ(), "CAMP_FIXTURE_CHILD=1")
	// Keep input open for the complete response, as the native transport does.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var output, failure bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &failure
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if _, err := stdin.Write(input.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("fixture failed: %v: %s", err, failure.String())
	}
	var events []message
	for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
		var value message
		if err := json.Unmarshal(line, &value); err != nil {
			t.Fatal(err)
		}
		events = append(events, value)
	}
	return events
}

func TestUIAuditPlanningUsesCompleteSnapshotsWithoutChatApprovals(t *testing.T) {
	for _, prompt := range []string{"Consider permission and approval boundaries", "Review the other proposal once", "Produce one combined plan"} {
		t.Run(prompt, func(t *testing.T) {
			events := auditEvents(t, message{"mode": "planning", "prompt": prompt, "session": message{"provider": "Codex"}})
			previous, snapshots, done := "", 0, 0
			for _, event := range events {
				switch text(event["type"]) {
				case "text":
					current := text(event["text"])
					if !strings.HasPrefix(current, previous) || len(current) <= len(previous) {
						t.Fatal("text events must contain the complete growing message")
					}
					previous = current
					snapshots++
				case "done":
					done++
				case "approval", "session", "cancelled", "error":
					t.Fatalf("unexpected planning event: %s", text(event["type"]))
				}
			}
			if snapshots < 4 || done != 1 || text(events[len(events)-1]["type"]) != "done" {
				t.Fatal("planning response did not complete")
			}
		})
	}
}

func TestUIAuditPlanningStopsBeforeCompletion(t *testing.T) {
	events := auditEvents(t, message{"mode": "planning", "session": message{"provider": "Claude"}}, message{"type": "stop"})
	if text(events[len(events)-1]["type"]) != "cancelled" {
		t.Fatal("planning stop was not acknowledged")
	}
	for _, event := range events {
		if text(event["type"]) == "done" {
			t.Fatal("cancelled planning was reported as completed")
		}
	}
}

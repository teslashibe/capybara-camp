package camp

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

func TestRunnerHistoryPagesCompletePinnedConversation(t *testing.T) {
	g, w, grant, owner, assignment := executionFixture(t, "planning")
	wanted := map[string]string{}
	messages := []MessageInput{}
	for i := 0; i < 145; i++ {
		id, text := fmt.Sprintf("m-%03d", i), strings.Repeat("history ", 50)
		if i == 71 {
			text = "x" + strings.Repeat("😀", 75000)
		}
		wanted[id] = text
		messages = append(messages, MessageInput{ID: id, Ordinal: int64(i * 2), Role: "assistant", Text: text})
	}
	revision, err := g.store.WriteTranscript(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), TranscriptInput{ExpectedRevision: 0, Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	assignment.ExpectedTranscriptRevision = revision
	if _, err = g.store.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), assignment); err != nil {
		t.Fatal(err)
	}
	r, err := g.store.CreateRequest(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), RequestInput{Prompt: "Continue the complete history"})
	if err != nil {
		t.Fatal(err)
	}
	claim := uuid.NewString()
	if _, err = g.ClaimRunnerRequest(t.Context(), grant.Token, w.ID, claim); err != nil {
		t.Fatal(err)
	}
	if _, err = g.RunnerTurn(t.Context(), grant.Token, w.ID, r.ID); !errors.Is(err, ErrInput) {
		t.Fatal("Legacy inline limit changed", err)
	}
	work, err := g.PagedRunnerTurn(t.Context(), grant.Token, w.ID, r.ID)
	if err != nil || len(work.History) != 1 || work.History[0].Revision != strconv.FormatInt(revision, 10) || len(work.Context) > 4 {
		t.Fatal("Missing paged history", work, err)
	}
	if _, err = g.store.pool.Exec(t.Context(), `INSERT INTO workspace_runtime(workspace_id,provider_session_id) VALUES($1,'already-created')`, w.ID); err != nil {
		t.Fatal(err)
	}
	resumed, err := g.PagedRunnerTurn(t.Context(), grant.Token, w.ID, r.ID)
	if err != nil || len(resumed.History) != 1 || resumed.ProviderSessionID != "already-created" {
		t.Fatal("Resumed conversation lost history access", err)
	}
	// A later message edit must not change the pinned source.
	if _, err = g.store.pool.Exec(t.Context(), `INSERT INTO message_revisions(session_id,local_id,revision,role,text) VALUES($1,'m-000',2,'assistant','newer text')`, w.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = g.store.pool.Exec(t.Context(), `UPDATE sessions SET transcript_revision=2 WHERE id=$1`, w.SessionID); err != nil {
		t.Fatal(err)
	}
	in := localcli.HistoryRead{SessionID: "camp:" + w.SessionID, Limit: 120}
	got := map[string]string{}
	pages := 0
	for {
		page, err := g.RunnerHistory(t.Context(), grant.Token, w.ID, r.ID, in)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if pages > 100 || len(page.Messages) > 20 || page.Revision != strconv.FormatInt(revision, 10) {
			t.Fatal("Invalid history page")
		}
		bytes := 0
		for _, m := range page.Messages {
			got[m.ID] += m.Text
			bytes += len(m.Text)
		}
		if bytes > 48<<10 {
			t.Fatal("Page exceeded byte budget")
		}
		if page.NextOffset == nil {
			break
		}
		if *page.NextOffset < in.Offset || (*page.NextOffset == in.Offset && page.NextTextOffset <= in.TextOffset) {
			t.Fatal("Cursor did not advance")
		}
		in.Offset, in.TextOffset = *page.NextOffset, page.NextTextOffset
	}
	if pages < 10 || len(got) != len(wanted) {
		t.Fatal("Incomplete transcript", pages, len(got))
	}
	for id, text := range wanted {
		if got[id] != text {
			t.Fatal("History changed or truncated", id, len(got[id]), len(text))
		}
	}
	if _, err = g.RunnerHistory(t.Context(), grant.Token, w.ID, r.ID, localcli.HistoryRead{SessionID: "camp:" + uuid.NewString(), Limit: 1}); err == nil {
		t.Fatal("Unattached history allowed")
	}
	if _, err = g.RunnerHistory(t.Context(), grant.Token, w.ID, r.ID, localcli.HistoryRead{SessionID: "camp:" + w.SessionID, Offset: 1, TextOffset: 1, Limit: 1}); err == nil {
		t.Fatal("Text offset applied to a different message")
	}
	if _, err = g.RunnerHistory(t.Context(), grant.Token, w.ID, r.ID, localcli.HistoryRead{SessionID: "camp:" + w.SessionID, Offset: 142, TextOffset: 2, Limit: 1}); err == nil {
		t.Fatal("Mid-character byte offset accepted")
	}
	if _, err = g.store.ActOnRequest(t.Context(), owner, w.CampID, r.ID, uuid.NewString(), RequestAction{Action: "cancel"}); err != nil {
		t.Fatal(err)
	}
	if _, err = g.RunnerHistory(t.Context(), grant.Token, w.ID, r.ID, localcli.HistoryRead{SessionID: "camp:" + w.SessionID, Limit: 1}); err == nil {
		t.Fatal("Cancelled turn retained history access")
	}
}

func TestRunnerHistoryRevalidatesPermissionAndInstallation(t *testing.T) {
	for _, change := range []string{"permission", "grant", "lease"} {
		t.Run(change, func(t *testing.T) {
			g, w, grant, _, r := streamFixture(t)
			if _, err := g.store.pool.Exec(t.Context(), `UPDATE prompt_requests SET context_revision=1 WHERE id=$1`, r.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := g.store.pool.Exec(t.Context(), `UPDATE sessions SET transcript_revision=1 WHERE id=$1`, w.SessionID); err != nil {
				t.Fatal(err)
			}
			in := localcli.HistoryRead{SessionID: "camp:" + w.SessionID, Limit: 1}
			if _, err := g.RunnerHistory(t.Context(), grant.Token, w.ID, r.ID, in); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "permission":
				_, _ = g.store.pool.Exec(t.Context(), `UPDATE sessions SET permission_revision=permission_revision+1 WHERE id=$1`, w.SessionID)
			case "grant":
				_, _ = g.store.pool.Exec(t.Context(), `UPDATE runner_grants SET revoked_at=now() WHERE token_hash=$1`, auth.HashToken(grant.Token))
			case "lease":
				_, _ = g.store.pool.Exec(t.Context(), `UPDATE prompt_requests SET lease_until=now()-interval '1 second' WHERE id=$1`, r.ID)
			}
			if _, err := g.RunnerHistory(t.Context(), grant.Token, w.ID, r.ID, in); err == nil {
				t.Fatal("History remained authorized")
			}
		})
	}
}

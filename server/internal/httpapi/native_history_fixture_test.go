package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
)

// Seeds real persisted histories and execution assignments without contacting
// GitHub or launching either provider. The native test downloads via HTTP.
func seedNativeTransferHistory(t *testing.T, s *camp.Store, g *camp.GitStore, owner auth.Actor, campID string, project camp.Project, grants map[string]camp.RunnerGrant, directory string) {
	t.Helper()
	ctx := t.Context()
	connections, err := g.Connections(ctx, owner)
	if err != nil || len(connections) != 1 {
		t.Fatal("fixture Git connection", err)
	}
	sessions := []camp.Session{}
	runnerTokens := map[string]string{}
	for account, grant := range grants {
		state, err := s.RunnerAccount(ctx, grant.Token)
		if err != nil {
			t.Fatal(err)
		}
		profile, err := s.RegisterProfile(ctx, owner, uuid.NewString(), camp.ProfileInput{LocalID: strings.Repeat(map[string]string{"Codex": "c", "Claude": "d"}[state.Provider], 64), Provider: state.Provider, Label: "History fixture"})
		if err != nil {
			t.Fatal(err)
		}
		session, err := s.PublishSession(ctx, owner, campID, uuid.NewString(), camp.SessionInput{ID: uuid.NewString(), ProjectID: project.ID, ProfileID: profile.ID, LocalSessionID: uuid.NewString(), Title: "History " + state.Provider, State: "idle", Stage: "Ready", Shared: false})
		if err != nil {
			t.Fatal(err)
		}
		var attachment *camp.AttachmentReference
		var attachmentBytes []byte
		if os.Getenv("CAMP_NATIVE_FIXTURE_HISTORY_ATTACHMENTS") == "1" {
			attachmentBytes = bytes.Repeat([]byte{255}, 16<<20)
			hash := sha256.Sum256(attachmentBytes)
			attachment = &camp.AttachmentReference{ID: "a:0", Kind: "document", Source: "inline", SourceDigest: strings.Repeat("c", 64), State: "embedded", MediaType: "application/octet-stream", SHA256: hex.EncodeToString(hash[:]), Size: len(attachmentBytes)}
			if _, err = s.PutAttachment(ctx, owner, campID, session.ID, attachment.SHA256, camp.AttachmentUpload{Data: attachmentBytes}); err != nil {
				t.Fatal(err)
			}
		}
		for batch := 0; batch < 2; batch++ {
			messages := []camp.MessageInput{}
			for i := batch * 130; i < (batch+1)*130; i++ {
				messages = append(messages, camp.MessageInput{ID: fmt.Sprintf("parent-%d", i), Ordinal: int64(i), Role: "assistant", Text: fmt.Sprintf("Original parent message %d", i), Model: "recorded-model", Effort: "high", Speed: "fast"})
				if attachment != nil && i == 259 {
					messages[len(messages)-1].Attachments = []camp.AttachmentReference{*attachment}
				}
			}
			revision, err := s.WriteTranscript(ctx, owner, campID, session.ID, uuid.NewString(), camp.TranscriptInput{ExpectedRevision: session.TranscriptRevision, Messages: messages})
			if err != nil {
				t.Fatal(err)
			}
			session.TranscriptRevision = revision
		}
		childID := uuid.NewString()
		var childRevision int64
		for batch := 0; batch < 2; batch++ {
			messages := []camp.MessageInput{}
			for i := batch * 125; i < (batch+1)*125; i++ {
				messages = append(messages, camp.MessageInput{ID: fmt.Sprintf("child-%d", i), Ordinal: int64(i), Role: "assistant", Text: fmt.Sprintf("Original child message %d", i), Model: "recorded-child", Effort: "high", Speed: "fast"})
				if attachment != nil && i == 249 {
					messages[len(messages)-1].Attachments = []camp.AttachmentReference{*attachment}
				}
			}
			if attachment != nil && batch == 1 {
				if _, err = s.PutAttachment(ctx, owner, campID, session.ID, attachment.SHA256, camp.AttachmentUpload{ChildID: childID, Data: attachmentBytes}); err != nil {
					t.Fatal(err)
				}
			}
			child, err := s.WriteChildHistory(ctx, owner, campID, session.ID, childID, uuid.NewString(), camp.ChildHistoryInput{SourceKey: strings.Repeat("a", 64), SourceRevision: strings.Repeat("b", 64), Name: "Review helper", Phase: "review", Workflow: "History", State: "done", Model: "recorded-child", Complete: batch == 1, ExpectedParentRevision: session.TranscriptRevision, ExpectedTranscriptRevision: childRevision, Messages: messages})
			if err != nil {
				t.Fatal(err)
			}
			childRevision = child.TranscriptRevision
			session.TranscriptRevision = child.Revision
		}
		w, err := g.CreateWorkspace(ctx, owner, campID, session.ID, uuid.NewString(), camp.WorkspaceInput{ProviderAccountID: account, GitConnectionID: connections[0].ID, GitHubInstallationID: 10, RepositoryID: 20, InstallationPage: 1, RepositoryPage: 1, Purpose: "planning", ExpectedSessionRevision: session.Revision, ExpectedProjectRevision: project.Revision})
		if err != nil {
			t.Fatal(err)
		}
		claim := uuid.NewString()
		if _, err = g.ClaimRunnerWorkspace(ctx, grant.Token, w.ID, claim); err != nil {
			t.Fatal(err)
		}
		if _, err = g.RunnerWorkspaceToken(ctx, grant.Token, w.ID, claim); err != nil {
			t.Fatal(err)
		}
		w, err = g.ReportRunnerWorkspace(ctx, grant.Token, w.ID, claim, camp.WorkspaceReport{State: "ready", BaseCommit: w.BaseCommit, Branch: w.Branch})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.AssignExecution(ctx, owner, campID, session.ID, uuid.NewString(), camp.ExecutionInput{Location: "cloud", WorkspaceID: w.ID, ExpectedEpoch: session.ExecutionEpoch, ExpectedSessionRevision: session.Revision, ExpectedTranscriptRevision: session.TranscriptRevision, ExpectedWorkspaceRevision: w.Revision}); err != nil {
			t.Fatal(err)
		}
		session, err = s.Session(ctx, owner, campID, session.ID)
		if err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, session)
		runnerTokens[state.Provider] = grant.Token
	}
	raw, err := json.Marshal(sessions)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "history-sessions.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	// Disposable fixture grants only. The test drives the runner's HTTP
	// acknowledgements; it does not attest a live model or real source process.
	if os.Getenv("CAMP_NATIVE_FIXTURE_RETURN") == "1" {
		raw, err = json.Marshal(runnerTokens)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(directory, "return-runner-tokens.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

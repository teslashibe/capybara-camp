package camp

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

func exportFixture(t *testing.T, kind string) (*GitStore, Workspace, RunnerGrant, auth.Actor, WorkspaceExport) {
	t.Helper()
	g, w, grant, owner, input := executionFixture(t, "planning")
	if _, err := g.store.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), input); err != nil {
		t.Fatal(err)
	}
	session, err := g.store.Session(t.Context(), owner, w.CampID, w.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{}
	if kind == "capture" {
		paths = []string{"file.txt"}
	}
	in := WorkspaceExportInput{Kind: kind, Paths: paths, ExpectedEpoch: session.ExecutionEpoch, ExpectedRevision: session.Revision, ExpectedTranscriptRevision: session.TranscriptRevision, ExpectedPermissionRevision: session.PermissionRevision}
	key := uuid.NewString()
	e, err := g.CreateWorkspaceExport(t.Context(), owner, w.CampID, w.SessionID, key, in)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := g.CreateWorkspaceExport(t.Context(), owner, w.CampID, w.SessionID, key, in)
	if err != nil || repeat.ID != e.ID {
		t.Fatal("create replay", err)
	}
	return g, w, grant, owner, e
}
func resultExport(t *testing.T, g *GitStore, grant RunnerGrant, e WorkspaceExport, raw []byte) WorkspaceExport {
	t.Helper()
	var err error
	e, err = g.BeginWorkspaceExportResult(t.Context(), grant.Token, e.ID, WorkspaceExportResultInput{ClaimID: e.ClaimID, SHA256: transferDigest(raw), Bytes: len(raw)})
	if err != nil {
		t.Fatal(err)
	}
	for i, offset := 0, 0; offset < len(raw); i, offset = i+1, offset+TransferChunkBytes {
		part := raw[offset:min(len(raw), offset+TransferChunkBytes)]
		in := TransferChunk{Data: part, SHA256: transferDigest(part)}
		e, err = g.PutWorkspaceExportChunk(t.Context(), grant.Token, e.ID, e.ClaimID, i, in)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = g.PutWorkspaceExportChunk(t.Context(), grant.Token, e.ID, e.ClaimID, i, in); err != nil {
			t.Fatal("chunk replay", err)
		}
	}
	e, err = g.FinishWorkspaceExport(t.Context(), grant.Token, e.ID, e.ClaimID, false)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func TestWorkspaceExportCaptureIsolationReplayAndCancellation(t *testing.T) {
	g, w, grant, owner, e := exportFixture(t, "capture")
	s := g.store
	var member auth.Actor
	if err := s.pool.QueryRow(t.Context(), `SELECT user_id FROM memberships WHERE camp_id=$1 AND user_id<>$2 LIMIT 1`, w.CampID, owner.UserID).Scan(&member.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WorkspaceExport(t.Context(), member, w.CampID, w.SessionID, e.ID, false); !errors.Is(err, ErrDenied) {
		t.Fatal("shared reader saw export", err)
	}
	_, foreign := addCloud(t, s, owner, "Codex")
	if _, err := g.ClaimWorkspaceExport(t.Context(), foreign.Token, e.ID, uuid.NewString()); err == nil {
		t.Fatal("foreign grant claimed")
	}
	claim := uuid.NewString()
	job, err := g.ClaimWorkspaceExport(t.Context(), grant.Token, e.ID, claim)
	if err != nil {
		t.Fatal(err)
	}
	e = job.Export
	if _, err = g.ClaimWorkspaceExport(t.Context(), grant.Token, e.ID, uuid.NewString()); !errors.Is(err, ErrDenied) {
		t.Fatal("second worker adopted", err)
	}
	if repeat, err := g.ClaimWorkspaceExport(t.Context(), grant.Token, e.ID, claim); err != nil || repeat.Export.ClaimID != claim {
		t.Fatal("claim replay", err)
	}
	data := []byte(strings.Repeat("safe fixture bytes\n", 20000))
	m := transfer.Manifest{Version: 1, BaseCommit: w.BaseCommit, Branch: w.Branch, Files: []transfer.File{{Path: "file.txt", Data: data, SHA256: transferDigest(data)}}, Excluded: []transfer.Exclusion{}}
	raw, _ := json.Marshal(m)
	e = resultExport(t, g, grant, e, raw)
	if e.State != "ready" || len(e.Uploaded) < 2 {
		t.Fatal("expected multiple encrypted chunks", e.State)
	}
	if _, err = g.FinishWorkspaceExport(t.Context(), grant.Token, e.ID, claim, false); err != nil {
		t.Fatal("lost finish reply", err)
	}
	var stored []byte
	if err = s.pool.QueryRow(t.Context(), `SELECT encrypted_data FROM workspace_export_chunks WHERE export_id=$1 AND ordinal=0`, e.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("safe fixture bytes")) {
		t.Fatal("plaintext result stored")
	}
	read := []byte{}
	for i := range e.Uploaded {
		chunk, err := g.WorkspaceExportChunk(t.Context(), owner, w.CampID, w.SessionID, e.ID, i)
		if err != nil {
			t.Fatal(err)
		}
		read = append(read, chunk.Data...)
	}
	if !bytes.Equal(read, raw) {
		t.Fatal("changed result")
	}
	if _, err = g.WorkspaceExportChunk(t.Context(), member, w.CampID, w.SessionID, e.ID, 0); !errors.Is(err, ErrDenied) {
		t.Fatal("shared reader downloaded files", err)
	}
	if _, err = s.WorkspaceExport(t.Context(), owner, w.CampID, w.SessionID, e.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = g.WorkspaceExportChunk(t.Context(), owner, w.CampID, w.SessionID, e.ID, 0); !errors.Is(err, ErrTransition) {
		t.Fatal("cancelled result readable", err)
	}
	var count int
	_ = s.pool.QueryRow(t.Context(), `SELECT count(*) FROM workspace_export_chunks WHERE export_id=$1`, e.ID).Scan(&count)
	if count != 0 {
		t.Fatal("cancel retained chunks")
	}
}
func TestWorkspaceExportReservesWorkerAndRejectsUnselectedFiles(t *testing.T) {
	g, w, grant, owner, e := exportFixture(t, "capture")
	job, err := g.ClaimWorkspaceExport(t.Context(), grant.Token, e.ID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	e = job.Export
	r, err := g.store.CreateRequest(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), RequestInput{Prompt: "Wait for capture"})
	if err != nil {
		t.Fatal(err)
	}
	if work, err := g.ClaimRunnerRequest(t.Context(), grant.Token, w.ID, uuid.NewString()); err != nil || work != nil {
		t.Fatal("provider overlapped export", err)
	}
	if _, err = g.store.ActOnRequest(t.Context(), owner, w.CampID, r.ID, uuid.NewString(), RequestAction{Action: "cancel"}); err != nil {
		t.Fatal(err)
	}
	m := transfer.Manifest{Version: 1, BaseCommit: w.BaseCommit, Branch: w.Branch, Files: []transfer.File{{Path: "unselected.txt", Data: []byte("no"), SHA256: transferDigest([]byte("no"))}}}
	raw, _ := json.Marshal(m)
	if _, err = g.BeginWorkspaceExportResult(t.Context(), grant.Token, e.ID, WorkspaceExportResultInput{ClaimID: e.ClaimID, SHA256: transferDigest(raw), Bytes: len(raw)}); err != nil {
		t.Fatal(err)
	}
	if _, err = g.PutWorkspaceExportChunk(t.Context(), grant.Token, e.ID, e.ClaimID, 0, TransferChunk{Data: raw, SHA256: transferDigest(raw)}); err != nil {
		t.Fatal(err)
	}
	if _, err = g.FinishWorkspaceExport(t.Context(), grant.Token, e.ID, e.ClaimID, false); !errors.Is(err, ErrInput) {
		t.Fatal("included unselected path", err)
	}
	if _, err = g.FinishWorkspaceExport(t.Context(), grant.Token, e.ID, e.ClaimID, true); err != nil {
		t.Fatal(err)
	}
}
func TestWorkspaceExportInspectionExpiryAndSourceChanges(t *testing.T) {
	for _, kind := range []string{"expiry", "source", "grant", "git", "project", "account"} {
		t.Run(kind, func(t *testing.T) {
			g, w, grant, owner, e := exportFixture(t, "inspect")
			job, err := g.ClaimWorkspaceExport(t.Context(), grant.Token, e.ID, uuid.NewString())
			if err != nil {
				t.Fatal(err)
			}
			e = job.Export
			raw, _ := json.Marshal(transfer.ChangeSet{BaseCommit: w.BaseCommit, Branch: w.Branch, Files: []transfer.Change{{Path: "file.txt", Kind: "modified"}}})
			e = resultExport(t, g, grant, e, raw)
			switch kind {
			case "expiry":
				_, err = g.store.pool.Exec(t.Context(), `UPDATE workspace_exports SET expires_at=now()-interval '1 second' WHERE id=$1`, e.ID)
			case "source":
				_, err = g.store.pool.Exec(t.Context(), `UPDATE sessions SET permission_revision=permission_revision+1 WHERE id=$1`, w.SessionID)
			case "grant":
				_, err = g.store.pool.Exec(t.Context(), `UPDATE runner_grants SET revoked_at=now() WHERE token_hash=$1`, auth.HashToken(grant.Token))
			case "git":
				_, err = g.store.pool.Exec(t.Context(), `UPDATE git_connections SET epoch=epoch+1 WHERE id=$1`, w.GitConnectionID)
			case "project":
				_, err = g.store.pool.Exec(t.Context(), `UPDATE projects SET authority_revision=authority_revision+1 WHERE id=$1`, w.ProjectID)
			case "account":
				_, err = g.store.pool.Exec(t.Context(), `UPDATE provider_installations SET epoch=epoch+1 WHERE id=$1`, w.ProviderInstallationID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = g.ClaimWorkspaceExport(t.Context(), grant.Token, e.ID, e.ClaimID); err == nil {
				t.Fatal("changed authority accepted")
			}
			if kind != "grant" {
				if _, err = g.WorkspaceExportChunk(t.Context(), owner, w.CampID, w.SessionID, e.ID, 0); err == nil {
					t.Fatal("stale source readable")
				}
			}
		})
	}
}

func TestWorkspaceExportChangedSourceReleasesClaim(t *testing.T) {
	g, w, grant, _, e := exportFixture(t, "capture")
	job, err := g.ClaimWorkspaceExport(t.Context(), grant.Token, e.ID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	e = job.Export
	if _, err = g.store.pool.Exec(t.Context(), `UPDATE sessions SET permission_revision=permission_revision+1 WHERE id=$1`, w.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = g.ClaimWorkspaceExport(t.Context(), grant.Token, e.ID, e.ClaimID); err == nil {
		t.Fatal("stale claim accepted")
	}
	for range 2 {
		failed, err := g.FinishWorkspaceExport(t.Context(), grant.Token, e.ID, e.ClaimID, true)
		if err != nil || failed.State != "failed" {
			t.Fatal("could not release stale claim", err)
		}
	}
	if next, err := g.NextWorkspaceExport(t.Context(), grant.Token); err != nil || next != nil {
		t.Fatal("failed export still scheduled", err)
	}
}

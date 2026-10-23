package camp

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/gitprovider"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

func checkoutExportFixture(t *testing.T) (*GitStore, *workspaceGit, Workspace, auth.Actor, WorkspaceExport) {
	t.Helper()
	g, w, grant, owner, e := exportFixture(t, "capture")
	job, err := g.ClaimWorkspaceExport(t.Context(), grant.Token, e.ID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(transfer.Manifest{Version: 1, BaseCommit: w.BaseCommit, Branch: w.Branch, Files: []transfer.File{{Path: "file.txt", Data: []byte("review me"), SHA256: transferDigest([]byte("review me"))}}})
	e = resultExport(t, g, grant, job.Export, raw)
	f := g.provider.(*workspaceGit)
	f.mu.Lock()
	f.scopeCalls = 0
	f.mu.Unlock()
	return g, f, w, owner, e
}
func checkoutToken(t *testing.T, g *GitStore, w Workspace, a auth.Actor, e WorkspaceExport) ExportCheckoutToken {
	t.Helper()
	v, err := g.WorkspaceExportCheckoutToken(t.Context(), a, w.CampID, w.SessionID, e.ID, e.ResultSHA256)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func TestExportCheckoutTokenReplayAndDeviceIsolation(t *testing.T) {
	g, f, w, owner, e := checkoutExportFixture(t)
	if _, err := g.WorkspaceExportCheckoutToken(t.Context(), owner, w.CampID, w.SessionID, e.ID, strings.Repeat("f", 64)); !errors.Is(err, ErrRevision) {
		t.Fatal("unreviewed export", err)
	}
	var wg sync.WaitGroup
	results := make(chan ExportCheckoutToken, 8)
	failures := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			v, err := g.WorkspaceExportCheckoutToken(t.Context(), owner, w.CampID, w.SessionID, e.ID, e.ResultSHA256)
			if err != nil {
				failures <- err
			} else {
				results <- v
			}
		})
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if !errors.Is(err, ErrTransition) {
			t.Fatal(err)
		}
	}
	value := checkoutToken(t, g, w, owner, e)
	for v := range results {
		if v != value {
			t.Fatal("replay changed credential binding")
		}
	}
	if value.DeviceID != owner.DeviceID || value.ExportID != e.ID || value.WorkspaceID != w.ID || value.BaseCommit != w.BaseCommit || value.Repository != w.RepositoryName || value.SHA256 != e.ResultSHA256 || !value.ExpiresAt.Before(e.ExpiresAt) {
		t.Fatal("incorrect checkout binding")
	}
	f.mu.Lock()
	calls := f.scopeCalls
	f.mu.Unlock()
	if calls != 1 {
		t.Fatal("duplicated provider mint", calls)
	}
	var encrypted []byte
	if err := g.store.pool.QueryRow(t.Context(), `SELECT encrypted_token FROM workspace_export_git_grants WHERE export_id=$1 AND device_id=$2`, e.ID, owner.DeviceID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte(value.Token)) {
		t.Fatal("plaintext persisted")
	}
	var foreign auth.Actor
	if err := g.store.pool.QueryRow(t.Context(), `SELECT user_id FROM memberships WHERE camp_id=$1 AND user_id<>$2 LIMIT 1`, w.CampID, owner.UserID).Scan(&foreign.UserID); err != nil {
		t.Fatal(err)
	}
	foreign.DeviceID = owner.DeviceID
	if _, err := g.WorkspaceExportCheckoutToken(t.Context(), foreign, w.CampID, w.SessionID, e.ID, e.ResultSHA256); !errors.Is(err, ErrDenied) {
		t.Fatal("teammate saw credential", err)
	}
	device := owner
	device.DeviceID = uuid.NewString()
	if _, err := g.WorkspaceExportCheckoutToken(t.Context(), device, w.CampID, w.SessionID, e.ID, e.ResultSHA256); !errors.Is(err, ErrDenied) {
		t.Fatal("unregistered device", err)
	}
	if _, err := g.store.pool.Exec(t.Context(), `INSERT INTO devices(id,user_id,name,token_hash,expires_at) VALUES($1,$2,'Second Mac',$3,now()+interval '1 day')`, device.DeviceID, device.UserID, auth.HashToken(device.DeviceID)); err != nil {
		t.Fatal(err)
	}
	second := checkoutToken(t, g, w, device, e)
	if second.DeviceID != device.DeviceID {
		t.Fatal("new device received old binding")
	}
	events, err := g.store.Events(t.Context(), owner, w.CampID, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(events)
	if bytes.Contains(raw, []byte("ghu_")) || bytes.Contains(raw, []byte("encrypted_token")) {
		t.Fatal("credential leaked into events")
	}
	if _, err = g.store.WorkspaceExport(t.Context(), owner, w.CampID, w.SessionID, e.ID, true); err != nil {
		t.Fatal(err)
	}
	var active int
	if err = g.store.pool.QueryRow(t.Context(), `SELECT count(*) FROM workspace_export_git_grants WHERE export_id=$1 AND (state<>'closed' OR encrypted_token IS NOT NULL)`, e.ID).Scan(&active); err != nil || active != 0 {
		t.Fatal("cancel retained credentials", err)
	}
	if err = g.store.pool.QueryRow(t.Context(), `SELECT count(*) FROM git_revocations WHERE id IN (SELECT revocation_id FROM workspace_export_git_grants WHERE export_id=$1) AND not_before>now()`, e.ID).Scan(&active); err != nil || active != 0 {
		t.Fatal("cancel delayed revocation", err)
	}
}
func TestExportCheckoutTokenUncertainProviderResultDoesNotRepeat(t *testing.T) {
	g, f, w, owner, e := checkoutExportFixture(t)
	f.mu.Lock()
	f.scopeError = gitprovider.ErrResponse
	f.mu.Unlock()
	if _, err := g.WorkspaceExportCheckoutToken(t.Context(), owner, w.CampID, w.SessionID, e.ID, e.ResultSHA256); err == nil {
		t.Fatal("uncertain request succeeded")
	}
	f.mu.Lock()
	f.scopeError = nil
	f.mu.Unlock()
	if _, err := g.WorkspaceExportCheckoutToken(t.Context(), owner, w.CampID, w.SessionID, e.ID, e.ResultSHA256); !errors.Is(err, ErrTransition) {
		t.Fatal("uncertain mint repeated", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.scopeCalls != 1 {
		t.Fatal("provider was retried")
	}
}
func TestExportCheckoutTokenRechecksAfterProviderCall(t *testing.T) {
	for _, kind := range []string{"cancel", "device", "source", "git", "project"} {
		t.Run(kind, func(t *testing.T) {
			g, f, w, owner, e := checkoutExportFixture(t)
			started, proceed := make(chan struct{}, 1), make(chan struct{})
			f.mu.Lock()
			f.scopeStarted = started
			f.scopeProceed = proceed
			f.mu.Unlock()
			result := make(chan error, 1)
			go func() {
				_, err := g.WorkspaceExportCheckoutToken(t.Context(), owner, w.CampID, w.SessionID, e.ID, e.ResultSHA256)
				result <- err
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				close(proceed)
				t.Fatal("provider not reached")
			}
			var err error
			switch kind {
			case "cancel":
				_, err = g.store.WorkspaceExport(t.Context(), owner, w.CampID, w.SessionID, e.ID, true)
			case "device":
				_, err = g.store.pool.Exec(t.Context(), `UPDATE devices SET revoked_at=now() WHERE id=$1`, owner.DeviceID)
			case "source":
				_, err = g.store.pool.Exec(t.Context(), `UPDATE sessions SET transcript_revision=transcript_revision+1 WHERE id=$1`, w.SessionID)
			case "git":
				_, err = g.store.pool.Exec(t.Context(), `UPDATE git_connections SET epoch=epoch+1 WHERE id=$1`, w.GitConnectionID)
			case "project":
				_, err = g.store.pool.Exec(t.Context(), `UPDATE projects SET revision=revision+1 WHERE id=$1`, w.ProjectID)
			}
			close(proceed)
			if err != nil {
				t.Fatal(err)
			}
			if err = <-result; err == nil {
				t.Fatal("changed authority delivered credential")
			}
			var encrypted []byte
			var state string
			if err = g.store.pool.QueryRow(t.Context(), `SELECT state,encrypted_token FROM workspace_export_git_grants WHERE export_id=$1`, e.ID).Scan(&state, &encrypted); err != nil || state != "closed" || len(encrypted) != 0 {
				t.Fatal("failed issuance retained credential", err)
			}
			var n int
			if err = g.store.pool.QueryRow(t.Context(), `SELECT count(*) FROM git_revocations WHERE owner_user_id=$1 AND not_before<=now()`, owner.UserID).Scan(&n); err != nil || n < 1 {
				t.Fatal("candidate not queued for revocation", err)
			}
		})
	}
}
func TestExportCheckoutTokenReapsRevokedAuthorityAndAbandonedIssuance(t *testing.T) {
	for _, kind := range []string{"device", "membership", "source", "expiry", "git", "project", "provider", "abandoned"} {
		t.Run(kind, func(t *testing.T) {
			g, _, w, owner, e := checkoutExportFixture(t)
			var err error
			if kind == "abandoned" {
				_, err = g.store.pool.Exec(t.Context(), `INSERT INTO workspace_export_git_grants(export_id,device_id,created_at) VALUES($1,$2,now()-interval '1 minute')`, e.ID, owner.DeviceID)
			} else {
				checkoutToken(t, g, w, owner, e)
			}
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "device":
				_, err = g.store.pool.Exec(t.Context(), `UPDATE devices SET revoked_at=now() WHERE id=$1`, owner.DeviceID)
			case "membership":
				_, err = g.store.pool.Exec(t.Context(), `UPDATE memberships SET role='viewer' WHERE camp_id=$1 AND user_id=$2`, w.CampID, owner.UserID)
			case "source":
				_, err = g.store.pool.Exec(t.Context(), `UPDATE sessions SET transcript_revision=transcript_revision+1 WHERE id=$1`, w.SessionID)
			case "expiry":
				_, err = g.store.pool.Exec(t.Context(), `UPDATE workspace_export_git_grants SET expires_at=now()-interval '1 second' WHERE export_id=$1`, e.ID)
			case "git":
				_, err = g.store.pool.Exec(t.Context(), `UPDATE git_connections SET epoch=epoch+1 WHERE id=$1`, w.GitConnectionID)
			case "project":
				_, err = g.store.pool.Exec(t.Context(), `UPDATE projects SET archived=true WHERE id=$1`, w.ProjectID)
			case "provider":
				_, err = g.store.pool.Exec(t.Context(), `UPDATE provider_installations SET epoch=epoch+1 WHERE id=$1`, w.ProviderInstallationID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = g.store.pool.Exec(t.Context(), `UPDATE workspace_export_git_grants SET next_check_at=now()-interval '1 second' WHERE export_id=$1`, e.ID); err != nil {
				t.Fatal(err)
			}
			if err = g.reapExportTokens(t.Context()); err != nil {
				t.Fatal(err)
			}
			var state string
			var encrypted []byte
			if err = g.store.pool.QueryRow(t.Context(), `SELECT state,encrypted_token FROM workspace_export_git_grants WHERE export_id=$1`, e.ID).Scan(&state, &encrypted); err != nil || state != "closed" || len(encrypted) != 0 {
				t.Fatal("reaper kept credential", err)
			}
			if _, err = g.WorkspaceExportCheckoutToken(t.Context(), owner, w.CampID, w.SessionID, e.ID, e.ResultSHA256); err == nil {
				t.Fatal("retired credential returned")
			}
		})
	}
}

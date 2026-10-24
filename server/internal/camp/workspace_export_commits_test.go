package camp

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

// Server validation binds the archive to the authorized workspace. Git object
// completeness is checked separately against the fetched base by the helper.
func exportArchiveFixture(base string) transfer.CommitArchive {
	data := []byte("tree " + strings.Repeat("a", 40) + "\nparent " + base + "\nauthor Fixture <fixture@example.com> 1700000000 +0000\ncommitter Fixture <fixture@example.com> 1700000000 +0000\n\nUnpublished cloud work\n")
	hash := sha1.New()
	fmt.Fprintf(hash, "commit %d\x00", len(data))
	hash.Write(data)
	head := hex.EncodeToString(hash.Sum(nil))
	return transfer.CommitArchive{Version: 1, BaseCommit: base, HeadCommit: head, Objects: []transfer.CommitObject{{ID: head, Kind: "commit", Data: data}}}
}
func TestWorkspaceExportCommitBinding(t *testing.T) {
	base := strings.Repeat("b", 40)
	archive := exportArchiveFixture(base)
	w := Workspace{BaseCommit: base, Branch: "camp/return"}
	original := transfer.Manifest{Version: 1, BaseCommit: archive.HeadCommit, Branch: w.Branch, Commits: &archive}
	for _, kind := range []string{"valid", "missing", "ancestor", "branch", "retained-import", "missing-import"} {
		t.Run(kind, func(t *testing.T) {
			m, workspace := original, w
			switch kind {
			case "missing":
				m.Commits = nil
			case "ancestor":
				workspace.BaseCommit = strings.Repeat("c", 40)
			case "branch":
				m.Branch = "other"
			case "retained-import":
				workspace.RemoteBaseCommit = base
				workspace.BaseCommit = archive.HeadCommit
			case "missing-import":
				workspace.RemoteBaseCommit = base
				workspace.BaseCommit = strings.Repeat("c", 40)
			}
			raw, _ := json.Marshal(m)
			_, err := validateExportCommits(raw, workspace)
			valid := kind == "valid" || kind == "retained-import"
			if (err == nil) != valid {
				t.Fatal("incorrect workspace archive admission", err)
			}
		})
	}
}
func TestExportCheckoutTokenBindsCapturedHeadAndAncestor(t *testing.T) {
	g, w, grant, owner, e := exportFixture(t, "capture")
	job, err := g.ClaimWorkspaceExport(t.Context(), grant.Token, e.ID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	archive := exportArchiveFixture(w.BaseCommit)
	m := transfer.Manifest{Version: 1, BaseCommit: archive.HeadCommit, Branch: w.Branch, Commits: &archive, Files: []transfer.File{{Path: "file.txt", Data: []byte("review me"), SHA256: transferDigest([]byte("review me"))}}}
	raw, _ := json.Marshal(m)
	e = resultExport(t, g, grant, job.Export, raw)
	first := checkoutToken(t, g, w, owner, e)
	if first.BaseCommit != archive.HeadCommit || first.RemoteBaseCommit != w.BaseCommit || first.SHA256 != transferDigest(raw) {
		t.Fatal("token not bound to captured history")
	}
	replay := checkoutToken(t, g, w, owner, e)
	if replay != first {
		t.Fatal("replay changed captured history")
	}
	// A legacy or corrupt cached grant must not silently downgrade the checkout.
	stale := first
	stale.BaseCommit = w.BaseCommit
	stale.RemoteBaseCommit = ""
	encrypted, err := g.seal(stale, exportTokenBinding(e, owner.DeviceID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.store.pool.Exec(t.Context(), `UPDATE workspace_export_git_grants SET encrypted_token=$2 WHERE export_id=$1`, e.ID, encrypted); err != nil {
		t.Fatal(err)
	}
	if _, err = g.WorkspaceExportCheckoutToken(t.Context(), owner, w.CampID, w.SessionID, e.ID, e.ResultSHA256); err == nil {
		t.Fatal("accepted old checkout binding")
	}
}

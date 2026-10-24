package camp

import (
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

func TestRunnerReadsPinnedChildAndRejectsUnattachedHistory(t *testing.T) {
	g, w, grant, owner, assignment := executionFixture(t, "planning")
	ctx := t.Context()
	id := uuid.NewString()
	body := strings.Repeat("😀", 75000)
	in := childInput(0)
	in.Complete = true
	in.Messages = []MessageInput{{ID: "child-message", Ordinal: 0, Role: "assistant", Text: body}}
	child, err := g.store.WriteChildHistory(ctx, owner, w.CampID, w.SessionID, id, uuid.NewString(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.SourceKey = strings.Repeat("f", 64)
	in.Complete = false
	in.Messages = nil
	in.ExpectedParentRevision = child.Revision
	incomplete, err := g.store.WriteChildHistory(ctx, owner, w.CampID, w.SessionID, uuid.NewString(), uuid.NewString(), in)
	if err != nil {
		t.Fatal(err)
	}
	assignment.ExpectedTranscriptRevision = incomplete.Revision
	if _, err = g.store.AssignExecution(ctx, owner, w.CampID, w.SessionID, uuid.NewString(), assignment); err != nil {
		t.Fatal(err)
	}
	r, err := g.store.CreateRequest(ctx, owner, w.CampID, w.SessionID, uuid.NewString(), RequestInput{Prompt: "Read child history"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.ClaimRunnerRequest(ctx, grant.Token, w.ID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	work, err := g.PagedRunnerTurn(ctx, grant.Token, w.ID, r.ID)
	if err != nil || len(work.History) != 2 {
		t.Fatal("Missing child catalog", work, err)
	}
	source := work.History[1]
	if source.ID != "camp:"+w.SessionID+"/child:"+id || source.ParentID != work.History[0].ID || source.Revision != strconv.FormatInt(incomplete.Revision, 10) {
		t.Fatal(source)
	}
	// A later child edit cannot change the request's saved snapshot.
	if _, err = g.store.pool.Exec(ctx, `INSERT INTO message_revisions(session_id,local_id,revision,role,text) VALUES($1,'child-message',2,'assistant','future')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = g.store.pool.Exec(ctx, `INSERT INTO session_child_revisions(parent_session_id,child_session_id,revision,transcript_revision,source_revision,name,phase,workflow,state,model,complete,removed)
 SELECT parent_session_id,child_session_id,3,transcript_revision,source_revision,name,phase,workflow,state,model,true,false FROM session_child_revisions WHERE child_session_id=$1`, incomplete.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = g.store.pool.Exec(ctx, `UPDATE sessions SET transcript_revision=3 WHERE id=$1`, w.SessionID); err != nil {
		t.Fatal(err)
	}
	later, err := g.PagedRunnerTurn(ctx, grant.Token, w.ID, r.ID)
	if err != nil || len(later.History) != 2 {
		t.Fatal("Later child leaked into a pinned turn", later, err)
	}
	read := localcli.HistoryRead{SessionID: source.ID, Limit: 120}
	var got strings.Builder
	for pages := 0; ; pages++ {
		if pages > 100 {
			t.Fatal("History cursor did not finish")
		}
		page, e := g.RunnerHistory(ctx, grant.Token, w.ID, r.ID, read)
		if e != nil {
			t.Fatal(e)
		}
		if page.Revision != source.Revision || page.Title != child.Name {
			t.Fatal(page)
		}
		for _, m := range page.Messages {
			got.WriteString(m.Text)
		}
		if page.NextOffset == nil {
			break
		}
		read.Offset, read.TextOffset = *page.NextOffset, page.NextTextOffset
	}
	if got.String() != body {
		t.Fatal("Child was truncated or changed")
	}
	for _, bad := range []string{"camp:" + w.SessionID + "/child:" + incomplete.ID, "camp:" + uuid.NewString() + "/child:" + id, "camp:" + w.SessionID + "/child:" + uuid.NewString()} {
		if _, err = g.RunnerHistory(ctx, grant.Token, w.ID, r.ID, localcli.HistoryRead{SessionID: bad, Limit: 20}); err == nil {
			t.Fatal("Unattached child was readable", bad)
		}
	}
	if _, err = g.store.pool.Exec(ctx, `INSERT INTO session_child_revisions(parent_session_id,child_session_id,revision,transcript_revision,source_revision,name,phase,workflow,state,model,complete,removed)
 SELECT parent_session_id,child_session_id,4,transcript_revision,source_revision,name,phase,workflow,state,model,complete,true FROM session_child_revisions WHERE child_session_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = g.store.pool.Exec(ctx, `UPDATE sessions SET transcript_revision=4 WHERE id=$1`, w.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = g.RunnerHistory(ctx, grant.Token, w.ID, r.ID, localcli.HistoryRead{SessionID: source.ID, Limit: 20}); err == nil {
		t.Fatal("Removed child remained readable")
	}
}

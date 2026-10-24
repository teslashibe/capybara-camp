package camp

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestDeferredMemoryIsRetainedButExcludedFromReplay(t *testing.T) {
	s, pool, actors, c, project, profile := sessionFixture(t)
	ctx := t.Context()
	owner := actors[0]
	source, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), sessionInput(project, profile))
	if err != nil {
		t.Fatal(err)
	}
	space, observation, job := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO memory_spaces(id,camp_id,project_id,owner_user_id) VALUES($1,$2,$3,$4)`, space, c.ID, project.ID, owner.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO memory_observations(id,camp_id,space_id,creator_user_id,source_session_id,transcript_revision,permission_revision,message_ids,title,content,content_hash) VALUES($1,$2,$3,$4,$5,1,1,ARRAY['one'],'Retained decision','Private content',$6)`, observation, c.ID, space, owner.UserID, source.ID, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO memory_jobs(id,camp_id,space_id,creator_user_id,provider,model,effort,speed,location,profile_id,owner_device_id,observation_ids,memory_refs) VALUES($1,$2,$3,$4,'Codex','fixture','','standard','local',$5,$6,ARRAY[$7]::uuid[],'[]')`, job, c.ID, space, owner.UserID, profile.ID, owner.DeviceID, observation); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for _, entry := range []struct{ kind, column, id string }{
		{"memory.changed", "", ""}, {"memory.job", "", ""},
		{"legacy.observation", "memory_observation_id", observation}, {"legacy.job", "memory_job_id", job},
		{"session.updated", "", ""},
	} {
		if err = emit(ctx, tx, owner, c.ID, entry.kind, map[string]string{"fixture": "retained"}); err != nil {
			t.Fatal(err)
		}
		if entry.column != "" {
			if _, err = tx.Exec(ctx, `UPDATE events SET `+entry.column+`=$2 WHERE camp_id=$1 AND cursor=(SELECT event_cursor FROM camps WHERE id=$1)`, c.ID, entry.id); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, actor := range actors[:3] {
		events, err := s.Events(ctx, actor, c.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, event := range events {
			if strings.HasPrefix(event.Kind, "memory.") || strings.HasPrefix(event.Kind, "legacy.") {
				t.Fatal("deferred content exposed", event.Kind)
			}
			if event.Kind == "session.updated" {
				found = true
			}
		}
		if !found {
			t.Fatal("ordinary events lost after deferred events")
		}
	}
	var content, state string
	if err = pool.QueryRow(ctx, `SELECT o.content,j.state FROM memory_observations o JOIN memory_jobs j ON j.space_id=o.space_id WHERE o.id=$1`, observation).Scan(&content, &state); err != nil || content != "Private content" || state != "queued" {
		t.Fatal("retained data changed", err)
	}
}

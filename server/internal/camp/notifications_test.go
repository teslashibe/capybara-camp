package camp

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

func completeNotificationRequest(t *testing.T, s *Store, owner, requester auth.Actor, campID, sessionID string) Request {
	t.Helper()
	ctx := context.Background()
	r, err := s.CreateRequest(ctx, requester, campID, sessionID, uuid.NewString(), RequestInput{Prompt: "Private prompt must not appear in notifications"})
	if err != nil {
		t.Fatal(err)
	}
	if r.State == "pending_approval" {
		if _, err = s.ActOnRequest(ctx, owner, campID, r.ID, uuid.NewString(), RequestAction{Action: "approve"}); err != nil {
			t.Fatal(err)
		}
	}
	claimed, err := s.ClaimAvailableRequest(ctx, owner, campID, uuid.NewString(), []string{sessionID})
	if err != nil || claimed == nil {
		t.Fatal("claim", claimed, err)
	}
	if _, err = s.ActOnRequest(ctx, owner, campID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: claimed.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	key := uuid.NewString()
	finish := RequestAction{Action: "finish", LeaseToken: claimed.LeaseToken, Outcome: "completed", Evidence: "Synthetic completion"}
	result, err := s.ActOnRequest(ctx, owner, campID, r.ID, key, finish)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ActOnRequest(ctx, owner, campID, r.ID, key, finish); err != nil {
		t.Fatal("finish replay", err)
	}
	return result
}

func TestNotificationRecipientsReadReceiptsAndRevocation(t *testing.T) {
	s, pool, actors, c, p, profile := sessionFixture(t)
	ctx := context.Background()
	owner, member, viewer := actors[0], actors[1], actors[2]
	input := sessionInput(p, profile)
	session, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	completeNotificationRequest(t, s, owner, member, c.ID, session.ID)
	ownerPage, err := s.Notifications(ctx, owner, c.ID, "", 50)
	if err != nil || len(ownerPage.Notifications) != 1 || ownerPage.Unread != 1 {
		t.Fatal(ownerPage, err)
	}
	memberPage, err := s.Notifications(ctx, member, c.ID, "", 50)
	if err != nil || len(memberPage.Notifications) != 1 || memberPage.Unread != 1 {
		t.Fatal(memberPage, err)
	}
	viewerPage, err := s.Notifications(ctx, viewer, c.ID, "", 50)
	if err != nil || len(viewerPage.Notifications) != 0 {
		t.Fatal("unrelated member got alert", viewerPage, err)
	}
	n := ownerPage.Notifications[0]
	if n.Kind != "finished" || n.Title != input.Title || n.RequestID == "" || n.SessionID != session.ID {
		t.Fatal("wrong result", n)
	}
	if _, err = s.Notification(ctx, owner, c.ID, memberPage.Notifications[0].ID); !errors.Is(err, ErrDenied) {
		t.Fatal("owner read another recipient", err)
	}
	if err = s.ActOnNotifications(ctx, owner, c.ID, uuid.NewString(), NotificationAction{IDs: []string{memberPage.Notifications[0].ID}, Action: "read"}); !errors.Is(err, ErrDenied) {
		t.Fatal("owner changed another recipient", err)
	}
	secondMac := owner
	secondMac.DeviceID = uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO devices(id,user_id,name,token_hash,expires_at) VALUES($1,$2,'Second Mac',$3,now()+interval '1 day')`, secondMac.DeviceID, owner.UserID, auth.HashToken(secondMac.DeviceID)); err != nil {
		t.Fatal(err)
	}
	key := uuid.NewString()
	action := NotificationAction{IDs: []string{n.ID}, Action: "read"}
	if err = s.ActOnNotifications(ctx, secondMac, c.ID, key, action); err != nil {
		t.Fatal(err)
	}
	if err = s.ActOnNotifications(ctx, secondMac, c.ID, key, action); err != nil {
		t.Fatal("read replay", err)
	}
	ownerPage, err = s.Notifications(ctx, owner, c.ID, "", 50)
	if err != nil || ownerPage.Unread != 0 || !ownerPage.Notifications[0].Read {
		t.Fatal("read did not sync", ownerPage, err)
	}
	memberPage, err = s.Notifications(ctx, member, c.ID, "", 50)
	if err != nil || memberPage.Unread != 1 {
		t.Fatal("other recipient changed", memberPage, err)
	}
	if err = s.ActOnNotifications(ctx, member, c.ID, uuid.NewString(), NotificationAction{IDs: []string{memberPage.Notifications[0].ID}, Action: "archive"}); !errors.Is(err, ErrTransition) {
		t.Fatal("archived unread alert", err)
	}
	if err = s.ActOnNotifications(ctx, owner, c.ID, uuid.NewString(), NotificationAction{IDs: []string{n.ID}, Action: "archive"}); err != nil {
		t.Fatal(err)
	}
	ownerPage, err = s.Notifications(ctx, owner, c.ID, "", 50)
	if err != nil || len(ownerPage.Notifications) != 0 {
		t.Fatal("archive", ownerPage, err)
	}
	events, err := s.Events(ctx, viewer, c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "notification.changed" {
			t.Fatal("private notification event leaked")
		}
	}
	input.ExpectedRevision = session.Revision
	input.Shared = false
	if _, err = s.PublishSession(ctx, owner, c.ID, uuid.NewString(), input); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Notification(ctx, member, c.ID, memberPage.Notifications[0].ID); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked alert still readable", err)
	}
	if err = s.ActOnNotifications(ctx, member, c.ID, uuid.NewString(), NotificationAction{IDs: []string{memberPage.Notifications[0].ID}, Action: "read"}); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked read receipt accepted", err)
	}
	if err = s.RemoveMember(ctx, owner, c.ID, uuid.NewString(), member.UserID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE recipient_user_id=$1`, member.UserID).Scan(&count); err != nil || count != 0 {
		t.Fatal("membership removal retained recipient data", count, err)
	}
}

func TestNotificationRetentionPaginationAndReplay(t *testing.T) {
	s, pool, actors, c, p, profile := sessionFixture(t)
	ctx := context.Background()
	owner := actors[0]
	session, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), sessionInput(p, profile))
	if err != nil {
		t.Fatal(err)
	}
	r := completeNotificationRequest(t, s, owner, owner, c.ID, session.ID)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, owner, c.ID, false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1002; i++ {
		if err = upsertNotification(ctx, tx, owner, c.ID, owner.UserID, fmt.Sprintf("fixture:%04d", i), "finished", session.ID, r.ID, "", 0); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	cursor := ""
	for {
		page, err := s.Notifications(ctx, owner, c.ID, cursor, 79)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range page.Notifications {
			if ids[n.ID] {
				t.Fatal("duplicate page", n.ID)
			}
			ids[n.ID] = true
		}
		cursor = page.Next
		if cursor == "" {
			break
		}
	}
	if len(ids) != 1000 {
		t.Fatal("retention bound", len(ids))
	}
	if _, err = s.Notifications(ctx, actors[1], c.ID, uuid.NewSHA1(uuid.NameSpaceOID, []byte("camp-notification-v1:"+c.ID+":"+owner.UserID+":fixture:1001")).String(), 50); !errors.Is(err, ErrCursor) {
		t.Fatal("foreign cursor accepted", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE notifications SET created_at=now()-interval '31 days' WHERE camp_id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	page, err := s.Notifications(ctx, owner, c.ID, "", 50)
	if err != nil || len(page.Notifications) != 0 || page.Unread != 0 {
		t.Fatal("expired entries visible", page, err)
	}
}

func TestPlanningNotificationOnlyAfterCombinedResult(t *testing.T) {
	s, actors, c, input := planningFixture(t)
	a := actors[0]
	ctx := context.Background()
	p := createAndStartPlanning(t, s, a, c, input)
	for i := 0; i < 5; i++ {
		r := claimPlanning(t, s, a, c, input)
		if r == nil {
			t.Fatal("missing plan step")
		}
		if _, err := s.ActOnRequest(ctx, a, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: r.LeaseToken}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ActOnRequest(ctx, a, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "finish", LeaseToken: r.LeaseToken, Outcome: "completed", Evidence: "Completed", Output: "Synthetic plan output"}); err != nil {
			t.Fatal(err)
		}
		page, err := s.Notifications(ctx, a, c.ID, "", 50)
		if err != nil {
			t.Fatal(err)
		}
		if i < 4 && len(page.Notifications) != 0 {
			t.Fatal("individual planning step generated a notification")
		}
		if i == 4 && (len(page.Notifications) != 1 || page.Notifications[0].Kind != "plan" || page.Notifications[0].PlanID != p.ID || page.Notifications[0].PlanVersion != 1) {
			t.Fatal("missing exact planning result", page)
		}
	}
}

func TestPlanningResultRechecksSourceAccess(t *testing.T) {
	for _, target := range []string{"planner", "context"} {
		t.Run(target, func(t *testing.T) {
			s, actors, c, input := planningFixture(t)
			ctx := context.Background()
			owner, reader := actors[0], actors[1]
			source, err := s.Session(ctx, owner, c.ID, input.Planners[0].SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if target == "context" {
				source, err = s.PublishSession(ctx, owner, c.ID, uuid.NewString(), SessionInput{
					ID: uuid.NewString(), ProjectID: source.ProjectID, ProfileID: source.ProfileID, LocalSessionID: uuid.NewString(),
					Title: "Private context", Model: source.Model, State: "idle", Stage: "Ready", Shared: true,
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.WriteTranscript(ctx, owner, c.ID, source.ID, uuid.NewString(), TranscriptInput{Messages: []MessageInput{{ID: "context", Ordinal: 0, Role: "assistant", Text: "Synthetic context"}}}); err != nil {
					t.Fatal(err)
				}
				source, err = s.Session(ctx, owner, c.ID, source.ID)
				if err != nil {
					t.Fatal(err)
				}
				input.Sources = []SourceRef{{SessionID: source.ID, TranscriptRevision: source.TranscriptRevision, MessageIDs: []string{"context"}}}
			}
			input.Shared = true
			plan, err := s.CreatePlanning(ctx, owner, c.ID, uuid.NewString(), input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Planning(ctx, reader, c.ID, plan.ID, 1); err != nil {
				t.Fatal("shared result denied", err)
			}
			_, err = s.PublishSession(ctx, owner, c.ID, uuid.NewString(), SessionInput{
				ID: source.ID, ProjectID: source.ProjectID, ProfileID: source.ProfileID, LocalSessionID: source.LocalSessionID,
				Title: source.Title, Model: source.Model, State: source.State, Stage: source.Stage, Shared: false, ExpectedRevision: source.Revision,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Planning(ctx, reader, c.ID, plan.ID, 1); !errors.Is(err, ErrDenied) {
				t.Fatal("revoked result leaked", err)
			}
			if _, err = s.Planning(ctx, owner, c.ID, plan.ID, 1); err != nil {
				t.Fatal("owner lost private result", err)
			}
		})
	}
}

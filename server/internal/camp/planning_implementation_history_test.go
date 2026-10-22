package camp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestImplementationDiscoveryAndFullHistory(t *testing.T) {
	s, actors, c, p, target, approval := approvedImplementationFixture(t, true)
	r := dispatchImplementationFixture(t, s, actors[0], c, p, approval)
	// Identical timestamps deliberately exercise the UUID tie-breaker. These
	// are saved synthetic completed turns, not provider executions.
	_, err := s.pool.Exec(t.Context(), `INSERT INTO prompt_requests SELECT (jsonb_populate_record(NULL::prompt_requests,to_jsonb(r)||jsonb_build_object('id',gen_random_uuid(),'state','completed'))).* FROM prompt_requests r CROSS JOIN generate_series(1,104) WHERE r.id=$1`, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Implementation(t.Context(), actors[0], c.ID, p.ID, r.ImplementationID)
	if err != nil || len(first.Requests) != 100 || first.RequestsBefore == "" {
		t.Fatal("full history cursor", err)
	}
	seen := map[string]bool{}
	for _, v := range first.Requests {
		seen[v.ID] = true
	}
	page, err := s.ImplementationRequests(t.Context(), actors[1], c.ID, p.ID, r.ImplementationID, first.RequestsBefore, 20)
	if err != nil || len(page.Requests) != 5 || page.RequestsBefore != "" {
		t.Fatal("older requests", err)
	}
	for _, v := range page.Requests {
		if seen[v.ID] || v.LeaseToken != "" {
			t.Fatal("overlapping history or leaked lease")
		}
		seen[v.ID] = true
	}
	if _, err = s.pool.Exec(t.Context(), `UPDATE prompt_requests SET prompt=$2 WHERE implementation_id=$1`, r.ImplementationID, strings.Repeat("\x01", 65536)); err != nil {
		t.Fatal(err)
	}
	bounded, err := s.ImplementationRequests(t.Context(), actors[0], c.ID, p.ID, r.ImplementationID, "", 20)
	if err != nil || len(bounded.Requests) == 0 || len(bounded.Requests) >= 20 || bounded.RequestsBefore == "" {
		t.Fatal("escaped prompt pagination", err)
	}
	encoded, err := json.Marshal(bounded)
	if err != nil || len(encoded) > (2<<20)+4096 {
		t.Fatal("oversized native page", err)
	}
	next, err := s.ImplementationRequests(t.Context(), actors[0], c.ID, p.ID, r.ImplementationID, bounded.RequestsBefore, 20)
	if err != nil || len(next.Requests) == 0 || next.Requests[0].ID == bounded.RequestsBefore {
		t.Fatal("byte-bound page lost history", err)
	}
	for range 22 {
		approvalID, implementationID := uuid.NewString(), uuid.NewString()
		_, err = s.pool.Exec(t.Context(), `INSERT INTO planning_execution_approvals SELECT (jsonb_populate_record(NULL::planning_execution_approvals,to_jsonb(a)||jsonb_build_object('id',$2::text))).* FROM planning_execution_approvals a WHERE a.id=$1`, approval.ID, approvalID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.pool.Exec(t.Context(), `INSERT INTO planning_execution_sources(approval_id,camp_id,session_id,permission_revision,transcript_revision) SELECT $2,camp_id,session_id,permission_revision,transcript_revision FROM planning_execution_sources WHERE approval_id=$1`, approval.ID, approvalID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.pool.Exec(t.Context(), `INSERT INTO planning_implementations SELECT (jsonb_populate_record(NULL::planning_implementations,to_jsonb(i)||jsonb_build_object('id',$2::text,'approval_id',$3::text))).* FROM planning_implementations i WHERE i.id=$1`, r.ImplementationID, implementationID, approvalID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.pool.Exec(t.Context(), `INSERT INTO prompt_requests SELECT (jsonb_populate_record(NULL::prompt_requests,to_jsonb(r)||jsonb_build_object('id',gen_random_uuid(),'implementation_id',$2::text,'state','completed'))).* FROM prompt_requests r WHERE r.id=$1`, r.ID, implementationID)
		if err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.Implementations(t.Context(), actors[0], c.ID, p.ID, 1, "", 20)
	if err != nil || len(list.Implementations) != 20 || list.Before == "" {
		t.Fatal("discovery first page", err)
	}
	seen = map[string]bool{}
	for _, v := range list.Implementations {
		if v.Provider != target.Provider || v.Model != approval.Model || v.SessionID != target.ID {
			t.Fatal("executor settings")
		}
		seen[v.ID] = true
	}
	older, err := s.Implementations(t.Context(), actors[1], c.ID, p.ID, 1, list.Before, 20)
	if err != nil || len(older.Implementations) != 3 || older.Before != "" {
		t.Fatal("discovery older page", err)
	}
	for _, v := range older.Implementations {
		if seen[v.ID] {
			t.Fatal("duplicate implementation")
		}
	}
	other, err := s.Implementations(t.Context(), actors[2], c.ID, p.ID, 1, "", 20)
	if err != nil || len(other.Implementations) != 0 {
		t.Fatal("unrelated member discovery", err)
	}
	if _, err = s.ImplementationRequests(t.Context(), actors[2], c.ID, p.ID, r.ImplementationID, first.RequestsBefore, 20); !errors.Is(err, ErrDenied) {
		t.Fatal("unrelated member history", err)
	}
	unknown, err := s.Implementations(t.Context(), actors[0], c.ID, p.ID, 1, uuid.NewString(), 20)
	if err != nil || len(unknown.Implementations) != 0 {
		t.Fatal("foreign cursor", err)
	}
	if _, err = s.Implementations(t.Context(), actors[0], c.ID, p.ID, 1, "", 21); !errors.Is(err, ErrInput) {
		t.Fatal("unbounded discovery", err)
	}
	// A changed grant stays revoked even if the source is shared again. Both
	// the empty history page and discovery must withhold frozen identities.
	_, err = s.pool.Exec(t.Context(), `UPDATE sessions SET permission_revision=permission_revision+1 WHERE id=$1`, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	list, err = s.Implementations(t.Context(), actors[0], c.ID, p.ID, 1, "", 20)
	if err != nil || len(list.Implementations) != 0 {
		t.Fatal("revoked discovery", err)
	}
	if _, err = s.ImplementationRequests(t.Context(), actors[1], c.ID, p.ID, r.ImplementationID, uuid.NewString(), 20); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked empty page", err)
	}
}

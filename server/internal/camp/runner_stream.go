package camp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

type RunnerTurn struct {
	Request           Request                  `json:"request"`
	Context           json.RawMessage          `json:"context"`
	ProviderSessionID string                   `json:"provider_session_id"`
	History           []localcli.HistorySource `json:"history,omitempty"`
	GitIdentity       *RunnerGitIdentity       `json:"git_identity,omitempty"`
}

// RunnerGitIdentity attributes commits made during a cloud turn. The
// committer is the workspace owner's GitHub account; the author is the member
// who asked for the turn when they have connected GitHub. Both use GitHub
// noreply addresses so no private email enters the history.
type RunnerGitIdentity struct {
	AuthorName     string `json:"author_name"`
	AuthorEmail    string `json:"author_email"`
	CommitterName  string `json:"committer_name"`
	CommitterEmail string `json:"committer_email"`
}

func githubNoreply(id int64, login string) string {
	return strconv.FormatInt(id, 10) + "+" + login + "@users.noreply.github.com"
}

func runnerGitIdentity(ctx context.Context, tx pgx.Tx, workspaceID string, r Request) (*RunnerGitIdentity, error) {
	var ownerID int64
	var ownerLogin string
	if err := tx.QueryRow(ctx, `SELECT github_user_id,github_login FROM ticket_workspaces WHERE id=$1`, workspaceID).Scan(&ownerID, &ownerLogin); err != nil {
		return nil, err
	}
	if ownerID <= 0 || ownerLogin == "" {
		return nil, nil
	}
	out := &RunnerGitIdentity{AuthorName: ownerLogin, AuthorEmail: githubNoreply(ownerID, ownerLogin), CommitterName: ownerLogin, CommitterEmail: githubNoreply(ownerID, ownerLogin)}
	if r.RequesterUserID == r.OwnerUserID {
		return out, nil
	}
	var id int64
	var login string
	err := tx.QueryRow(ctx, `SELECT github_user_id,github_login FROM git_connections WHERE owner_user_id=$1 AND state='connected' AND github_user_id IS NOT NULL ORDER BY updated_at DESC LIMIT 1`, r.RequesterUserID).Scan(&id, &login)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if id > 0 && login != "" {
		out.AuthorName, out.AuthorEmail = login, githubNoreply(id, login)
	}
	return out, nil
}
type StreamEvent struct {
	Seq   int64           `json:"seq"`
	Event json.RawMessage `json:"event"`
}
type StreamPage struct {
	Events []StreamEvent `json:"events"`
	Next   int64         `json:"next"`
}

// StreamHead reveals only display metadata to a session reader. Queued prompts,
// account identities and execution leases remain private to their owners.
type StreamHead struct {
	LocalContinuation bool   `json:"local_continuation,omitempty"`
	ID                string `json:"id"`
	State             string `json:"state"`
	Seq               int64  `json:"seq"`
	Model             string `json:"model"`
	Effort            string `json:"effort"`
	Speed             string `json:"speed"`
}

func (s *Store) SessionStream(ctx context.Context, a auth.Actor, campID, id string) (*StreamHead, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return nil, err
	}
	if _, err = s.readSession(ctx, tx, a, campID, id); err != nil {
		return nil, err
	}
	var out StreamHead
	err = tx.QueryRow(ctx, `SELECT id,state,stream_seq,model,effort,speed,(`+localContinuationSQL+`) FROM prompt_requests r WHERE session_id=$1 AND kind='chat' AND (execution_location='cloud' OR (`+localContinuationSQL+`)) AND (stream_seq>0 OR state IN ('claimed','started','cancel_requested','outcome_unknown')) ORDER BY (state IN ('claimed','started','cancel_requested','outcome_unknown')) DESC,created_at DESC,id DESC LIMIT 1`, id).Scan(&out.ID, &out.State, &out.Seq, &out.Model, &out.Effort, &out.Speed, &out.LocalContinuation)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = readStreamSettings(ctx, tx, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func readStreamSettings(ctx context.Context, tx pgx.Tx, out *StreamHead) error {
	var settings []byte
	err := tx.QueryRow(ctx, `SELECT payload FROM request_stream_events WHERE request_id=$1 AND payload->>'type'='settings' ORDER BY seq DESC LIMIT 1`, out.ID).Scan(&settings)
	if err == nil {
		var value map[string]any
		if json.Unmarshal(settings, &value) != nil {
			return ErrInput
		}
		if model := strEvent(value, "model"); model != "" {
			out.Model = model
		}
		if effort := strEvent(value, "effort"); effort != "" {
			out.Effort = effort
		}
		if speed := strEvent(value, "speed"); speed != "" {
			out.Speed = speed
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return nil
}

func (g *GitStore) RunnerRequestStatus(ctx context.Context, token, workspaceID, id string) (Request, error) {
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return Request{}, err
	}
	defer tx.Rollback(ctx)
	l, session, err := g.lockExecution(ctx, tx, token, workspaceID)
	if err != nil {
		return Request{}, err
	}
	r, err := loadRequest(ctx, tx, l.value.CampID, id)
	if err != nil {
		return Request{}, err
	}
	if r.ExecutionWorkspaceID != workspaceID || r.ExecutionEpoch != session.ExecutionEpoch {
		return Request{}, ErrDenied
	}
	r.Prompt = ""
	r.Sources = nil
	r.LeaseToken = ""
	return r, nil
}

func (g *GitStore) lockedRunnerRequest(ctx context.Context, tx pgx.Tx, token, workspaceID, id string) (Request, error) {
	l, session, err := g.lockExecution(ctx, tx, token, workspaceID)
	if err != nil {
		return Request{}, err
	}
	r, err := loadRequest(ctx, tx, l.value.CampID, id)
	if err != nil {
		return r, err
	}
	if r.RunnerGrantID != l.runner.grantID || r.ExecutionEpoch != session.ExecutionEpoch || r.ExecutionWorkspaceID != workspaceID || r.ExecutionLocation != "cloud" {
		return r, ErrDenied
	}
	return r, nil
}

func (g *GitStore) RunnerTurn(ctx context.Context, token, workspaceID, id string) (RunnerTurn, error) {
	return g.runnerTurn(ctx, token, workspaceID, id, false)
}
func (g *GitStore) PagedRunnerTurn(ctx context.Context, token, workspaceID, id string) (RunnerTurn, error) {
	return g.runnerTurn(ctx, token, workspaceID, id, true)
}
func (g *GitStore) runnerTurn(ctx context.Context, token, workspaceID, id string, paged bool) (RunnerTurn, error) {
	s := g.store
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RunnerTurn{}, err
	}
	defer tx.Rollback(ctx)
	r, err := g.lockedRunnerRequest(ctx, tx, token, workspaceID, id)
	if err != nil {
		return RunnerTurn{}, err
	}
	live, err := hasLiveLease(ctx, tx, r.ID)
	if err != nil {
		return RunnerTurn{}, err
	}
	if !live || !oneOf(r.State, "claimed", "started", "cancel_requested") {
		return RunnerTurn{}, ErrTransition
	}
	if _, err = s.requestAccess(ctx, tx, r); err != nil {
		return RunnerTurn{}, err
	}
	var providerSession string
	err = tx.QueryRow(ctx, `SELECT provider_session_id FROM workspace_runtime WHERE workspace_id=$1`, workspaceID).Scan(&providerSession)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return RunnerTurn{}, err
	}
	if r.Kind == "implementation" {
		providerSession = r.ProviderSessionID
	}
	sources := []map[string]any{}
	size := 0
	refs := append([]SourceRef{}, r.Sources...)
	history := []localcli.HistorySource{}
	if r.Kind != "implementation" && r.ContextRevision > 0 && (paged || providerSession == "" || r.Mode == "planning") {
		if !paged {
			refs = append([]SourceRef{{SessionID: r.SessionID, TranscriptRevision: r.ContextRevision}}, refs...)
		} else {
			session, e := s.readSession(ctx, tx, auth.Actor{UserID: r.OwnerUserID}, r.CampID, r.SessionID)
			if e != nil {
				return RunnerTurn{}, e
			}
			history, e = runnerHistorySources(ctx, tx, session, r.ContextRevision)
			if e != nil {
				return RunnerTurn{}, e
			}
		}
	}
	for index, ref := range refs {
		messages, err := contextRefMessages(ctx, tx, ref)
		if err != nil {
			return RunnerTurn{}, err
		}
		if len(messages) == 0 {
			continue
		}
		values := []map[string]string{}
		for _, m := range messages {
			size += len(m.Text)
			if len(m.Text) > 48<<10 || size > 256<<10 {
				return RunnerTurn{}, ErrInput
			}
			values = append(values, map[string]string{"id": m.ID, "role": m.Role, "text": m.Text})
		}
		sources = append(sources, map[string]any{"id": "camp:" + ref.SessionID + ":" + strconv.Itoa(index), "title": "Camp session", "revision": strconv.FormatInt(ref.TranscriptRevision, 10), "messages": values})
	}
	for _, quote := range r.QuotedSources {
		values := []map[string]string{}
		for _, m := range quote.Messages {
			size += len(m.Text)
			if len(m.Text) > 48<<10 || size > 256<<10 {
				return RunnerTurn{}, ErrInput
			}
			values = append(values, map[string]string{"id": m.ID, "role": m.Role, "text": m.Text})
		}
		sources = append(sources, map[string]any{"id": "plan-source:" + r.PlanID + ":" + strconv.FormatInt(r.PlanVersion, 10) + ":" + quote.ID, "title": quote.Title, "revision": quote.Revision, "messages": values})
	}
	if len(sources) > 20 {
		return RunnerTurn{}, ErrInput
	}
	contextJSON, _ := json.Marshal(sources)
	identity, err := runnerGitIdentity(ctx, tx, workspaceID, r)
	if err != nil {
		return RunnerTurn{}, err
	}
	return RunnerTurn{Request: r, Context: contextJSON, ProviderSessionID: providerSession, History: history, GitIdentity: identity}, nil
}

// contextRefMessages reads one source as a turn sends it: the selected
// messages, or the whole session up to 120 messages at the pinned revision.
func contextRefMessages(ctx context.Context, tx pgx.Tx, ref SourceRef) ([]Message, error) {
	if len(ref.MessageIDs) > 0 {
		return readContextMessages(ctx, tx, ref)
	}
	rows, err := tx.Query(ctx, `SELECT m.local_id,m.ordinal,v.role,v.text,v.removed,v.revision,v.model,v.effort,v.speed,v.attachments FROM messages m CROSS JOIN LATERAL(SELECT * FROM message_revisions WHERE session_id=m.session_id AND local_id=m.local_id AND revision<=$2 ORDER BY revision DESC LIMIT 1) v WHERE m.session_id=$1 AND NOT v.removed ORDER BY m.ordinal LIMIT 121`, ref.SessionID, ref.TranscriptRevision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []Message
	for rows.Next() {
		var m Message
		if err = rows.Scan(&m.ID, &m.Ordinal, &m.Role, &m.Text, &m.Removed, &m.Revision, &m.Model, &m.Effort, &m.Speed, &m.Attachments); err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(messages) > 120 {
		return nil, ErrInput
	}
	return messages, nil
}

func validStreamEvent(raw json.RawMessage) (map[string]any, []byte, error) {
	if len(raw) > 128<<10 || !utf8.Valid(raw) {
		return nil, nil, ErrInput
	}
	var event map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&event) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, nil, ErrInput
	}
	kind, _ := event["type"].(string)
	keys := map[string]string{
		"delta": "type,text", "text": "type,text", "reset": "type", "activity": "type,text", "permissions": "type,text",
		"session": "type,sessionID", "settings": "type,model,effort,speed", "usage": "type,inputTokens,outputTokens", "started": "type,turnID",
		"approval": "type,id,kind,title,detail,questions,canApprove,decisions,acceptLabel", "approvalResolved": "type,requestID", "approvalRejected": "type,requestID,message",
		"steerAccepted": "type,requestID", "steerRejected": "type,requestID,message", "notice": "type,message", "error": "type,message", "done": "type", "cancelled": "type,text", "boundary": "type",
	}
	allowed, ok := keys[kind]
	if !ok {
		return nil, nil, ErrInput
	}
	for key := range event {
		if !strings.Contains(","+allowed+",", ","+key+",") {
			return nil, nil, ErrInput
		}
	}
	if oneOf(kind, "delta", "text", "activity", "permissions") {
		if _, ok = event["text"].(string); !ok {
			return nil, nil, ErrInput
		}
	}
	for _, key := range []string{"text", "message", "requestID", "sessionID", "turnID", "model", "effort", "speed", "id", "kind", "title", "detail", "acceptLabel"} {
		if value, exists := event[key]; exists {
			if _, valid := value.(string); !valid {
				return nil, nil, ErrInput
			}
		}
	}
	if kind == "settings" {
		for key, limit := range map[string]int{"model": 200, "effort": 64, "speed": 64} {
			if len(strEvent(event, key)) > limit {
				return nil, nil, ErrInput
			}
		}
	}
	for _, key := range []string{"inputTokens", "outputTokens"} {
		if value, exists := event[key]; exists {
			number, valid := value.(json.Number)
			if !valid {
				return nil, nil, ErrInput
			}
			count, err := number.Int64()
			if err != nil || count < 0 || count > 9007199254740991 {
				return nil, nil, ErrInput
			}
		}
	}
	if kind == "approval" {
		id, _ := event["id"].(string)
		approvalKind, _ := event["kind"].(string)
		if !validID(id) || !oneOf(approvalKind, "command", "file", "permissions", "tool", "question") {
			return nil, nil, ErrInput
		}
		if _, valid := event["canApprove"].(bool); !valid {
			return nil, nil, ErrInput
		}
		questions, valid := event["questions"].([]any)
		if !valid || len(questions) > 8 {
			return nil, nil, ErrInput
		}
		if len(strEvent(event, "title")) > 512 || len(strEvent(event, "detail")) > 65536 {
			return nil, nil, ErrInput
		}
	}
	canonical, err := json.Marshal(event)
	if err != nil {
		return nil, nil, ErrInput
	}
	return event, canonical, nil
}

func strEvent(event map[string]any, key string) string { value, _ := event[key].(string); return value }

func (g *GitStore) AppendRunnerEvent(ctx context.Context, token, workspaceID, id string, input StreamEvent) (int64, error) {
	event, raw, err := validStreamEvent(input.Event)
	if err != nil || input.Seq < 1 {
		return 0, ErrInput
	}
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	r, err := g.lockedRunnerRequest(ctx, tx, token, workspaceID, id)
	if err != nil {
		return 0, err
	}
	seq, err := g.store.appendRequestEvent(ctx, tx, r, input, event, raw, workspaceID)
	if err != nil {
		return 0, err
	}
	return seq, tx.Commit(ctx)
}

// Both official CLI workers persist through the same ordered, bounded event log.
// Ownership/lease authentication is performed by their separate entry points.
func (s *Store) appendRequestEvent(ctx context.Context, tx pgx.Tx, r Request, input StreamEvent, event map[string]any, raw []byte, workspaceID string) (int64, error) {
	if _, err := s.requestAccess(ctx, tx, r); err != nil {
		return 0, err
	}
	var err error
	hash := sha256.Sum256(raw)
	if input.Seq <= r.StreamSeq {
		var stored []byte
		if err = tx.QueryRow(ctx, `SELECT payload_hash FROM request_stream_events WHERE request_id=$1 AND seq=$2`, r.ID, input.Seq).Scan(&stored); err != nil {
			return 0, err
		}
		if subtle.ConstantTimeCompare(stored, hash[:]) != 1 {
			return 0, ErrConflict
		}
		return r.StreamSeq, nil
	}
	live, err := hasLiveLease(ctx, tx, r.ID)
	if err != nil {
		return 0, err
	}
	if !live || !oneOf(r.State, "started", "cancel_requested") || input.Seq != r.StreamSeq+1 {
		return 0, ErrTransition
	}
	var total, textBytes int64
	if err = tx.QueryRow(ctx, `SELECT stream_bytes,stream_text_bytes FROM prompt_requests WHERE id=$1`, r.ID).Scan(&total, &textBytes); err != nil {
		return 0, err
	}
	kind, _ := event["type"].(string)
	text, _ := event["text"].(string)
	switch kind {
	case "reset":
		textBytes = 0
	case "text":
		textBytes = int64(len(text))
	case "delta":
		textBytes += int64(len(text))
	}
	if input.Seq > 32768 || total+int64(len(raw)) > 16<<20 || textBytes > 2<<20 {
		return 0, ErrInput
	}
	if _, err = tx.Exec(ctx, `INSERT INTO request_stream_events(request_id,seq,payload,payload_hash) VALUES($1,$2,$3,$4)`, r.ID, input.Seq, raw, hash[:]); err != nil {
		return 0, err
	}
	if kind == "session" && (r.Mode != "planning" || r.LocalContinuation) {
		providerSession, _ := event["sessionID"].(string)
		if providerSession == "" || len(providerSession) > 200 || strings.ContainsAny(providerSession, "/\\\x00\r\n") {
			return 0, ErrInput
		}
		if r.LocalContinuation {
			if err = saveLocalContinuationSession(ctx, tx, r, providerSession); err != nil {
				return 0, err
			}
		} else if r.Kind == "implementation" {
			if err = saveImplementationSession(ctx, tx, r, providerSession); err != nil {
				return 0, err
			}
		} else if _, err = tx.Exec(ctx, `INSERT INTO workspace_runtime(workspace_id,provider_session_id) VALUES($1,$2) ON CONFLICT(workspace_id) DO UPDATE SET provider_session_id=EXCLUDED.provider_session_id`, workspaceID, providerSession); err != nil {
			return 0, err
		}
	}
	if err = tx.QueryRow(ctx, `UPDATE prompt_requests SET stream_seq=$2,stream_bytes=stream_bytes+$3,stream_text_bytes=$4,revision=revision+1 WHERE id=$1 RETURNING revision`, r.ID, input.Seq, len(raw), textBytes).Scan(&r.Revision); err != nil {
		return 0, err
	}
	r.StreamSeq = input.Seq
	if err = s.settleControl(ctx, tx, r, event); err != nil {
		return 0, err
	}
	if oneOf(kind, "approval", "approvalResolved", "approvalRejected") {
		if err = s.cloudActivity(ctx, tx, r); err != nil {
			return 0, err
		}
	}
	if err = s.emitCloudRequest(ctx, tx, r, "request.stream"); err != nil {
		return 0, err
	}
	if kind == "settings" && r.Kind == "implementation" {
		if err = s.emitCloudRequest(ctx, tx, r, "request.settings"); err != nil {
			return 0, err
		}
	}
	return input.Seq, nil
}

func (s *Store) RequestStream(ctx context.Context, a auth.Actor, campID, id string, after int64) (StreamPage, error) {
	out := StreamPage{Events: []StreamEvent{}, Next: after}
	if after < 0 {
		return out, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return out, err
	}
	r, err := loadRequest(ctx, tx, campID, id)
	if err != nil {
		return out, err
	}
	if _, err = s.readSession(ctx, tx, a, campID, r.SessionID); err != nil {
		return out, err
	}
	if err = requestContentAccess(ctx, tx, a, r); err != nil {
		return out, err
	}
	if oneOf(r.Kind, "planning", "implementation") {
		p, err := s.loadPlanning(ctx, tx, a, campID, r.PlanID, r.PlanVersion)
		if err != nil {
			return out, err
		}
		if err = s.checkPlanningSources(ctx, tx, a, p); err != nil {
			return out, err
		}
	}
	rows, err := tx.Query(ctx, `SELECT seq,payload FROM request_stream_events WHERE request_id=$1 AND seq>$2 ORDER BY seq LIMIT 100`, id, after)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	size := 0
	for rows.Next() {
		var e StreamEvent
		if err = rows.Scan(&e.Seq, &e.Event); err != nil {
			return out, err
		}
		size += len(e.Event)
		if size > 512<<10 {
			break
		}
		out.Events = append(out.Events, e)
		out.Next = e.Seq
	}
	return out, rows.Err()
}

func (s *Store) publishRunnerTranscript(ctx context.Context, tx pgx.Tx, r Request) error {
	if r.Kind != "chat" || r.StreamSeq == 0 {
		return nil
	}
	var published bool
	if err := tx.QueryRow(ctx, `SELECT stream_published FROM prompt_requests WHERE id=$1`, r.ID).Scan(&published); err != nil {
		return err
	}
	if published {
		return nil
	}
	accepted := map[string]string{}
	controls, err := tx.Query(ctx, `SELECT provider_id,accepted_text FROM request_controls WHERE request_id=$1 AND kind='steer' AND accepted_text<>''`, r.ID)
	if err != nil {
		return err
	}
	for controls.Next() {
		var id, text string
		if err = controls.Scan(&id, &text); err != nil {
			controls.Close()
			return err
		}
		accepted[id] = text
	}
	err = controls.Err()
	controls.Close()
	if err != nil {
		return err
	}
	type part struct{ role, text string }
	parts := []part{{"user", r.Prompt}}
	var text strings.Builder
	flush := func() {
		remaining := text.String()
		for len(remaining) > 0 {
			n := len(remaining)
			if n > 128<<10 {
				n = 128 << 10
				for !utf8.RuneStart(remaining[n]) {
					n--
				}
			}
			parts = append(parts, part{"assistant", remaining[:n]})
			remaining = remaining[n:]
		}
		text.Reset()
	}
	rows, err := tx.Query(ctx, `SELECT payload FROM request_stream_events WHERE request_id=$1 ORDER BY seq`, r.ID)
	if err != nil {
		return err
	}
	model, effort, speed := r.Model, r.Effort, r.Speed
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var e map[string]any
		if json.Unmarshal(raw, &e) != nil {
			rows.Close()
			return ErrInput
		}
		kind, _ := e["type"].(string)
		value, _ := e["text"].(string)
		switch kind {
		case "boundary":
			flush()
		case "steerAccepted":
			if id, ok := e["requestID"].(string); ok && accepted[id] != "" {
				flush()
				parts = append(parts, part{"user", accepted[id]})
			}
		case "reset":
			text.Reset()
		case "text":
			text.Reset()
			text.WriteString(value)
		case "delta":
			text.WriteString(value)
		case "settings":
			if v, ok := e["model"].(string); ok && v != "" {
				model = v
			}
			if v, ok := e["effort"].(string); ok && v != "" {
				effort = v
			}
			if v, ok := e["speed"].(string); ok && v != "" {
				speed = v
			}
		}
		if text.Len() > 2<<20 {
			rows.Close()
			return ErrInput
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var revision, ordinal int64
	if err = tx.QueryRow(ctx, `SELECT transcript_revision+1 FROM sessions WHERE id=$1`, r.SessionID).Scan(&revision); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT COALESCE(max(ordinal)+1,0) FROM messages WHERE session_id=$1`, r.SessionID).Scan(&ordinal); err != nil {
		return err
	}
	flush()
	for i, part := range parts {
		prefix := "cloud:"
		if r.LocalContinuation {
			prefix = "camp:"
		}
		id := prefix + r.ID + ":" + strconv.Itoa(i)
		if _, err = tx.Exec(ctx, `INSERT INTO messages(session_id,local_id,ordinal) VALUES($1,$2,$3)`, r.SessionID, id, ordinal+int64(i)); err != nil {
			return err
		}
		// The prompt belongs to whoever asked for this turn, not the owner.
		var author any
		if part.role == "user" && validID(r.RequesterUserID) {
			author = r.RequesterUserID
		}
		if _, err = tx.Exec(ctx, `INSERT INTO message_revisions(session_id,local_id,revision,role,text,model,effort,speed,author_user_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, r.SessionID, id, revision, part.role, part.text, model, effort, speed, author); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE sessions SET transcript_revision=$2,updated_at=now() WHERE id=$1`, r.SessionID, revision); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE prompt_requests SET stream_published=true WHERE id=$1`, r.ID); err != nil {
		return err
	}
	session, err := s.readSession(ctx, tx, auth.Actor{UserID: r.OwnerUserID}, r.CampID, r.SessionID)
	if err != nil {
		return err
	}
	return emitSession(ctx, tx, auth.Actor{UserID: r.OwnerUserID}, r.CampID, "transcript.updated", session, false)
}

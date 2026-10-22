package camp

import (
	"context"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

var ErrDenied = errors.New("Camp or operation is unavailable")
var ErrInput = errors.New("invalid input")
var ErrCursor = errors.New("request an authorized snapshot")

type Store struct {
	pool           *pgxpool.Pool
	secret         []byte
	providerCipher cipher.AEAD
	transferSlots  chan struct{}
}

func New(pool *pgxpool.Pool, secret string) *Store {
	return &Store{pool: pool, secret: []byte(secret), transferSlots: make(chan struct{}, 2)}
}

type Camp struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Role   string `json:"role"`
	Cursor int64  `json:"cursor"`
}
type Project struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Revision     int64    `json:"revision"`
	Archived     bool     `json:"archived"`
	Inbox        bool     `json:"inbox"`
	Repositories []string `json:"repositories"`
}
type Member struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	Role   string `json:"role"`
}
type Snapshot struct {
	Camp     Camp      `json:"camp"`
	Members  []Member  `json:"members"`
	Projects []Project `json:"projects"`
}
type Invitation struct {
	ID        string    `json:"id"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}
type Event struct {
	Cursor  int64           `json:"cursor"`
	Kind    string          `json:"kind"`
	ActorID string          `json:"actor_id"`
	Payload json.RawMessage `json:"payload"`
}

func validName(s string) bool { return len(strings.TrimSpace(s)) > 0 && len(s) <= 120 }
func validID(id string) bool  { _, err := uuid.Parse(id); return err == nil }

// lock serializes Camp mutations and their event numbers in commit order.
// It also fences membership revocation against writes already in progress.
func (s *Store) lock(ctx context.Context, tx pgx.Tx, a auth.Actor, id string, write bool) (Camp, error) {
	var c Camp
	if !validID(id) {
		return c, ErrDenied
	}
	err := tx.QueryRow(ctx, `SELECT c.id,c.name,m.role,c.event_cursor FROM camps c JOIN memberships m ON m.camp_id=c.id WHERE c.id=$1 AND m.user_id=$2 FOR UPDATE OF c`, id, a.UserID).Scan(&c.ID, &c.Name, &c.Role, &c.Cursor)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrDenied
	}
	if err != nil {
		return c, err
	}
	if write && c.Role == "viewer" {
		return c, ErrDenied
	}
	return c, nil
}
func emit(ctx context.Context, tx pgx.Tx, a auth.Actor, id, kind string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var cursor int64
	if err = tx.QueryRow(ctx, `UPDATE camps SET event_cursor=event_cursor+1 WHERE id=$1 RETURNING event_cursor`, id).Scan(&cursor); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO events(camp_id,cursor,kind,actor_id,payload) VALUES($1,$2,$3,$4,$5)`, id, cursor, kind, a.UserID, b)
	return err
}

func (s *Store) List(ctx context.Context, a auth.Actor) ([]Camp, error) {
	rows, err := s.pool.Query(ctx, `SELECT c.id,c.name,m.role,c.event_cursor FROM camps c JOIN memberships m ON m.camp_id=c.id WHERE m.user_id=$1 ORDER BY c.created_at,c.id LIMIT 100`, a.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Camp{}
	for rows.Next() {
		var c Camp
		if err = rows.Scan(&c.ID, &c.Name, &c.Role, &c.Cursor); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Store) Create(ctx context.Context, a auth.Actor, requestID, name string) (Camp, error) {
	name = strings.TrimSpace(name)
	if !validName(name) {
		return Camp{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Camp{}, err
	}
	defer tx.Rollback(ctx)
	c := Camp{}
	if hit, err := s.beginCommand(ctx, tx, a, requestID, "camp.create", name, &c); err != nil {
		return Camp{}, err
	} else if hit {
		return c, nil
	}
	c = Camp{ID: uuid.NewString(), Name: name, Role: "owner", Cursor: 1}
	if _, err = tx.Exec(ctx, `INSERT INTO camps(id,name) VALUES($1,$2)`, c.ID, c.Name); err != nil {
		return Camp{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO memberships(camp_id,user_id,role) VALUES($1,$2,'owner')`, c.ID, a.UserID); err != nil {
		return Camp{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO projects(id,camp_id,name,inbox) VALUES($1,$2,'Inbox',true)`, uuid.NewString(), c.ID); err != nil {
		return Camp{}, err
	}
	if err = emit(ctx, tx, a, c.ID, "camp.created", map[string]string{"id": c.ID, "name": name}); err != nil {
		return Camp{}, err
	}
	if err = s.finishCommand(ctx, tx, a, requestID, c); err != nil {
		return Camp{}, err
	}
	return c, tx.Commit(ctx)
}
func (s *Store) Snapshot(ctx context.Context, a auth.Actor, id string) (Snapshot, error) {
	return retryRead(ctx, func() (Snapshot, error) { return s.snapshotOnce(ctx, a, id) })
}
func (s *Store) snapshotOnce(ctx context.Context, a auth.Actor, id string) (Snapshot, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return Snapshot{}, err
	}
	defer tx.Rollback(ctx)
	c, err := s.lock(ctx, tx, a, id, false)
	if err != nil {
		return Snapshot{}, err
	}
	out := Snapshot{Camp: c, Members: []Member{}, Projects: []Project{}}
	rows, err := tx.Query(ctx, `SELECT m.user_id,u.display_name,m.role FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.camp_id=$1 ORDER BY u.display_name,u.id`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var m Member
		if err = rows.Scan(&m.UserID, &m.Name, &m.Role); err != nil {
			rows.Close()
			return out, err
		}
		out.Members = append(out.Members, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = tx.Query(ctx, `SELECT p.id,p.name,p.revision,p.archived,p.inbox,ARRAY(SELECT url FROM project_repository_links r WHERE r.project_id=p.id ORDER BY url) FROM projects p WHERE camp_id=$1 ORDER BY inbox DESC,archived,name,id`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var p Project
		if err = rows.Scan(&p.ID, &p.Name, &p.Revision, &p.Archived, &p.Inbox, &p.Repositories); err != nil {
			rows.Close()
			return out, err
		}
		out.Projects = append(out.Projects, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
func (s *Store) CreateProject(ctx context.Context, a auth.Actor, id, requestID, name string) (Project, error) {
	return s.CreateProjectWithRepositories(ctx, a, id, requestID, name, nil)
}
func (s *Store) CreateProjectWithRepositories(ctx context.Context, a auth.Actor, id, requestID, name string, repositories []string) (Project, error) {
	name = strings.TrimSpace(name)
	if !validName(name) {
		return Project{}, ErrInput
	}
	urls, err := repositoryURLs(repositories)
	if err != nil {
		return Project{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, id, true); err != nil {
		return Project{}, err
	}
	p := Project{}
	var commandInput any = name
	if len(urls) > 0 {
		commandInput = struct {
			Name         string
			Repositories []string
		}{name, urls}
	}
	if hit, err := s.beginCommand(ctx, tx, a, requestID, "project.create:"+id, commandInput, &p); err != nil {
		return Project{}, err
	} else if hit {
		return readProject(ctx, tx, id, p.ID)
	}
	p = Project{ID: uuid.NewString(), Name: name, Revision: 1, Repositories: urls}
	if _, err = tx.Exec(ctx, `INSERT INTO projects(id,camp_id,name) VALUES($1,$2,$3)`, p.ID, id, p.Name); err != nil {
		return Project{}, err
	}
	for _, url := range urls {
		if _, err = tx.Exec(ctx, `INSERT INTO project_repository_links(camp_id,project_id,url) VALUES($1,$2,$3)`, id, p.ID, url); err != nil {
			return Project{}, err
		}
	}
	if err = emit(ctx, tx, a, id, "project.created", p); err != nil {
		return Project{}, err
	}
	if err = s.finishCommand(ctx, tx, a, requestID, p); err != nil {
		return Project{}, err
	}
	return p, tx.Commit(ctx)
}
func (s *Store) Invite(ctx context.Context, a auth.Actor, id, requestID, email, role string) (Invitation, error) {
	email, err := auth.NormalizeEmail(email)
	if err != nil || (role != "editor" && role != "viewer") {
		return Invitation{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Invitation{}, err
	}
	defer tx.Rollback(ctx)
	c, err := s.lock(ctx, tx, a, id, true)
	if err != nil {
		return Invitation{}, err
	}
	if c.Role != "owner" {
		return Invitation{}, ErrDenied
	}
	inv := Invitation{}
	if hit, err := s.beginCommand(ctx, tx, a, requestID, "invite:"+id, []string{email, role}, &inv); err != nil {
		return Invitation{}, err
	} else if hit {
		return inv, nil
	}
	token, err := auth.Token()
	if err != nil {
		return Invitation{}, err
	}
	inv = Invitation{ID: uuid.NewString(), Token: token}
	err = tx.QueryRow(ctx, `INSERT INTO invitations(id,camp_id,email,role,token_hash,created_by,expires_at) VALUES($1,$2,$3,$4,$5,$6,now()+interval '7 days') RETURNING expires_at`, inv.ID, id, email, role, auth.HashToken(token), a.UserID).Scan(&inv.ExpiresAt)
	if err != nil {
		return Invitation{}, err
	}
	if err = s.finishCommand(ctx, tx, a, requestID, inv); err != nil {
		return Invitation{}, err
	}
	return inv, tx.Commit(ctx)
}
func (s *Store) Accept(ctx context.Context, a auth.Actor, requestID, token string) (string, error) {
	if len(token) != 48 {
		return "", ErrDenied
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, `SELECT camp_id FROM invitations WHERE token_hash=$1 AND email=$2`, auth.HashToken(token), a.Email).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrDenied
	}
	if err != nil {
		return "", err
	}
	// All mutations lock the Camp before locking its invitation or membership.
	if _, err = tx.Exec(ctx, `SELECT id FROM camps WHERE id=$1 FOR UPDATE`, id); err != nil {
		return "", err
	}
	var previous string
	if hit, err := s.beginCommand(ctx, tx, a, requestID, "invite.accept", token, &previous); err != nil {
		return "", err
	} else if hit {
		if _, err = s.lock(ctx, tx, a, previous, false); err != nil {
			return "", err
		}
		return previous, nil
	}
	var role string
	err = tx.QueryRow(ctx, `UPDATE invitations SET accepted_at=now() WHERE token_hash=$1 AND email=$2 AND accepted_at IS NULL AND expires_at>now() RETURNING role`, auth.HashToken(token), a.Email).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrDenied
	}
	if err != nil {
		return "", err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO memberships(camp_id,user_id,role) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, a.UserID, role)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() > 0 {
		if err = emit(ctx, tx, a, id, "member.joined", Member{UserID: a.UserID, Name: a.Name, Role: role}); err != nil {
			return "", err
		}
	}
	if err = s.finishCommand(ctx, tx, a, requestID, id); err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}
func (s *Store) RemoveMember(ctx context.Context, a auth.Actor, id, requestID, user string) error {
	if !validID(user) || user == a.UserID {
		return ErrDenied
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	c, err := s.lock(ctx, tx, a, id, true)
	if err != nil {
		return err
	}
	if c.Role != "owner" {
		return ErrDenied
	}
	if hit, err := s.beginCommand(ctx, tx, a, requestID, "member.remove:"+id, user, &struct{}{}); err != nil {
		return err
	} else if hit {
		return nil
	}
	tag, err := tx.Exec(ctx, `DELETE FROM memberships WHERE camp_id=$1 AND user_id=$2 AND role!='owner'`, id, user)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrDenied
	}
	if _, err = tx.Exec(ctx, `DELETE FROM invitations WHERE camp_id=$1 AND email=(SELECT email FROM users WHERE id=$2) AND accepted_at IS NULL`, id, user); err != nil {
		return err
	}
	if err = s.invalidateMemberRequests(ctx, tx, a, id, user); err != nil {
		return err
	}
	if err = emit(ctx, tx, a, id, "member.removed", map[string]string{"user_id": user}); err != nil {
		return err
	}
	if err = s.finishCommand(ctx, tx, a, requestID, struct{}{}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) Events(ctx context.Context, a auth.Actor, id string, after int64) ([]Event, error) {
	if !validID(id) || after < 0 {
		return nil, ErrDenied
	}
	var current int64
	err := s.pool.QueryRow(ctx, `SELECT c.event_cursor FROM camps c JOIN memberships m ON m.camp_id=c.id WHERE c.id=$1 AND m.user_id=$2`, id, a.UserID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDenied
	}
	if err != nil {
		return nil, err
	}
	if after > current {
		return nil, ErrCursor
	}
	// Authorization and event reads share the same statement snapshot.
	rows, err := s.pool.Query(ctx, `SELECT e.cursor,e.kind,e.actor_id,e.payload FROM events e
	 JOIN memberships m ON m.camp_id=e.camp_id AND m.user_id=$1 WHERE e.camp_id=$2 AND e.cursor>$3
	 AND (e.recipient_user_id IS NULL OR e.recipient_user_id=$1)
	 AND (e.session_id IS NULL OR EXISTS(SELECT 1 FROM sessions s WHERE s.id=e.session_id AND (s.shared OR s.owner_user_id=$1))
	 OR EXISTS(SELECT 1 FROM event_recipients r WHERE r.camp_id=e.camp_id AND r.cursor=e.cursor AND r.user_id=$1))
	 AND (e.plan_id IS NULL OR EXISTS(SELECT 1 FROM planning_tickets p WHERE p.id=e.plan_id AND (p.shared OR p.creator_user_id=$1 OR EXISTS(SELECT 1 FROM planning_participants x WHERE x.plan_id=p.id AND x.version=p.active_version AND x.owner_user_id=$1)))) ORDER BY e.cursor LIMIT 250`, a.UserID, id, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err = rows.Scan(&e.Cursor, &e.Kind, &e.ActorID, &e.Payload); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	// Empty result must distinguish an up-to-date authorized client from revocation.
	var permitted bool
	err = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships WHERE camp_id=$1 AND user_id=$2)`, id, a.UserID).Scan(&permitted)
	if err != nil {
		return nil, err
	}
	if !permitted {
		return nil, ErrDenied
	}
	return out, nil
}

package localcli

import (
	"container/list"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/teslashibe/capybara-camp/server/internal/attachments"
)

// Until attachment bodies are part of Camp publication, refuse a text-only
// snapshot of a conversation containing attachments. Local viewing is supported.

func validSession(s Session) error {
	if (s.Provider != "Codex" && s.Provider != "Claude") || s.SessionID == "" || s.Root == "" {
		return errors.New("Choose a local Codex or Claude session")
	}
	return nil
}

func records(ctx context.Context, s Session, strict bool, visit func(Message) error) error {
	return recordsContent(ctx, s, strict, func(m Message, content any) error {
		var err error
		m.Attachments, err = attachments.Inspect(content)
		if err != nil {
			return err
		}
		if m.Text == "" && len(m.Attachments) == 0 {
			return nil
		}
		return visit(m)
	})
}

func recordsContent(ctx context.Context, s Session, strict bool, visit func(Message, any) error) error {
	if err := validSession(s); err != nil {
		return err
	}
	if s.Provider == "Codex" {
		history := filepath.Join(s.Root, "thread_history_1.sqlite")
		if isFile(history) {
			db, err := openReadDB(history)
			if err != nil {
				return err
			}
			defer db.Close()
			rows, err := db.QueryContext(ctx, "SELECT item_id,item_json FROM thread_items WHERE thread_id=? AND item_type IN ('userMessage','agentMessage') ORDER BY rollout_ordinal", s.SessionID)
			if err != nil {
				return err
			}
			defer rows.Close()
			found := false
			for rows.Next() {
				var id, raw string
				if err = rows.Scan(&id, &raw); err != nil {
					return err
				}
				if len(raw) > maxRecord {
					return errors.New("The Codex history contains a record larger than 32 MiB")
				}
				var item map[string]any
				if err = json.Unmarshal([]byte(raw), &item); err != nil {
					return errors.New("The Codex history contains an invalid record")
				}
				m := Message{ID: id, Role: "user", Text: textBlocks(item["content"])}
				if str(item["type"]) == "agentMessage" {
					m.Role = "assistant"
					m.Text = str(item["text"])
					m.Model = observedModel(item["model"])
					m.Effort = observedEffort(item["effort"])
					m.Speed = observedSpeed(item["service_tier"])
				}
				found = true
				if err = visit(m, item["content"]); err != nil {
					return err
				}
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if found {
				return nil
			}
		}
		if s.Path == "" {
			return nil
		}
		var settings settingsReader
		return readLines(ctx, s.Path, strict, func(n int, d map[string]any) error {
			settings.consume("Codex", d)
			p := object(d["payload"])
			role := str(p["role"])
			if str(d["type"]) != "response_item" || str(p["type"]) != "message" || (role != "user" && role != "assistant") {
				return nil
			}
			text := textBlocks(p["content"])
			id := str(p["id"])
			if id == "" {
				id = strconv.Itoa(n)
			}
			m := Message{ID: id, Role: role, Text: text}
			if role == "assistant" {
				m.Model, m.Effort, m.Speed = settings.current.Model, settings.current.Effort, settings.current.Speed
			}
			return visit(m, p["content"])
		})
	}
	if s.Path == "" {
		return errors.New("The Claude transcript path is missing")
	}
	return readLines(ctx, s.Path, strict, func(n int, d map[string]any) error {
		role := str(d["type"])
		if role != "user" && role != "assistant" || boolean(d["isSidechain"]) && !s.IsSubagent {
			return nil
		}
		message, ok := d["message"].(map[string]any)
		if !ok {
			if strict {
				return errors.New("The transcript contains an invalid message")
			}
			return nil
		}
		text := textBlocks(message["content"])
		id := str(d["uuid"])
		if id == "" {
			id = strconv.Itoa(n)
		}
		m := Message{ID: id, Role: role, Text: text}
		if role == "assistant" {
			settings := claudeMessageSettings(d)
			m.Model, m.Effort, m.Speed = settings.Model, settings.Effort, settings.Speed
		}
		return visit(m, message["content"])
	})
}

type Transcript struct {
	Messages []Message `json:"messages"`
	Total    int       `json:"total"`
}

func (a *App) Transcript(ctx context.Context, s Session, limit int) (Transcript, error) {
	result := Transcript{Messages: []Message{}}
	if limit < 1 || limit > 10000 {
		return result, errors.New("History page size must be between 1 and 10000")
	}
	// Retain only the visible tail's text. For Claude, IDs retain the original
	// insertion order while later records replace the same message in place.
	tail := list.New()
	seen := map[string]bool{}
	positions := map[string]*list.Element{}
	err := records(ctx, s, false, func(m Message) error {
		if s.Provider == "Claude" && seen[m.ID] {
			if element := positions[m.ID]; element != nil {
				element.Value = m
			}
			return nil
		}
		seen[m.ID] = true
		result.Total++
		positions[m.ID] = tail.PushBack(m)
		if tail.Len() > limit {
			first := tail.Front()
			delete(positions, first.Value.(Message).ID)
			tail.Remove(first)
		}
		return nil
	})
	for e := tail.Front(); e != nil; e = e.Next() {
		result.Messages = append(result.Messages, e.Value.(Message))
	}
	return result, err
}

func hash(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }

func fingerprint(s Session) (string, error) {
	path := s.Path
	if path == "" {
		path = "/nonexistent"
	}
	paths := []string{path}
	if s.Provider == "Codex" {
		for _, name := range []string{"thread_history_1.sqlite", "thread_history_1.sqlite-wal", "state_5.sqlite", "state_5.sqlite-wal"} {
			paths = append(paths, filepath.Join(s.Root, name))
		}
	}
	values := [][]any{}
	for _, path := range paths {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			values = append(values, []any{path, nil})
			continue
		}
		if err != nil {
			return "", err
		}
		var inode uint64
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			inode = stat.Ino
		}
		values = append(values, []any{path, inode, info.Size(), info.ModTime().UnixNano()})
	}
	encoded, err := json.Marshal(values)
	return hash(append([]byte("transcript-attachments-v1\n"), encoded...)), err
}

type ExportMessage struct {
	Attachments []attachments.Reference `json:"attachments,omitempty"`
	ID          string                  `json:"id"`
	Ordinal     int64                   `json:"ordinal"`
	Role        string                  `json:"role"`
	Text        string                  `json:"text"`
	Removed     bool                    `json:"removed"`
	Model       string                  `json:"model,omitempty"`
	Effort      string                  `json:"effort,omitempty"`
	Speed       string                  `json:"speed,omitempty"`
}
type ExportPage struct {
	Messages        []ExportMessage `json:"messages"`
	After           int64           `json:"after"`
	HasMore         bool            `json:"hasMore"`
	Fingerprint     string          `json:"fingerprint"`
	ContentRevision string          `json:"contentRevision"`
}

func (a *App) ExportPage(ctx context.Context, s Session, after int64, limit int) (ExportPage, error) {
	result := ExportPage{Messages: []ExportMessage{}, After: after}
	if err := validSession(s); err != nil {
		return result, err
	}
	if after < -1 || limit < 1 || limit > 200 {
		return result, errors.New("Invalid transcript page")
	}
	directory := filepath.Join(a.Config.Support, "transcript-index")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return result, err
	}
	key := hash([]byte(s.Provider + "\n" + s.Root + "\n" + s.SessionID + "\n" + s.Path))
	path := filepath.Join(directory, key+".sqlite")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return result, err
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return result, err
	}
	f.Close()
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return result, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS messages(id TEXT PRIMARY KEY,ordinal INTEGER NOT NULL UNIQUE,body TEXT NOT NULL,seen INTEGER NOT NULL,removed INTEGER NOT NULL);`); err != nil {
		return result, err
	}
	stamp, err := fingerprint(s)
	if err != nil {
		return result, err
	}
	result.Fingerprint = stamp
	var cached, contentRevision string
	err = db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='fingerprint'").Scan(&cached)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if err = db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='content_revision'").Scan(&contentRevision); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if cached != stamp || len(contentRevision) != 64 {
		if err := syncIndex(ctx, db, s, stamp); err != nil {
			return result, err
		}
	}
	// Read the revision and page from one SQLite snapshot. Another importer may
	// reconcile this index while a different page is being published.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='content_revision'").Scan(&result.ContentRevision); err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT body,removed FROM messages WHERE ordinal>? ORDER BY ordinal LIMIT ?", after, limit+1)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	size := 0
	for rows.Next() {
		var raw string
		var removed bool
		if err := rows.Scan(&raw, &removed); err != nil {
			return result, err
		}
		var m ExportMessage
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return result, errors.New("Invalid cached transcript message")
		}
		if removed {
			m.Removed = true
			m.Text = ""
			m.Attachments = nil
		}
		encoded, _ := json.Marshal(m)
		if len(encoded) > 1<<20 {
			return result, errors.New("This transcript contains a message larger than the 1 MiB sharing limit")
		}
		if len(result.Messages) >= limit || size+len(encoded) > 1<<20 {
			result.HasMore = true
			break
		}
		size += len(encoded)
		result.Messages = append(result.Messages, m)
		result.After = m.Ordinal
	}
	return result, rows.Err()
}

func syncIndex(ctx context.Context, db *sql.DB, s Session, stamp string) error {
	// One writer owns the reconciliation until commit. A partial provider file
	// rolls back both amendments and tombstones, preserving the prior snapshot.
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if _, err = conn.ExecContext(ctx, "UPDATE messages SET seen=0"); err != nil {
		return err
	}
	var next int64
	if err = conn.QueryRowContext(ctx, "SELECT coalesce(max(ordinal)+1,0) FROM messages").Scan(&next); err != nil {
		return err
	}
	err = records(ctx, s, true, func(m Message) error {
		if len(m.ID) > 256 {
			m.ID = "m:" + hash([]byte(m.ID))
		}
		var position int64
		err := conn.QueryRowContext(ctx, "SELECT ordinal FROM messages WHERE id=?", m.ID).Scan(&position)
		if errors.Is(err, sql.ErrNoRows) {
			position = next
			next++
		} else if err != nil {
			return err
		}
		body, err := json.Marshal(ExportMessage{ID: m.ID, Ordinal: position, Role: m.Role, Text: m.Text, Model: m.Model, Effort: m.Effort, Speed: m.Speed, Attachments: m.Attachments})
		if err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO messages VALUES(?,?,?,1,0) ON CONFLICT(id) DO UPDATE SET body=excluded.body,seen=1,removed=0`, m.ID, position, string(body))
		return err
	})
	if err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "UPDATE messages SET removed=1 WHERE seen=0"); err != nil {
		return err
	}
	contentRevision, err := indexedContentRevision(ctx, conn)
	if err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "INSERT INTO metadata VALUES('content_revision',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", contentRevision); err != nil {
		return err
	}
	current, err := fingerprint(s)
	if err != nil {
		return err
	}
	if current != stamp {
		stamp = ""
	}
	if _, err = conn.ExecContext(ctx, "INSERT INTO metadata VALUES('fingerprint',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", stamp); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("Commit transcript index: %w", err)
	}
	committed = true
	return nil
}

func indexedContentRevision(ctx context.Context, conn *sql.Conn) (string, error) {
	rows, err := conn.QueryContext(ctx, "SELECT substr(body,1,1049600),removed FROM messages ORDER BY ordinal")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	digest := sha256.New()
	_, _ = digest.Write([]byte("capybara-transcript-content-v1\n"))
	encoder := json.NewEncoder(digest)
	for count := 0; rows.Next(); count++ {
		if count >= 200000 {
			return "", errors.New("Transcript exceeds 200,000 messages")
		}
		var raw string
		var removed bool
		if err = rows.Scan(&raw, &removed); err != nil {
			return "", err
		}
		if len(raw) >= 1049600 {
			return "", errors.New("This transcript contains a message larger than the 1 MiB sharing limit")
		}
		var message ExportMessage
		if err = json.Unmarshal([]byte(raw), &message); err != nil {
			return "", errors.New("Invalid cached transcript message")
		}
		if removed {
			message.Removed = true
			message.Text = ""
			message.Attachments = nil
		}
		data, err := json.Marshal(message)
		if err != nil || len(data) > 1<<20 {
			return "", errors.New("This transcript contains a message larger than the 1 MiB sharing limit")
		}
		if err = encoder.Encode(message); err != nil {
			return "", err
		}
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/cortex"
)

// Snapshot is a read-only projection of Cortex's current memory. The digest
// binds selection to the content and provenance that a member reviewed.
type Snapshot struct {
	Memory cortex.Memory `json:"memory"`
	Digest string        `json:"digest"`
}

var ErrSnapshotLimit = errors.New("selected memory snapshots exceed 8 MiB; select fewer memories")

func (s *Store) Memories(ctx context.Context, tx pgx.Tx, space string, ids []string, after string, limit int) ([]Snapshot, error) {
	if limit < 1 || limit > 500 || len(ids) > 500 {
		return nil, errors.New("invalid memory read limit")
	}
	rows, err := tx.Query(ctx, `SELECT id,user_id,kind,summary,confidence,reinforced_count,born_at,last_seen_at,COALESCE(contradicted_by,''),expires_at,sources,archived_at,pinned,utility,outcome_count FROM `+pgx.Identifier{s.schema, "memories"}.Sanitize()+` WHERE user_id=$1 AND contradicted_by IS NULL AND archived_at IS NULL AND (expires_at IS NULL OR expires_at>now()) AND ($2::text[] IS NULL OR id=ANY($2)) AND id>$3 ORDER BY id LIMIT $4`, space, ids, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Snapshot{}
	bytes := 0
	for rows.Next() {
		var value Snapshot
		m := &value.Memory
		if err = rows.Scan(&m.ID, &m.UserID, &m.Kind, &m.Summary, &m.Confidence, &m.ReinforcedCount, &m.BornAt, &m.LastSeenAt, &m.ContradictedBy, &m.ExpiresAt, &m.Sources, &m.ArchivedAt, &m.Pinned, &m.Utility, &m.OutcomeCount); err != nil {
			return nil, err
		}
		data, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		bytes += len(data)
		if bytes > 8<<20 {
			return nil, ErrSnapshotLimit
		}
		digest := sha256.Sum256(data)
		value.Digest = hex.EncodeToString(digest[:])
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) Pending(ctx context.Context, tx pgx.Tx, space string, ids []string) (bool, error) {
	var count int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{s.schema, "observations"}.Sanitize()+` WHERE user_id=$1 AND id=ANY($2) AND consolidated_at IS NULL`, space, ids).Scan(&count)
	return count == len(ids), err
}

package camp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5"
)

type TransferAttachmentGroup struct {
	Kind  string `json:"kind"`
	State string `json:"state"`
	Count int    `json:"count"`
}
type TransferAttachmentReview struct {
	Version     int                       `json:"version"`
	SHA256      string                    `json:"sha256"`
	Files       int                       `json:"files"`
	Bytes       int64                     `json:"bytes"`
	Unavailable []TransferAttachmentGroup `json:"unavailable"`
}

// The digest covers every descriptor and its original message/conversation at
// the pinned revision. Counts are a compact review; they are not the identity.
func transferAttachmentReview(ctx context.Context, tx pgx.Tx, parent string, revision int64, checkBodies bool) (*TransferAttachmentReview, error) {
	rows, err := tx.Query(ctx, `WITH histories AS (
 SELECT $1::uuid AS id,$2::bigint AS revision
 UNION ALL
 SELECT c.session_id,v.transcript_revision FROM session_children c
 CROSS JOIN LATERAL(SELECT transcript_revision,complete,removed FROM session_child_revisions
 WHERE child_session_id=c.session_id AND parent_session_id=$1 AND revision<=$2 ORDER BY revision DESC LIMIT 1)v
 WHERE c.parent_session_id=$1 AND v.complete AND NOT v.removed
 ) SELECT h.id,m.local_id,v.attachments FROM histories h JOIN messages m ON m.session_id=h.id
 CROSS JOIN LATERAL(SELECT attachments,removed FROM message_revisions WHERE session_id=h.id AND local_id=m.local_id AND revision<=h.revision ORDER BY revision DESC LIMIT 1)v
 WHERE NOT v.removed AND v.attachments<>'[]'::jsonb ORDER BY h.id,m.ordinal,m.local_id`, parent, revision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &TransferAttachmentReview{Version: 1, Unavailable: []TransferAttachmentGroup{}}
	digest := sha256.New()
	digest.Write([]byte("capybara-transfer-attachments-v1\n" + parent + "\n" + strconv.FormatInt(revision, 10) + "\n"))
	type blobKey struct{ session, hash string }
	needed := map[blobKey]int{}
	groups := map[[2]string]int{}
	size, count := 0, 0
	for rows.Next() {
		var session, message string
		var raw []byte
		if err = rows.Scan(&session, &message, &raw); err != nil {
			return nil, err
		}
		size += len(raw)
		if size > 32<<20 {
			return nil, ErrInput
		}
		var refs []AttachmentReference
		if json.Unmarshal(raw, &refs) != nil || len(refs) > 64 {
			return nil, ErrInput
		}
		for _, ref := range refs {
			if !ref.valid() {
				return nil, ErrInput
			}
			count++
			encoded, e := json.Marshal(struct {
				Session   string              `json:"session"`
				Message   string              `json:"message"`
				Reference AttachmentReference `json:"reference"`
			}{session, message, ref})
			if e != nil {
				return nil, e
			}
			digest.Write(encoded)
			digest.Write([]byte{'\n'})
			if ref.State == "embedded" {
				key := blobKey{session, ref.SHA256}
				if previous, ok := needed[key]; ok {
					if previous != ref.Size {
						return nil, ErrConflict
					}
				} else {
					needed[key] = ref.Size
					out.Bytes += int64(ref.Size)
				}
			} else {
				groups[[2]string{ref.Kind, ref.State}]++
			}
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if count == 0 {
		return nil, nil
	}
	if len(needed) > 1024 || out.Bytes > 128<<20 {
		return nil, ErrInput
	}
	out.Files = len(needed)
	// Publication already binds encrypted bytes to the reference. Refuse a
	// transfer if retained storage no longer contains that exact body and size.
	if checkBodies && len(needed) > 0 {
		rows, err = tx.Query(ctx, `SELECT session_id,sha256,byte_count FROM transcript_attachment_blobs WHERE parent_session_id=$1`, parent)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key blobKey
			var bytes int
			if err = rows.Scan(&key.session, &key.hash, &bytes); err != nil {
				rows.Close()
				return nil, err
			}
			if size, ok := needed[key]; ok {
				if size != bytes {
					rows.Close()
					return nil, ErrConflict
				}
				delete(needed, key)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if len(needed) > 0 {
			return nil, ErrConflict
		}
	}
	for key, n := range groups {
		out.Unavailable = append(out.Unavailable, TransferAttachmentGroup{Kind: key[0], State: key[1], Count: n})
	}
	sort.Slice(out.Unavailable, func(i, j int) bool {
		a, b := out.Unavailable[i], out.Unavailable[j]
		return a.Kind < b.Kind || a.Kind == b.Kind && a.State < b.State
	})
	out.SHA256 = hex.EncodeToString(digest.Sum(nil))
	return out, nil
}

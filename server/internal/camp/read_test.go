package camp

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestSnapshotRetryAfterConcurrentCampCommit(t *testing.T) {
	s, pool, actors, c, _, _ := sessionFixture(t)
	ctx := context.Background()
	calls := 0
	_, err := retryRead(ctx, func() (Camp, error) {
		calls++
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
		if err != nil {
			return Camp{}, err
		}
		defer tx.Rollback(ctx)
		var cursor int64
		if err = tx.QueryRow(ctx, `SELECT event_cursor FROM camps WHERE id=$1`, c.ID).Scan(&cursor); err != nil {
			return Camp{}, err
		}
		if calls == 1 {
			if _, err = pool.Exec(ctx, `UPDATE camps SET event_cursor=event_cursor+1 WHERE id=$1`, c.ID); err != nil {
				return Camp{}, err
			}
		}
		value, err := s.lock(ctx, tx, actors[0], c.ID, false)
		if err != nil {
			return Camp{}, err
		}
		return value, tx.Commit(ctx)
	})
	if err != nil || calls != 2 {
		t.Fatal("concurrent read recovery", calls, err)
	}
}
func TestReadRetriesAreBoundedAndRespectCancellation(t *testing.T) {
	calls := 0
	_, err := retryRead(context.Background(), func() (int, error) { calls++; return 0, &pgconn.PgError{Code: "40001"} })
	if err == nil || calls != 4 {
		t.Fatal("unbounded retries", calls, err)
	}
	calls = 0
	_, err = retryRead(context.Background(), func() (int, error) { calls++; return 0, ErrDenied })
	if !errors.Is(err, ErrDenied) || calls != 1 {
		t.Fatal("retried access denial", calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls = 0
	_, err = retryRead(ctx, func() (int, error) { calls++; cancel(); return 0, &pgconn.PgError{Code: "40001"} })
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal("ignored cancellation", calls, err)
	}
}

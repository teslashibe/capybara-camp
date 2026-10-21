package camp

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// A repeatable-read snapshot can lose a race with a committed Camp event while
// acquiring the Camp row lock. Only complete, read-only database operations use
// this retry; provider work and mutation commands never pass through it.
func retryRead[T any](ctx context.Context, read func() (T, error)) (T, error) {
	var zero T
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		value, err := read()
		var databaseError *pgconn.PgError
		if err == nil || attempt == 3 || !errors.As(err, &databaseError) || (databaseError.Code != "40001" && databaseError.Code != "40P01") {
			return value, err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		case <-timer.C:
		}
	}
}

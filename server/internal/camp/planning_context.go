package camp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

func planningQuotes(v PlanningVersion) []ImportedPlanningSource {
	if v.Imported != nil {
		return v.Imported.LocalSources
	}
	return v.QuotedSources
}

// A restart selects immutable quoted sources from one readable saved version.
// Missing selection is different from explicitly removing every source.
func (s *Store) retainPlanningQuotes(ctx context.Context, tx pgx.Tx, previous PlanningTicket, in PlanningInput, next *PlanningVersion) error {
	if in.QuotedSourceVersion < 0 {
		return ErrInput
	}
	if in.QuotedSourceIDs != nil && len(in.QuotedSourceIDs) == 0 {
		return validatePlanningContext(ctx, tx, *next)
	}
	version := in.QuotedSourceVersion
	if version == 0 {
		version = previous.Version.Version
	}
	origin := previous
	if version != previous.Version.Version {
		var err error
		origin, err = s.loadPlanning(ctx, tx, auth.Actor{UserID: previous.CreatorUserID}, previous.CampID, previous.ID, version)
		if err != nil {
			return err
		}
		if err = s.checkPlanningSources(ctx, tx, auth.Actor{UserID: previous.CreatorUserID}, origin); err != nil {
			return err
		}
	}
	quotes := planningQuotes(origin.Version)
	if len(quotes) > 0 && in.QuotedSourceIDs == nil {
		return ErrInput
	}
	if len(in.QuotedSourceIDs) > 20 {
		return ErrInput
	}
	seen := map[string]bool{}
	for _, id := range in.QuotedSourceIDs {
		if seen[id] {
			return ErrInput
		}
		seen[id] = true
		found := false
		for _, quote := range quotes {
			if quote.ID == id {
				next.QuotedSources = append(next.QuotedSources, quote)
				found = true
				break
			}
		}
		if !found {
			return ErrInput
		}
	}
	return validatePlanningContext(ctx, tx, *next)
}

func validatePlanningContext(ctx context.Context, tx pgx.Tx, v PlanningVersion) error {
	if len(v.Sources)+len(v.QuotedSources) > 20 {
		return ErrInput
	}
	bytes := 0
	for _, source := range v.QuotedSources {
		for _, m := range source.Messages {
			bytes += len(m.Text)
		}
	}
	for _, source := range v.Sources {
		messages, err := readContextMessages(ctx, tx, source)
		if err != nil {
			return err
		}
		for _, m := range messages {
			bytes += len(m.Text)
		}
	}
	if bytes > 256<<10 {
		return ErrInput
	}
	return nil
}

func quotedContextDigest(quotes []ImportedPlanningSource) string {
	if len(quotes) == 0 {
		return ""
	}
	raw, _ := json.Marshal(quotes)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

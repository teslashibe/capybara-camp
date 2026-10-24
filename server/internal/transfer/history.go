package transfer

import (
	"encoding/json"
	"sort"
)

// HistoryBinding identifies the local snapshots copied to one cloud revision.
// It contains no transcript text, provider credentials or filesystem paths.
type HistoryBinding struct {
	Version            int            `json:"version"`
	ParentID           string         `json:"parent_id"`
	MainRevision       string         `json:"main_revision"`
	Children           []HistoryChild `json:"children"`
	TranscriptRevision int64          `json:"transcript_revision"`
}
type HistoryChild struct {
	SourceID string `json:"source_id"`
	Revision string `json:"revision"`
}

func (h HistoryBinding) Validate() error {
	if h.Version != 1 || !digestPattern.MatchString(h.ParentID) || !digestPattern.MatchString(h.MainRevision) || h.TranscriptRevision < 0 || len(h.Children) > 512 {
		return ErrInput
	}
	seen := map[string]bool{}
	for _, c := range h.Children {
		if !digestPattern.MatchString(c.SourceID) || !digestPattern.MatchString(c.Revision) || seen[c.SourceID] {
			return ErrInput
		}
		seen[c.SourceID] = true
	}
	return nil
}
func (h HistoryBinding) Digest() (string, error) {
	if err := h.Validate(); err != nil {
		return "", err
	}
	h.Children = append([]HistoryChild{}, h.Children...)
	sort.Slice(h.Children, func(i, j int) bool { return h.Children[i].SourceID < h.Children[j].SourceID })
	raw, err := json.Marshal(h)
	if err != nil {
		return "", err
	}
	return hash(append([]byte("capybara-history-binding-v1\n"), raw...)), nil
}

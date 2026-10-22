package localcli

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

var errHistoryChanged = errors.New("Session history changed; finish syncing and review the move again")

func (a *App) currentHistoryBinding(ctx context.Context, parent Session, revision int64) (transfer.HistoryBinding, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	parent, err := childHistoryParent(parent)
	if err != nil {
		return transfer.HistoryBinding{}, err
	}
	page, err := a.ExportPage(ctx, parent, -1, 1)
	if err != nil {
		return transfer.HistoryBinding{}, err
	}
	out := transfer.HistoryBinding{Version: 1, ParentID: childHistoryKey(parent, ""), MainRevision: page.ContentRevision, Children: []transfer.HistoryChild{}, TranscriptRevision: revision}
	children, err := a.ChildHistory(ctx, parent)
	if err != nil {
		return out, errHistoryChanged
	}
	for _, source := range children.Sources {
		out.Children = append(out.Children, transfer.HistoryChild{SourceID: source.ID, Revision: source.Revision})
	}
	current, err := a.ChildHistory(ctx, parent)
	if err != nil || current.Revision != children.Revision {
		return out, errHistoryChanged
	}
	final, err := a.ExportPage(ctx, parent, -1, 1)
	if err != nil || final.ContentRevision != out.MainRevision {
		return out, errHistoryChanged
	}
	sort.Slice(out.Children, func(i, j int) bool { return out.Children[i].SourceID < out.Children[j].SourceID })
	return out, out.Validate()
}
func (a *App) verifyHistoryBinding(ctx context.Context, session Session, expected *transfer.HistoryBinding) error {
	if expected == nil {
		return errHistoryChanged
	}
	digest, err := expected.Digest()
	if err != nil {
		return errHistoryChanged
	}
	current, err := a.currentHistoryBinding(ctx, session, expected.TranscriptRevision)
	if err != nil {
		return err
	}
	actual, err := current.Digest()
	if err != nil || actual != digest {
		return errHistoryChanged
	}
	return nil
}

package localcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"unicode/utf8"

	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

// RestoredHistoryReference addresses an installed archive, not a provider
// conversation. It grants no execution ownership and never implies NewSession.
// The native caller must separately authorize the committed local assignment.
type RestoredHistoryReference struct {
	Scope         string `json:"scope"`
	TransferID    string `json:"transferID"`
	ReceiptSHA256 string `json:"receiptSHA256"`
}

func (a *App) openRestoredHistory(ref RestoredHistoryReference) (*os.Root, TransferInstallReceipt, error) {
	var receipt TransferInstallReceipt
	if !installDigest(ref.Scope) || !installUUID(ref.TransferID) || !installDigest(ref.ReceiptSHA256) || !filepath.IsAbs(a.Config.Support) {
		return nil, receipt, transfer.ErrInput
	}
	info, err := os.Lstat(a.Config.Support)
	if err != nil || !info.IsDir() || !ownedByUser(info) || info.Mode().Perm()&0077 != 0 {
		return nil, receipt, transfer.ErrDestination
	}
	support, err := os.OpenRoot(a.Config.Support)
	if err != nil {
		return nil, receipt, err
	}
	defer support.Close()
	name := "restore-" + ref.Scope + "-" + ref.TransferID
	info, err = support.Lstat(name)
	if err != nil || !info.IsDir() || !ownedByUser(info) || info.Mode().Perm()&0077 != 0 {
		return nil, receipt, transfer.ErrDestination
	}
	root, err := support.OpenRoot(name)
	if err != nil {
		return nil, receipt, err
	}
	fail := func() (*os.Root, TransferInstallReceipt, error) {
		root.Close()
		return nil, receipt, transfer.ErrChanged
	}
	raw, err := readInstallFile(root, "installation.json", 32768)
	if err != nil || installHash(raw) != ref.ReceiptSHA256 || json.Unmarshal(raw, &receipt) != nil || receipt.Version != 1 || receipt.Scope != ref.Scope || receipt.TransferID != ref.TransferID {
		return fail()
	}
	intent, err := readInstallFile(root, "intent.json", 32768)
	if err != nil || !bytes.Equal(intent, raw) {
		return fail()
	}
	return root, receipt, nil
}

// Load one bounded, immutable snapshot per conversation. Reopening its receipt
// on each tool read detects erasure/replacement without rehashing up to 32 MiB
// for every page. Content returned is always from the initially verified bytes.
func (a *App) restoredHistory(ctx context.Context, session Session, ref RestoredHistoryReference) ([]HistorySource, HistoryReader, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	root, receipt, err := a.openRestoredHistory(ref)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	if receipt.Target.Provider != session.Provider || receipt.Target.Root != session.Root || receipt.Target.SourceID != session.SourceID || receipt.Destination != session.CWD {
		return nil, nil, transfer.ErrDestination
	}
	if err = a.validateInstallTarget(receipt.Target, receipt.Destination); err != nil {
		return nil, nil, err
	}
	raw, err := readInstallFile(root, "history.json", maxInstalledHistory)
	if err != nil {
		return nil, nil, err
	}
	h, err := validateInstalledHistory(TransferInstallInput{TransferStageInput: TransferStageInput{Scope: ref.Scope}, Target: receipt.Target, History: raw, HistorySHA256: receipt.HistorySHA256})
	if err != nil || h.Session.ID != receipt.SourceID || h.Session.TranscriptRevision != receipt.TranscriptRevision {
		return nil, nil, transfer.ErrChanged
	}
	originalInfo, err := root.Lstat("history.json")
	if err != nil {
		return nil, nil, err
	}
	parent := "camp:" + h.Session.ID
	revision := strconv.FormatInt(h.Session.TranscriptRevision, 10)
	sources := make([]HistorySource, 0, len(h.Conversations))
	indices := map[string]int{}
	for index, c := range h.Conversations {
		source := HistorySource{ID: parent, Title: h.Session.Title, Revision: revision}
		if index != 0 {
			source.ID += "/child:" + c.ID
			source.ParentID = parent
			source.Title = c.Child.Name
		}
		if source.Title == "" {
			source.Title = "Restored conversation"
		}
		sources = append(sources, source)
		indices[source.ID] = index
	}
	if err = validateHistoryCatalog(nil, sources); err != nil {
		return nil, nil, err
	}
	reader := func(readCtx context.Context, in HistoryRead) (HistoryPage, error) {
		var page HistoryPage
		if err := ctx.Err(); err != nil {
			return page, err
		}
		if err := readCtx.Err(); err != nil {
			return page, err
		}
		index, exists := indices[in.SessionID]
		if !exists || in.Offset < 0 || in.TextOffset < 0 || in.TextOffset > 8<<20 || in.Limit < 1 || in.Limit > 120 {
			return page, transfer.ErrInput
		}
		current, _, err := a.openRestoredHistory(ref)
		if err != nil {
			return page, err
		}
		info, err := current.Lstat("history.json")
		current.Close()
		if err != nil || !os.SameFile(originalInfo, info) || info.Size() != originalInfo.Size() || !info.ModTime().Equal(originalInfo.ModTime()) || info.Mode() != originalInfo.Mode() {
			return page, transfer.ErrChanged
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Nlink != 1 || int(stat.Uid) != os.Geteuid() {
			return page, transfer.ErrDestination
		}
		source, messages := sources[index], h.Conversations[index].Messages
		page = HistoryPage{SessionID: source.ID, Title: source.Title, Revision: source.Revision, Messages: []HistoryMessage{}}
		start := sort.Search(len(messages), func(i int) bool { return messages[i].Ordinal >= in.Offset })
		if in.TextOffset != 0 && (start == len(messages) || messages[start].Ordinal != in.Offset) {
			return page, transfer.ErrInput
		}
		total := 0
		for _, m := range messages[start:] {
			offset := 0
			if m.Ordinal == in.Offset {
				offset = in.TextOffset
			}
			if offset > len(m.Text) || (offset < len(m.Text) && !utf8.RuneStart(m.Text[offset])) {
				return HistoryPage{}, transfer.ErrInput
			}
			end := min(len(m.Text), offset+16384)
			for end < len(m.Text) && !utf8.RuneStart(m.Text[end]) {
				end--
			}
			if len(page.Messages) == min(in.Limit, 20) || total+end-offset > 48<<10 {
				next := m.Ordinal
				page.NextOffset = &next
				break
			}
			complete := end == len(m.Text)
			page.Messages = append(page.Messages, HistoryMessage{ID: m.ID, Ordinal: m.Ordinal, Role: m.Role, Text: m.Text[offset:end], TextOffset: offset, Complete: complete})
			total += end - offset
			if !complete {
				next := m.Ordinal
				page.NextOffset, page.NextTextOffset = &next, end
				break
			}
		}
		if err = validateHistoryPage(source, in, page); err != nil {
			return HistoryPage{}, err
		}
		return page, nil
	}
	// The caller and the reader must not share mutable catalog storage.
	return append([]HistorySource(nil), sources...), reader, nil
}

func (a *App) attachRestoredHistory(ctx context.Context, request *conversationRequest) error {
	if request.RestoredHistory == nil {
		return nil
	}
	if a.isolated || request.Mode == "inference" || len(request.History) != 0 || request.ReadHistory != nil {
		return errors.New("Restored history requires its selected local profile and checkout")
	}
	history, reader, err := a.restoredHistory(ctx, request.Session, *request.RestoredHistory)
	if err != nil {
		return err
	}
	request.History, request.ReadHistory = history, reader
	return nil
}

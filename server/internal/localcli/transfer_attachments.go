package localcli

import (
	"context"
	"os"
	"path/filepath"

	"github.com/teslashibe/capybara-camp/server/internal/attachments"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

// Archive references retain the Camp wire format. Local provider references use
// camelCase, so conversion is explicit at this boundary.
type installedAttachmentReference struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Source       string `json:"source"`
	SourceDigest string `json:"source_digest"`
	State        string `json:"state"`
	MediaType    string `json:"media_type,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	Size         int    `json:"size,omitempty"`
}

func (r installedAttachmentReference) local() attachments.Reference {
	return attachments.Reference{ID: r.ID, Kind: r.Kind, Source: r.Source, SourceDigest: r.SourceDigest, State: r.State, MediaType: r.MediaType, SHA256: r.SHA256, Size: r.Size}
}

type installedAttachment struct {
	StreamID   string                       `json:"stream_id"`
	MessageID  string                       `json:"message_id"`
	Attachment installedAttachmentReference `json:"attachment"`
}

func (a installedAttachment) filename() string {
	return "attachment-" + a.StreamID + "-" + a.Attachment.SHA256
}

func validateInstalledAttachments(h installedHistory) error {
	if len(h.Attachments) > 1024 || (h.Version == 1 && len(h.Attachments) != 0) {
		return transfer.ErrInput
	}
	type selected struct{ stream, message, id string }
	references := map[selected]installedAttachmentReference{}
	expected := map[string]int{}
	total := 0
	for _, c := range h.Conversations {
		for _, m := range c.Messages {
			if len(m.Attachments) > attachments.MaxPerMessage || (h.Version == 1 && len(m.Attachments) != 0) {
				return transfer.ErrInput
			}
			ids := map[string]bool{}
			for _, r := range m.Attachments {
				if !r.local().Valid() || ids[r.ID] {
					return transfer.ErrInput
				}
				ids[r.ID] = true
				if r.State != "embedded" {
					continue
				}
				references[selected{c.ID, m.ID, r.ID}] = r
				name := (installedAttachment{StreamID: c.ID, Attachment: r}).filename()
				if size, ok := expected[name]; ok {
					if size != r.Size {
						return transfer.ErrInput
					}
				} else {
					expected[name] = r.Size
					total += r.Size
				}
			}
		}
	}
	if total > 128<<20 || len(expected) > 1024 {
		return transfer.ErrLimit
	}
	if len(expected) != len(h.Attachments) {
		return transfer.ErrInput
	}
	seen := map[string]bool{}
	for _, a := range h.Attachments {
		r, ok := references[selected{a.StreamID, a.MessageID, a.Attachment.ID}]
		if !ok || r != a.Attachment || seen[a.filename()] {
			return transfer.ErrInput
		}
		seen[a.filename()] = true
	}
	return nil
}

// The native app stages verified bodies in a private temporary directory. Only
// manifest-derived filenames are read; transcript paths never enter this API.
// Each body is copied separately so the 128 MiB budget is not held in memory.
func installAttachmentFiles(ctx context.Context, destination *os.Root, h installedHistory, directory string) error {
	if len(h.Attachments) == 0 {
		if directory != "" {
			return transfer.ErrInput
		}
		return nil
	}
	var source *os.Root
	if directory != "" {
		if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
			return transfer.ErrInput
		}
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || !ownedByUser(info) || info.Mode().Perm()&0077 != 0 {
			return transfer.ErrDestination
		}
		source, err = os.OpenRoot(directory)
		if err != nil {
			return err
		}
		defer source.Close()
	}
	for _, a := range h.Attachments {
		if err := ctx.Err(); err != nil {
			return err
		}
		// A retry may use already installed bytes after its source temporary was
		// removed. Existing corrupt data is never silently replaced.
		data, err := readInstallFile(destination, a.filename(), attachments.MaxBytes)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if os.IsNotExist(err) {
			if source == nil {
				return transfer.ErrChanged
			}
			data, err = readInstallFile(source, a.filename(), attachments.MaxBytes)
			if err != nil {
				return err
			}
		}
		if len(data) != a.Attachment.Size || installHash(data) != a.Attachment.SHA256 {
			return transfer.ErrChanged
		}
		if err = bindInstallFile(destination, a.filename(), data, attachments.MaxBytes); err != nil {
			return err
		}
	}
	return ctx.Err()
}

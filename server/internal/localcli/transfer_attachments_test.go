package localcli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func attachmentInstallFixture(t *testing.T, provider string) (*App, TransferInstallInput, installedHistory, []byte) {
	t.Helper()
	a, in := installFixture(t, provider)
	var h installedHistory
	if err := json.Unmarshal(in.History, &h); err != nil {
		t.Fatal(err)
	}
	h.Version = 2
	body := []byte("A preserved attachment\n")
	ref := installedAttachmentReference{ID: "a:0", Kind: "document", Source: "inline", SourceDigest: strings.Repeat("c", 64), State: "embedded", MediaType: "text/plain", SHA256: installHash(body), Size: len(body)}
	unavailable := installedAttachmentReference{ID: "a:1", Kind: "image", Source: "remote", SourceDigest: strings.Repeat("d", 64), State: "reference"}
	for i := range h.Conversations {
		c := &h.Conversations[i]
		c.Messages[0].Attachments = []installedAttachmentReference{ref, unavailable}
		h.Attachments = append(h.Attachments, installedAttachment{StreamID: c.ID, MessageID: c.Messages[0].ID, Attachment: ref})
	}
	in.AttachmentDirectory = t.TempDir()
	if err := os.Chmod(in.AttachmentDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	for _, item := range h.Attachments {
		if err := os.WriteFile(filepath.Join(in.AttachmentDirectory, item.filename()), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	setAttachmentHistory(t, &in, h)
	return a, in, h, body
}
func setAttachmentHistory(t *testing.T, in *TransferInstallInput, h installedHistory) {
	t.Helper()
	var err error
	in.History, err = json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	in.HistorySHA256 = installHash(in.History)
}

func TestTransferAttachmentInstallation(t *testing.T) {
	for _, provider := range []string{"Codex", "Claude"} {
		t.Run(provider, func(t *testing.T) {
			a, in, h, body := attachmentInstallFixture(t, provider)
			result, err := installCall(t.Context(), a, in)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range h.Attachments {
				path := filepath.Join(installDirectory(a, in), item.filename())
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, body) {
					t.Fatal("Attachment body changed", err)
				}
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatal("Attachment permissions", err)
				}
			}
			if err := os.RemoveAll(in.AttachmentDirectory); err != nil {
				t.Fatal(err)
			}
			in.AttachmentDirectory = ""
			again, err := installCall(t.Context(), New(a.Config), in)
			if err != nil || result != again {
				t.Fatal("Retry changed receipt", err)
			}
			path := filepath.Join(installDirectory(a, in), h.Attachments[0].filename())
			if err := os.WriteFile(path, bytes.Repeat([]byte{'X'}, len(body)), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := installCall(t.Context(), a, in); err == nil {
				t.Fatal("Corrupt installed bytes accepted")
			}
		})
	}
}
func TestTransferAttachmentRefusesIncompleteOrUnsafeInput(t *testing.T) {
	for _, mode := range []string{"missing", "corrupt", "symlink", "hardlink", "public-directory", "public-file", "wrong-message", "wrong-stream", "wrong-source", "duplicate-manifest", "missing-manifest", "v1"} {
		t.Run(mode, func(t *testing.T) {
			a, in, h, body := attachmentInstallFixture(t, "Codex")
			path := filepath.Join(in.AttachmentDirectory, h.Attachments[0].filename())
			switch mode {
			case "missing":
				os.Remove(path)
			case "corrupt":
				os.WriteFile(path, bytes.Repeat([]byte{'X'}, len(body)), 0600)
			case "symlink", "hardlink":
				os.Remove(path)
				other := filepath.Join(t.TempDir(), "body")
				os.WriteFile(other, body, 0600)
				var err error
				if mode == "symlink" {
					err = os.Symlink(other, path)
				} else {
					err = os.Link(other, path)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "public-directory":
				os.Chmod(in.AttachmentDirectory, 0755)
			case "public-file":
				os.Chmod(path, 0644)
			case "wrong-message":
				h.Attachments[0].MessageID = "other"
			case "wrong-stream":
				h.Attachments[0].StreamID = h.Attachments[1].StreamID
			case "wrong-source":
				h.Attachments[0].Attachment.SourceDigest = strings.Repeat("e", 64)
			case "duplicate-manifest":
				h.Attachments[1] = h.Attachments[0]
			case "missing-manifest":
				h.Attachments = h.Attachments[:1]
			case "v1":
				h.Version = 1
			}
			setAttachmentHistory(t, &in, h)
			if _, err := installCall(t.Context(), a, in); err == nil {
				t.Fatal("Unsafe installation accepted")
			}
			if _, err := os.Stat(filepath.Join(installDirectory(a, in), "installation.json")); !os.IsNotExist(err) {
				t.Fatal("Failure issued receipt", err)
			}
		})
	}
}
func TestTransferAttachmentPartialInstallRecovery(t *testing.T) {
	a, in, h, body := attachmentInstallFixture(t, "Claude")
	path := filepath.Join(in.AttachmentDirectory, h.Attachments[1].filename())
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := installCall(t.Context(), a, in); err == nil {
		t.Fatal("Missing child accepted")
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := installCall(t.Context(), a, in); err != nil {
		t.Fatal("Partial installation did not recover", err)
	}
}

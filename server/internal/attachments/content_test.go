package attachments

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestProviderContentAndBoundRead(t *testing.T) {
	body := []byte("attachment fixture")
	encoded := base64.StdEncoding.EncodeToString(body)
	for _, test := range []struct {
		name  string
		block map[string]any
		media string
	}{
		{"claude-image", map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": encoded}}, "image/png"},
		{"claude-pdf", map[string]any{"type": "document", "source": map[string]any{"type": "base64", "media_type": "application/pdf", "data": encoded}}, "application/pdf"},
		{"claude-text", map[string]any{"type": "document", "source": map[string]any{"type": "text", "data": string(body)}}, "text/plain"},
		{"codex-rollout", map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + encoded}, "image/png"},
		{"codex-core", map[string]any{"type": "image", "image_url": "data:image/jpeg;base64," + encoded}, "image/jpeg"},
		{"codex-app", map[string]any{"type": "image", "url": "data:image/webp;base64," + encoded}, "image/webp"},
		{"codex-object", map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/gif;base64," + encoded}}, "image/gif"},
		{"codex-audio", map[string]any{"type": "audio", "audio_url": "data:audio/wav;base64," + encoded}, "audio/wav"},
		{"input-audio", map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": encoded, "format": "mp3"}}, "audio/mpeg"},
		{"input-file", map[string]any{"type": "input_file", "file_data": "data:application/pdf;base64," + encoded}, "application/pdf"},
	} {
		t.Run(test.name, func(t *testing.T) {
			content := []any{map[string]any{"type": "text", "text": "context"}, test.block}
			refs, err := Inspect(content)
			if err != nil || len(refs) != 1 {
				t.Fatal(refs, err)
			}
			ref := refs[0]
			if !ref.Valid() {
				t.Fatal("Parser produced an invalid reference", ref)
			}
			if ref.ID != "a:1" || ref.State != "embedded" || ref.Size != len(body) || ref.SHA256 != digest(body) || ref.MediaType != test.media {
				t.Fatal(ref)
			}
			got, data, err := Read(content, ref.ID, ref.SourceDigest)
			if err != nil || got != ref || !bytes.Equal(data, body) {
				t.Fatal(got, err)
			}
			if _, _, err = Read(content, ref.ID, strings.Repeat("0", 64)); !errors.Is(err, ErrChanged) {
				t.Fatal(err)
			}
			if _, _, err = Read(content, "a:0", ref.SourceDigest); !errors.Is(err, ErrChanged) {
				t.Fatal(err)
			}
			wire, _ := json.Marshal(ref)
			if bytes.Contains(wire, []byte(encoded)) || bytes.Contains(wire, body) {
				t.Fatal("Metadata contains attachment bytes")
			}
		})
	}
}

func TestReferencesNeverExposeOrFollowLocations(t *testing.T) {
	for _, block := range []map[string]any{
		{"type": "localImage", "path": "/private/credentials"},
		{"type": "local_audio", "path": "/private/credentials"},
		{"type": "image", "source": map[string]any{"type": "url", "url": "http://127.0.0.1/private?token=secret"}},
		{"type": "image", "image_url": "file:///private/credentials"},
		{"type": "input_file", "file_id": "file-private"},
		{"type": "document", "source": map[string]any{"type": "file", "file_id": "file-private"}},
	} {
		content := []any{block}
		refs, err := Inspect(content)
		if err != nil || len(refs) != 1 || refs[0].State != "reference" || refs[0].SHA256 != "" {
			t.Fatal(refs, err)
		}
		wire, _ := json.Marshal(refs)
		if bytes.Contains(wire, []byte("private")) || bytes.Contains(wire, []byte("secret")) {
			t.Fatal("Reference leaked a location")
		}
		if _, data, err := Read(content, refs[0].ID, refs[0].SourceDigest); !errors.Is(err, ErrUnavailable) || len(data) != 0 {
			t.Fatal(err)
		}
		block["path"] = "changed"
		changed, _ := Inspect(content)
		if changed[0].SourceDigest == refs[0].SourceDigest {
			t.Fatal("Changed source retained its digest")
		}
	}
}

func TestMalformedUnsupportedAndLimitsAreVisible(t *testing.T) {
	for _, test := range []struct{ url, state string }{
		{"data:image/png;base64,?", "invalid"},
		{"data:image/png;base64,QR==", "invalid"},
		{"data:image/png;base64,QQ==\n", "invalid"},
		{"data:image/png;base64,", "invalid"},
		{"data:image/png,abc", "invalid"},
		{"data:text/html;base64,QQ==", "unsupported"},
		{"data:image/png;base64," + strings.Repeat("A", base64.StdEncoding.EncodedLen(MaxBytes)+4), "too_large"},
	} {
		refs, err := Inspect([]any{map[string]any{"type": "image", "url": test.url}})
		if err != nil || len(refs) != 1 || refs[0].State != test.state {
			t.Fatal(test.state, refs, err)
		}
	}
	block := map[string]any{"type": "image", "url": "https://example.test/image"}
	tooMany := make([]any, MaxPerMessage+1)
	for i := range tooMany {
		tooMany[i] = block
	}
	if _, err := Inspect(tooMany); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	nested := []any{block}
	for i := 0; i < 10; i++ {
		nested = []any{map[string]any{"type": "tool_result", "content": nested}}
	}
	if _, err := Inspect(nested); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	refs, err := Inspect([]any{map[string]any{"type": "tool_result", "content": []any{block, block}}})
	if err != nil || len(refs) != 2 || refs[0].ID != "a:0/0" || refs[1].ID != "a:0/1" {
		t.Fatal(refs, err)
	}
}

func TestEmbeddedByteBoundary(t *testing.T) {
	for _, size := range []int{MaxBytes, MaxBytes + 1} {
		body := bytes.Repeat([]byte{0x61}, size)
		content := []any{map[string]any{"type": "image", "url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(body)}}
		refs, err := Inspect(content)
		if err != nil || len(refs) != 1 {
			t.Fatal(refs, err)
		}
		if size == MaxBytes {
			if refs[0].State != "embedded" || refs[0].Size != size || refs[0].SHA256 != digest(body) {
				t.Fatal(refs)
			}
		} else if refs[0].State != "too_large" || refs[0].SHA256 != "" || refs[0].Size != 0 {
			t.Fatal(refs)
		}
	}
}

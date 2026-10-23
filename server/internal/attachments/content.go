// Package attachments reads attachment descriptions from provider transcript
// blocks. It never opens a path, follows a URL, or contacts a provider.
package attachments

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	MaxBytes      = 16 << 20
	MaxPerMessage = 64
)

var (
	ErrLimit       = errors.New("This message exceeds the attachment inspection limit")
	ErrChanged     = errors.New("The attachment changed; reload the session before reading it")
	ErrUnavailable = errors.New("The transcript does not contain readable attachment bytes")
)

// Reference contains no provider path, URL, file ID or inline body. SourceDigest
// binds the complete original block, including references whose bytes are absent.
// ID identifies its position within one message, not a globally readable blob.
type Reference struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Source       string `json:"source"`
	SourceDigest string `json:"sourceDigest"`
	State        string `json:"state"`
	MediaType    string `json:"mediaType,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	Size         int    `json:"size,omitempty"`
}

func digest(data []byte) string   { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func stringValue(v any) string    { s, _ := v.(string); return s }
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }

// Inspect returns bounded metadata. Inline bodies are hashed but are not kept
// in the returned references or written to a second cache.
func Inspect(content any) ([]Reference, error) {
	refs, _, err := inspect(content, "")
	return refs, err
}

// Read returns only embedded bytes matching the exact reviewed source block.
// Local paths, remote URLs and provider file IDs remain references, even if the
// caller happens to have filesystem or network access.
func Read(content any, id, sourceDigest string) (Reference, []byte, error) {
	refs, data, err := inspect(content, id)
	if err != nil {
		return Reference{}, nil, err
	}
	for _, ref := range refs {
		if ref.ID != id {
			continue
		}
		if ref.SourceDigest != sourceDigest {
			return Reference{}, nil, ErrChanged
		}
		if ref.State != "embedded" {
			return Reference{}, nil, ErrUnavailable
		}
		return ref, data, nil
	}
	return Reference{}, nil, ErrChanged
}

func inspect(content any, selected string) ([]Reference, []byte, error) {
	var refs []Reference
	var selectedData []byte
	nodes := 0
	var walk func(any, string, int) error
	walk = func(value any, prefix string, depth int) error {
		blocks, _ := value.([]any)
		if len(blocks) == 0 {
			return nil
		}
		if depth > 8 {
			return ErrLimit
		}
		for i, raw := range blocks {
			nodes++
			if nodes > 4096 {
				return ErrLimit
			}
			block := object(raw)
			id := fmt.Sprintf("%s%d", prefix, i)
			typ := stringValue(block["type"])
			if typ == "tool_result" {
				if err := walk(block["content"], id+"/", depth+1); err != nil {
					return err
				}
				continue
			}
			kind := ""
			switch typ {
			case "image", "input_image", "image_url", "local_image", "localImage":
				kind = "image"
			case "audio", "input_audio", "local_audio", "localAudio":
				kind = "audio"
			case "document", "file", "input_file":
				kind = "document"
			default:
				continue
			}
			if len(refs) >= MaxPerMessage {
				return ErrLimit
			}
			encoded, err := json.Marshal(block)
			if err != nil {
				return err
			}
			ref := Reference{ID: "a:" + id, Kind: kind, Source: "unknown", SourceDigest: digest(encoded), State: "unsupported"}
			body, media, source, state := sourceBytes(block, typ, kind)
			ref.Source, ref.State, ref.MediaType = source, state, media
			if state == "embedded" {
				ref.Size, ref.SHA256 = len(body), digest(body)
				if ref.ID == selected {
					selectedData = body
				}
			}
			refs = append(refs, ref)
		}
		return nil
	}
	err := walk(content, "", 0)
	return refs, selectedData, err
}

func sourceBytes(block map[string]any, typ, kind string) ([]byte, string, string, string) {
	if typ == "local_image" || typ == "localImage" || typ == "local_audio" || typ == "localAudio" {
		return nil, "", "local", "reference"
	}
	source := object(block["source"])
	switch stringValue(source["type"]) {
	case "base64":
		return decode(stringValue(source["data"]), stringValue(source["media_type"]), kind)
	case "text":
		body := stringValue(source["data"])
		if len(body) > MaxBytes {
			return nil, "text/plain", "inline", "too_large"
		}
		if body == "" || kind != "document" {
			return nil, "", "inline", "invalid"
		}
		return []byte(body), "text/plain", "inline", "embedded"
	case "url":
		return nil, "", "remote", "reference"
	case "file":
		return nil, "", "provider", "reference"
	}
	if stringValue(block["file_id"]) != "" || stringValue(block["fileId"]) != "" {
		return nil, "", "provider", "reference"
	}
	if nested := object(block["input_audio"]); nested != nil {
		format := stringValue(nested["format"])
		media := ""
		if format == "wav" {
			media = "audio/wav"
		} else if format == "mp3" {
			media = "audio/mpeg"
		}
		return decode(stringValue(nested["data"]), media, kind)
	}
	url := stringValue(block["image_url"])
	if url == "" {
		url = stringValue(object(block["image_url"])["url"])
	}
	if url == "" {
		url = stringValue(block["url"])
	}
	if url == "" {
		url = stringValue(block["audio_url"])
	}
	if url == "" {
		url = stringValue(block["file_data"])
	}
	if strings.HasPrefix(url, "data:") {
		header, body, ok := strings.Cut(strings.TrimPrefix(url, "data:"), ",")
		if !ok || !strings.HasSuffix(header, ";base64") {
			return nil, "", "inline", "invalid"
		}
		return decode(body, strings.TrimSuffix(header, ";base64"), kind)
	}
	if url != "" {
		return nil, "", "remote", "reference"
	}
	return nil, "", "unknown", "unsupported"
}

func allowedMediaType(media, kind string) bool {
	allowed := false
	switch kind {
	case "image":
		allowed = media == "image/png" || media == "image/jpeg" || media == "image/gif" || media == "image/webp"
	case "audio":
		allowed = media == "audio/wav" || media == "audio/x-wav" || media == "audio/mpeg" || media == "audio/mp3" || media == "audio/mp4" || media == "audio/ogg" || media == "audio/webm"
	case "document":
		allowed = media == "application/pdf" || media == "text/plain" || media == "application/octet-stream"
	}
	return allowed
}

func decode(encoded, media, kind string) ([]byte, string, string, string) {
	if !allowedMediaType(media, kind) {
		return nil, "", "inline", "unsupported"
	}
	// Reject line breaks and bound before allocating; DecodeString otherwise
	// accepts arbitrary CR/LF padding, defeating a decoded-size-only bound.
	if len(encoded) > base64.StdEncoding.EncodedLen(MaxBytes) {
		return nil, media, "inline", "too_large"
	}
	if encoded == "" || strings.ContainsAny(encoded, "\r\n") {
		return nil, media, "inline", "invalid"
	}
	body, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, media, "inline", "invalid"
	}
	if len(body) > MaxBytes {
		return nil, media, "inline", "too_large"
	}
	return body, media, "inline", "embedded"
}

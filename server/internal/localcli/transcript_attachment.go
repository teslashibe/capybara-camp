package localcli

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/teslashibe/capybara-camp/server/internal/attachments"
)

type AttachmentRead struct {
	Session      Session `json:"session"`
	MessageID    string  `json:"messageID"`
	AttachmentID string  `json:"attachmentID"`
	SourceDigest string  `json:"sourceDigest"`
}

type AttachmentBody struct {
	MessageID  string                `json:"messageID"`
	Attachment attachments.Reference `json:"attachment"`
	Data       []byte                `json:"data"`
}

func (a *App) readTranscriptAttachment(ctx context.Context, input io.Reader) (AttachmentBody, error) {
	var request AttachmentRead
	decoder := json.NewDecoder(io.LimitReader(input, 1<<20+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return AttachmentBody{}, errors.New("Invalid attachment request")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return AttachmentBody{}, errors.New("Unexpected data after attachment request")
	}
	return a.TranscriptAttachment(ctx, request)
}

// TranscriptAttachment reopens the provider record read-only. Last-write wins
// for amended Claude messages, exactly as in Transcript and ExportPage. No
// attachment bytes escape if a later amendment or partial write invalidates it.
func (a *App) TranscriptAttachment(ctx context.Context, request AttachmentRead) (AttachmentBody, error) {
	if err := validSession(request.Session); err != nil {
		return AttachmentBody{}, err
	}
	if request.MessageID == "" || len(request.MessageID) > 4096 || len(request.AttachmentID) > 100 || len(request.SourceDigest) != 64 {
		return AttachmentBody{}, errors.New("Invalid attachment selection")
	}
	before, err := fingerprint(request.Session)
	if err != nil {
		return AttachmentBody{}, err
	}
	var result AttachmentBody
	selectedErr := attachments.ErrChanged
	err = recordsContent(ctx, request.Session, true, func(m Message, content any) error {
		id := m.ID
		if len(id) > 256 {
			id = "m:" + hash([]byte(id))
		}
		if m.ID != request.MessageID && id != request.MessageID {
			return nil
		}
		ref, data, readErr := attachments.Read(content, request.AttachmentID, request.SourceDigest)
		selectedErr = readErr
		result = AttachmentBody{MessageID: request.MessageID, Attachment: ref, Data: data}
		return nil
	})
	if err != nil {
		return AttachmentBody{}, err
	}
	if selectedErr != nil {
		return AttachmentBody{}, selectedErr
	}
	after, err := fingerprint(request.Session)
	if err != nil {
		return AttachmentBody{}, err
	}
	if before != after {
		return AttachmentBody{}, attachments.ErrChanged
	}
	return result, nil
}

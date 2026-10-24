package localcli

import (
	"context"
	"encoding/base64"
	"errors"
	"unicode/utf8"

	"github.com/teslashibe/capybara-camp/server/internal/attachments"
)

func validHistoryAttachmentSelection(in HistoryRead) bool {
	a := in.Attachment
	return a != nil && a.MessageID != "" && len(a.MessageID) <= 256 && len(a.ID) <= 100 && a.ID != "" && installDigest(a.SourceDigest) && in.Offset == 0 && in.TextOffset == 0 && in.Limit == 1
}
func validateHistoryAttachment(source HistorySource, in HistoryRead, page HistoryPage) error {
	if !validHistoryAttachmentSelection(in) || page.SessionID != source.ID || page.Revision != source.Revision || len(page.Messages) != 0 || page.NextOffset != nil || page.NextTextOffset != 0 || page.Attachment == nil {
		return errors.New("Invalid history attachment response")
	}
	a, selected := page.Attachment, in.Attachment
	if a.MessageID != selected.MessageID || a.Reference.ID != selected.ID || a.Reference.SourceDigest != selected.SourceDigest || !a.Reference.Valid() || a.Reference.State != "embedded" || len(a.Data) > attachments.MaxBytes || len(a.Data) != a.Reference.Size || installHash(a.Data) != a.Reference.SHA256 {
		return errors.New("History attachment changed")
	}
	return nil
}

func (t contextTool) readAttachment(raw any) ([]any, error) {
	args, ok := raw.(map[string]any)
	if !ok || len(args) != 4 {
		return nil, errors.New("Choose a message attachment returned by read_session")
	}
	for _, key := range []string{"session_id", "message_id", "attachment_id", "source_digest"} {
		if _, ok := args[key].(string); !ok {
			return nil, errors.New("Invalid attachment selection")
		}
	}
	in := HistoryRead{SessionID: str(args["session_id"]), Limit: 1, Attachment: &HistoryAttachmentSelection{MessageID: str(args["message_id"]), ID: str(args["attachment_id"]), SourceDigest: str(args["source_digest"])}}
	if !validHistoryAttachmentSelection(in) || t.reader == nil {
		return nil, errors.New("Invalid attachment selection")
	}
	var source *HistorySource
	for i := range t.history {
		if t.history[i].ID == in.SessionID {
			source = &t.history[i]
			break
		}
	}
	if source == nil {
		return nil, errors.New("This history is not attached to the current turn")
	}
	ctx := t.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	page, err := t.reader(ctx, in)
	if err != nil {
		return nil, err
	}
	if err = validateHistoryAttachment(*source, in, page); err != nil {
		return nil, err
	}
	body := page.Attachment
	data := base64.StdEncoding.EncodeToString(body.Data)
	switch body.Reference.Kind {
	case "image":
		return []any{map[string]any{"type": "image", "mimeType": body.Reference.MediaType, "data": data}}, nil
	case "audio":
		return []any{map[string]any{"type": "audio", "mimeType": body.Reference.MediaType, "data": data}}, nil
	default:
		resource := map[string]any{"uri": "capybara-attachment:" + body.Reference.SHA256, "mimeType": body.Reference.MediaType}
		if body.Reference.MediaType == "text/plain" && utf8.Valid(body.Data) {
			resource["text"] = string(body.Data)
		} else {
			resource["blob"] = data
		}
		return []any{map[string]any{"type": "resource", "resource": resource}}, nil
	}
}

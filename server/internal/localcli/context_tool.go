package localcli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"
)

type contextMessage struct {
	ID   string `json:"id"`
	Role string `json:"role"`
	Text string `json:"text"`
}

type contextSource struct {
	ID       string           `json:"id"`
	Title    string           `json:"title"`
	Revision string           `json:"revision"`
	Messages []contextMessage `json:"messages"`
}

func validateContext(sources []contextSource) error {
	if sources == nil || len(sources) > 20 {
		return errors.New("Attach up to 20 session sources")
	}
	seen, total := make(map[string]bool), 0
	for _, source := range sources {
		if n := utf8.RuneCountInString(source.ID); n == 0 || n > 1024 || seen[source.ID] {
			return errors.New("Invalid or repeated context source")
		}
		seen[source.ID] = true
		if utf8.RuneCountInString(source.Title) > 1000 || utf8.RuneCountInString(source.Revision) > 128 {
			return errors.New("Invalid context metadata")
		}
		if len(source.Messages) == 0 || len(source.Messages) > 120 {
			return errors.New("Choose between 1 and 120 source messages")
		}
		ids := make(map[string]bool)
		for _, message := range source.Messages {
			if n := utf8.RuneCountInString(message.ID); n == 0 || n > 1024 || ids[message.ID] {
				return errors.New("Invalid context message identity")
			}
			ids[message.ID] = true
			if message.Role != "user" && message.Role != "assistant" && message.Role != "system" && message.Role != "tool" {
				return errors.New("Invalid context message")
			}
			if len(message.Text) > 48<<10 {
				return errors.New("Choose source messages under 48 KB each")
			}
			total += len(message.Text)
		}
	}
	if total > 256<<10 {
		return errors.New("The selected context exceeds 256 KB")
	}
	return nil
}

// Decode exact wire keys before the typed snapshot. Missing or null string
// values must not silently become empty strings and change its provenance.
func decodeContext(data []byte) ([]contextSource, error) {
	if len(data) > 1<<20 {
		return nil, errors.New("Context manifest is too large")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("Context manifest is not valid UTF-8")
	}
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil || len(raw) > 20 {
		return nil, errors.New("Invalid context manifest")
	}
	for _, source := range raw {
		if len(source) != 4 || !jsonStrings(source, "id", "title", "revision") {
			return nil, errors.New("Invalid context fields")
		}
		var messages []map[string]json.RawMessage
		if err := json.Unmarshal(source["messages"], &messages); err != nil || messages == nil || len(messages) > 120 {
			return nil, errors.New("Invalid context messages")
		}
		for _, message := range messages {
			if len(message) != 3 || !jsonStrings(message, "id", "role", "text") {
				return nil, errors.New("Invalid context message fields")
			}
		}
	}
	var sources []contextSource
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&sources); err != nil {
		return nil, errors.New("Invalid context manifest")
	}
	return sources, validateContext(sources)
}

func jsonStrings(value map[string]json.RawMessage, keys ...string) bool {
	for _, key := range keys {
		data := bytes.TrimSpace(value[key])
		if len(data) == 0 || data[0] != '"' {
			return false
		}
	}
	return true
}

func contextLink(source contextSource, message contextMessage) string {
	// PathEscape leaves '+' and other path-safe reserved bytes untouched; the
	// native deep link expects percent-encoded IDs in both path and query.
	quote := func(value string) string { return strings.ReplaceAll(url.QueryEscape(value), "+", "%20") }
	return "capybara://session/" + quote(source.ID) + "?message=" + quote(message.ID) + "&revision=" + quote(source.Revision)
}

// Keep the historical UTF-8, sorted-key JSON digest, including literal HTML and
// Unicode line separators. This is a source identity, not rendered HTML.
func utf8JSONString(value string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, char := range value {
		switch char {
		case '"', '\\':
			out.WriteByte('\\')
			out.WriteRune(char)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if char < 0x20 {
				fmt.Fprintf(&out, `\u%04x`, char)
			} else {
				out.WriteRune(char)
			}
		}
	}
	out.WriteByte('"')
	return out.String()
}

func contextDigest(messages []contextMessage) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, "[")
	for index, message := range messages {
		if index > 0 {
			_, _ = io.WriteString(hash, ",")
		}
		_, _ = io.WriteString(hash, `{"id":`+utf8JSONString(message.ID)+`,"role":`+utf8JSONString(message.Role)+`,"text":`+utf8JSONString(message.Text)+`}`)
	}
	_, _ = io.WriteString(hash, "]")
	return hex.EncodeToString(hash.Sum(nil))
}

type contextTool struct {
	sources []contextSource
	history []HistorySource
	reader  HistoryReader
	ctx     context.Context
}

func (t contextTool) listSubagents(raw any) (map[string]any, error) {
	args, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("Use an offset and limit")
	}
	offset, limit := int64(0), int64(20)
	for key, value := range args {
		n, ok := value.(json.Number)
		if !ok {
			return nil, errors.New("Use integer offsets")
		}
		v, e := n.Int64()
		if e != nil {
			return nil, errors.New("Use integer offsets")
		}
		switch key {
		case "offset":
			offset = v
		case "limit":
			limit = v
		default:
			return nil, errors.New("Unsupported child history parameter")
		}
	}
	if offset < 0 || limit < 1 || limit > 50 || len(t.history) < 2 || t.reader == nil {
		return nil, errors.New("Child histories are unavailable")
	}
	ctx := t.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	probe := HistoryRead{SessionID: t.history[0].ID, Limit: 1}
	page, err := t.reader(ctx, probe)
	if err != nil {
		return nil, err
	}
	if err = validateHistoryPage(t.history[0], probe, page); err != nil {
		return nil, err
	}
	children := t.history[1:]
	start := min(offset, int64(len(children)))
	end := min(start+limit, int64(len(children)))
	out := []map[string]string{}
	for _, child := range children[start:end] {
		out = append(out, map[string]string{"session_id": child.ID, "title": child.Title, "revision": child.Revision, "parent_id": child.ParentID})
	}
	var next any
	if end < int64(len(children)) {
		next = end
	}
	return map[string]any{"children": out, "next_offset": next}, nil
}

func (t contextTool) read(raw any) (map[string]any, error) {
	args, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("Read accepts only a selected session ID, offset and limit")
	}
	for _, h := range t.history {
		if h.ID != str(args["session_id"]) {
			continue
		}
		in, err := historyInput(args)
		if err != nil {
			return nil, err
		}
		if t.reader == nil {
			return nil, errors.New("History is unavailable")
		}
		ctx := t.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		page, err := t.reader(ctx, in)
		if err != nil {
			return nil, err
		}
		if err = validateHistoryPage(h, in, page); err != nil {
			return nil, err
		}
		for i := range page.Messages {
			page.Messages[i].Source = contextLink(contextSource{ID: h.ID, Revision: h.Revision}, contextMessage{ID: page.Messages[i].ID})
		}
		return map[string]any{"session_id": h.ID, "title": h.Title, "revision": h.Revision, "messages": page.Messages, "next_offset": page.NextOffset, "next_text_offset": page.NextTextOffset}, nil
	}
	for key := range args {
		if key == "text_offset" {
			n, ok := args[key].(json.Number)
			if !ok || n.String() != "0" {
				return nil, errors.New("Text offsets apply only to complete history")
			}
			continue
		}
		if key != "session_id" && key != "offset" && key != "limit" {
			return nil, errors.New("Read accepts only a selected session ID, offset and limit")
		}
	}
	var source *contextSource
	for index := range t.sources {
		if str(args["session_id"]) == t.sources[index].ID {
			source = &t.sources[index]
			break
		}
	}
	if source == nil {
		return nil, errors.New("This session was not attached to the current turn")
	}
	offset, limit := int64(0), int64(20)
	for key, target := range map[string]*int64{"offset": &offset, "limit": &limit} {
		if value, exists := args[key]; exists {
			number, ok := value.(json.Number)
			if !ok {
				return nil, errors.New("Use a non-negative offset and a limit between 1 and 120")
			}
			parsed, err := number.Int64()
			if err != nil {
				return nil, errors.New("Use a non-negative offset and a limit between 1 and 120")
			}
			*target = parsed
		}
	}
	if offset < 0 || limit < 1 || limit > 120 {
		return nil, errors.New("Use a non-negative offset and a limit between 1 and 120")
	}
	// Clamp before adding so an oversized offset cannot overflow or panic.
	start := min(offset, int64(len(source.Messages)))
	end := min(start+limit, int64(len(source.Messages)))
	messages := make([]map[string]any, 0, end-start)
	total := 0
	for _, message := range source.Messages[start:end] {
		total += len(message.Text)
		if total > 64<<10 {
			return nil, errors.New("This range exceeds 64 KB; use a smaller limit")
		}
		messages = append(messages, map[string]any{"id": message.ID, "role": message.Role, "text": message.Text, "source": contextLink(*source, message)})
	}
	var next any
	if end < int64(len(source.Messages)) {
		next = end
	}
	return map[string]any{"session_id": source.ID, "title": source.Title, "revision": source.Revision, "digest": contextDigest(source.Messages), "messages": messages, "next_offset": next}, nil
}

func (t contextTool) handle(request map[string]any) map[string]any {
	id, hasID := request["id"]
	if !hasID {
		return nil
	}
	response := map[string]any{"jsonrpc": "2.0", "id": id}
	params := map[string]any{}
	if raw, exists := request["params"]; exists {
		var ok bool
		params, ok = raw.(map[string]any)
		if !ok {
			response["error"] = map[string]any{"code": -32602, "message": "Expected object parameters"}
			return response
		}
	}
	switch str(request["method"]) {
	case "initialize":
		version := str(params["protocolVersion"])
		if version == "" {
			version = "2024-11-05"
		}
		response["result"] = map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "capybara-context", "version": "1.0"}}
	case "ping":
		response["result"] = map[string]any{}
	case "tools/list":
		ids := make([]string, 0, len(t.sources)+1)
		for _, source := range t.sources {
			ids = append(ids, source.ID)
		}
		children := false
		for _, source := range t.history {
			if source.ParentID == "" {
				ids = append(ids, source.ID)
			} else {
				children = true
			}
		}
		sessionProperty := map[string]any{"type": "string", "enum": ids}
		if children {
			sessionProperty = map[string]any{"type": "string", "description": "An attached session ID or an ID returned by list_subagents"}
		}
		annotations := map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false, "idempotentHint": true}
		tools := []any{map[string]any{
			"name": "read_session", "description": "Read an attached snapshot or a pinned parent/child conversation. Follow next_offset and next_text_offset for history pages. Cannot discover unrelated sessions or send prompts.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"session_id": sessionProperty, "offset": map[string]any{"type": "integer", "minimum": 0}, "text_offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 120}}, "required": []string{"session_id"}, "additionalProperties": false}, "annotations": annotations,
		}}
		if children {
			tools = append(tools, map[string]any{"name": "list_subagents", "description": "List child histories attached to the current conversation. Read only the histories you need using read_session.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50}}, "additionalProperties": false}, "annotations": annotations})
		}
		if len(t.history) > 0 {
			tools = append(tools, map[string]any{"name": "read_attachment", "description": "Read an embedded attachment from a pinned parent or child message returned by read_session. Returns its image, audio or document bytes. External file and URL references cannot be opened.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"session_id": sessionProperty, "message_id": map[string]any{"type": "string"}, "attachment_id": map[string]any{"type": "string"}, "source_digest": map[string]any{"type": "string"}}, "required": []string{"session_id", "message_id", "attachment_id", "source_digest"}, "additionalProperties": false}, "annotations": annotations})
		}
		response["result"] = map[string]any{"tools": tools}
	case "tools/call":
		if str(params["name"]) == "read_attachment" {
			content, err := t.readAttachment(params["arguments"])
			if err != nil {
				content = []any{map[string]any{"type": "text", "text": err.Error()}}
			}
			response["result"] = map[string]any{"content": content, "isError": err != nil}
			return response
		}
		var value map[string]any
		var err error
		switch str(params["name"]) {
		case "read_session":
			value, err = t.read(params["arguments"])
		case "list_subagents":
			value, err = t.listSubagents(params["arguments"])
		default:
			err = errors.New("This tool is not available")
		}
		var text string
		if err != nil {
			text = err.Error()
		} else {
			data, _ := json.Marshal(value)
			text = string(data)
		}
		response["result"] = map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": err != nil}
	default:
		response["error"] = map[string]any{"code": -32601, "message": "Method not available"}
	}
	return response
}

func privateContextFile(path string) ([]byte, error) {
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 || !ownedByUser(parent) {
		return nil, errors.New("Context directory must be private to this user")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("Could not open the selected context snapshot")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !ownedByUser(info) || info.Size() > 1<<20 {
		return nil, errors.New("Context snapshot must be a private file under 1 MB")
	}
	data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return nil, errors.New("Could not read the selected context snapshot")
	}
	return data, nil
}

func ownedByUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid())
}

// ServeContext is intentionally separate from Run: the only file path is
// supplied by Camp's process launch, never accepted from an MCP request.
func (a *App) ServeContext(ctx context.Context, path string, input io.Reader, output io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := privateContextFile(path)
	if err != nil {
		return err
	}
	if !utf8.Valid(data) {
		return errors.New("Context manifest is not valid UTF-8")
	}
	tool := contextTool{ctx: ctx}
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		var manifest contextManifest
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if d.Decode(&manifest) != nil || d.Decode(new(any)) != io.EOF || manifest.Version != 2 || len(manifest.History) == 0 {
			return errors.New("Invalid history manifest")
		}
		raw, _ := json.Marshal(manifest.Sources)
		tool.sources, err = decodeContext(raw)
		if err != nil {
			return err
		}
		if err = validateHistoryCatalog(tool.sources, manifest.History); err != nil {
			return err
		}
		tool.history = manifest.History
		tool.reader = manifest.Proxy.reader
	} else {
		tool.sources, err = decodeContext(data)
		if err != nil {
			return err
		}
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 16<<10), 128<<10+1)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(scanner.Bytes()) > 128<<10 {
			return errors.New("Context request exceeds 128 KB")
		}
		var request map[string]any
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.UseNumber()
		var response map[string]any
		if err := decoder.Decode(&request); err != nil || decoder.Decode(new(any)) != io.EOF {
			response = map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "Invalid request"}}
		} else if request != nil {
			response = tool.handle(request)
		}
		if response != nil {
			if err := encoder.Encode(response); err != nil {
				return err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return scanner.Err()
}

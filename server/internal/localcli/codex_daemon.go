package localcli

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const codexFrameLimit = 1 << 20
const codexThreadLimit = 2048

// This connection can only inspect a private local daemon. It never responds
// to approval requests, starts a thread, or adopts another client's session.
type codexDaemon struct {
	conn             net.Conn
	buffer, fragment []byte
	fragmented       bool
	serial           int
	events           []map[string]any
}

func openCodexDaemon(ctx context.Context, root string) (*codexDaemon, error) {
	path := filepath.Join(root, "app-server-control", "app-server-control.sock")
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || stat.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("The Codex control socket is not private to this user")
	}
	conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	c := &codexDaemon{conn: conn}
	stopClose := context.AfterFunc(ctx, c.close)
	defer stopClose()
	success := false
	defer func() {
		if !success {
			conn.Close()
		}
	}()
	keyBytes := make([]byte, 16)
	if _, err = rand.Read(keyBytes); err != nil {
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: "+key+"\r\nSec-WebSocket-Version: 13\r\n\r\n"); err != nil {
		return nil, err
	}
	for !strings.Contains(string(c.buffer), "\r\n\r\n") {
		chunk := make([]byte, 4096)
		n, e := conn.Read(chunk)
		c.buffer = append(c.buffer, chunk[:n]...)
		if len(c.buffer) > 16384 {
			return nil, errors.New("Codex handshake exceeded its size limit")
		}
		if e != nil {
			return nil, e
		}
	}
	parts := strings.SplitN(string(c.buffer), "\r\n\r\n", 2)
	header := strings.Split(parts[0], "\r\n")
	digest := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	accept := ""
	for _, line := range header[1:] {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(k, "Sec-WebSocket-Accept") {
			accept = strings.TrimSpace(v)
		}
	}
	if !strings.HasPrefix(header[0], "HTTP/1.1 101 ") || accept != base64.StdEncoding.EncodeToString(digest[:]) {
		return nil, errors.New("Codex refused its local observer")
	}
	c.buffer = []byte(parts[1])
	conn.SetDeadline(time.Time{})
	if _, err = c.rpc(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "capybara_activity", "version": "1.0"}, "capabilities": map[string]any{"experimentalApi": true}}); err != nil {
		return nil, err
	}
	if err = c.send(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return nil, err
	}
	success = true
	return c, nil
}

func (c *codexDaemon) close() { _ = c.conn.Close() }

func (c *codexDaemon) send(value map[string]any) error {
	switch str(value["method"]) {
	case "initialize", "initialized", "thread/loaded/list", "thread/read":
	default:
		return errors.New("Only read-only Codex observation is permitted")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.frame(1, data)
}

func (c *codexDaemon) frame(opcode byte, data []byte) error {
	if len(data) > codexFrameLimit {
		return errors.New("Codex observer frame exceeded its size limit")
	}
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return err
	}
	header := []byte{0x80 | opcode}
	switch {
	case len(data) < 126:
		header = append(header, 0x80|byte(len(data)))
	case len(data) < 65536:
		header = append(header, 0x80|126, byte(len(data)>>8), byte(len(data)))
	default:
		header = append(header, 0x80|127)
		header = binary.BigEndian.AppendUint64(header, uint64(len(data)))
	}
	header = append(header, mask...)
	for i, b := range data {
		header = append(header, b^mask[i%4])
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	for len(header) > 0 {
		n, err := c.conn.Write(header)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		header = header[n:]
	}
	return nil
}

func (c *codexDaemon) receive(ctx context.Context, timeout time.Duration) (map[string]any, error) {
	deadline := time.Now().Add(timeout)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(c.buffer) >= 2 {
			first, second := c.buffer[0], c.buffer[1]
			opcode, final := first&15, first&128 != 0
			if first&0x70 != 0 || second&128 != 0 {
				return nil, errors.New("Invalid Codex observer frame")
			}
			size, offset := uint64(second&127), 2
			completeHeader := true
			if size == 126 {
				if len(c.buffer) < 4 {
					completeHeader = false
				} else {
					size = uint64(binary.BigEndian.Uint16(c.buffer[2:4]))
					offset = 4
				}
			}
			if size == 127 {
				if len(c.buffer) < 10 {
					completeHeader = false
				} else {
					size = binary.BigEndian.Uint64(c.buffer[2:10])
					offset = 10
				}
			}
			if completeHeader {
				if size > codexFrameLimit || (opcode < 8 && uint64(len(c.fragment))+size > codexFrameLimit) {
					return nil, errors.New("Codex activity frame exceeded its size limit")
				}
				if uint64(len(c.buffer)) >= uint64(offset)+size {
					payload := c.buffer[offset : offset+int(size)]
					c.buffer = c.buffer[offset+int(size):]
					if opcode >= 8 {
						if !final || size > 125 {
							return nil, errors.New("Invalid Codex control frame")
						}
						switch opcode {
						case 8:
							return nil, io.EOF
						case 9:
							if err := c.frame(10, payload); err != nil {
								return nil, err
							}
						case 10:
						default:
							return nil, errors.New("Unsupported Codex control frame")
						}
						continue
					}
					switch {
					case opcode == 1 && !c.fragmented:
						c.fragment = append(c.fragment[:0], payload...)
						c.fragmented = !final
					case opcode == 0 && c.fragmented:
						c.fragment = append(c.fragment, payload...)
						c.fragmented = !final
					default:
						return nil, errors.New("Unsupported Codex observer message")
					}
					if c.fragmented {
						continue
					}
					var value map[string]any
					err := json.Unmarshal(c.fragment, &value)
					c.fragment = c.fragment[:0]
					if err != nil || value == nil {
						return nil, errors.New("Invalid Codex observer event")
					}
					return value, nil
				}
			}
		}
		if !time.Now().Before(deadline) {
			return nil, context.DeadlineExceeded
		}
		_ = c.conn.SetReadDeadline(deadline)
		chunk := make([]byte, 65536)
		n, err := c.conn.Read(chunk)
		c.buffer = append(c.buffer, chunk[:n]...)
		if err != nil {
			if e, ok := err.(net.Error); ok && e.Timeout() {
				return nil, context.DeadlineExceeded
			}
			return nil, err
		}
	}
}

func (c *codexDaemon) rpc(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	c.serial++
	id := c.serial
	if err := c.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	for {
		value, err := c.receive(ctx, 4*time.Second)
		if err != nil {
			return nil, err
		}
		if _, request := value["method"]; !request && number(value["id"]) == float64(id) {
			if _, failed := value["error"]; failed {
				return nil, errors.New("Codex rejected read-only observation")
			}
			result, ok := value["result"].(map[string]any)
			if !ok {
				return nil, errors.New("Invalid Codex observer response")
			}
			return result, nil
		}
		if _, request := value["id"]; !request {
			switch str(value["method"]) {
			case "thread/status/changed", "thread/started", "thread/closed":
				if len(c.events) >= codexThreadLimit {
					return nil, errors.New("Too many Codex status events")
				}
				c.events = append(c.events, value)
			}
		}
	}
}

func codexMetadata(row map[string]any) map[string]any {
	result := map[string]any{}
	for _, key := range []string{"id", "title", "name", "cwd", "path", "model", "source", "sourceKind", "parentThreadId", "agentNickname", "agentRole", "status", "updatedAt", "archived"} {
		if value, ok := row[key]; ok {
			switch key {
			case "status":
				result[key] = boundedCodexStatus(value)
			case "source", "sourceKind":
				if codexChild(value) {
					result[key] = map[string]any{"subagent": true}
				} else if text, ok := value.(string); ok && len(text) <= 4096 {
					result[key] = text
				}
			default:
				if text, ok := value.(string); ok {
					if len(text) > 8192 {
						text = text[:8192]
					}
					result[key] = text
				} else if key == "updatedAt" {
					if _, ok := value.(float64); ok {
						result[key] = value
					}
				} else if key == "archived" {
					if _, ok := value.(bool); ok {
						result[key] = value
					}
				}
			}
		}
	}
	return result
}

func boundedCodexStatus(value any) map[string]any {
	row := object(value)
	kind := str(row["type"])
	if len(kind) > 64 {
		kind = "unknown"
	}
	result := map[string]any{"type": kind}
	if raw, exists := row["activeFlags"]; exists {
		flags, ok := raw.([]any)
		if !ok || len(flags) > 32 {
			return map[string]any{"type": "unknown"}
		}
		for _, flag := range flags {
			value, ok := flag.(string)
			if !ok || len(value) > 128 {
				return map[string]any{"type": "unknown"}
			}
		}
		result["activeFlags"] = flags
	}
	return result
}

func (c *codexDaemon) loadedIDs(ctx context.Context) ([]string, error) {
	ids := []string{}
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > codexThreadLimit {
			return nil, errors.New("Too many loaded Codex thread pages")
		}
		params := map[string]any{"limit": 200}
		if cursor != "" {
			params["cursor"] = cursor
		}
		page, err := c.rpc(ctx, "thread/loaded/list", params)
		if err != nil {
			return nil, err
		}
		data, ok := page["data"].([]any)
		if !ok {
			return nil, errors.New("Invalid loaded Codex thread list")
		}
		for _, value := range data {
			id, ok := value.(string)
			if !ok || id == "" || len(id) > 200 || seen["id:"+id] || len(ids) >= codexThreadLimit {
				return nil, errors.New("Invalid or oversized Codex thread list")
			}
			seen["id:"+id] = true
			ids = append(ids, id)
		}
		next := page["nextCursor"]
		if next == nil || next == "" {
			break
		}
		cursor, ok = next.(string)
		if !ok || len(cursor) > 4096 || seen["cursor:"+cursor] {
			return nil, errors.New("Invalid Codex page cursor")
		}
		seen["cursor:"+cursor] = true
	}
	return ids, nil
}

func (c *codexDaemon) loaded(ctx context.Context) (map[string]map[string]any, error) {
	ids, err := c.loadedIDs(ctx)
	if err != nil {
		return nil, err
	}
	rows := map[string]map[string]any{}
	for _, id := range ids {
		result, err := c.rpc(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": false})
		if err != nil {
			return nil, err
		}
		row := object(result["thread"])
		if str(row["id"]) == id {
			rows[id] = codexMetadata(row)
		}
	}
	return rows, nil
}

func codexStatus(value any) string {
	status := object(value)
	switch str(status["type"]) {
	case "idle":
		return "idle"
	case "notLoaded":
		return ""
	case "active":
		flags, ok := status["activeFlags"].([]any)
		if _, present := status["activeFlags"]; !present {
			flags, ok = []any{}, true
		}
		if !ok {
			return "unknown"
		}
		waiting := false
		for _, flag := range flags {
			s, ok := flag.(string)
			if !ok {
				return "unknown"
			}
			waiting = waiting || s == "waitingOnApproval" || s == "waitingOnUserInput"
		}
		if waiting {
			return "waiting"
		}
		return "running"
	default:
		return "unknown"
	}
}

func codexChild(value any) bool {
	if s, ok := value.(string); ok {
		var parsed any
		if json.Unmarshal([]byte(s), &parsed) == nil {
			return codexChild(parsed)
		}
		return strings.HasPrefix(s, `{"subagent"`)
	}
	row := object(value)
	_, first := row["subAgent"]
	_, second := row["subagent"]
	return first || second
}

func requireCodexSources(sources []Source) ([]Source, error) {
	result := []Source{}
	seen := map[string]bool{}
	for _, s := range sources {
		if s.Provider != "Codex" {
			continue
		}
		root, err := canonical(s.Root, "")
		if err != nil || !filepath.IsAbs(s.Root) || root != s.Root || s.ID != "Codex:"+root || s.Label == "" || seen[s.ID] {
			return nil, fmt.Errorf("Invalid Codex observer profile")
		}
		seen[s.ID] = true
		result = append(result, s)
	}
	if len(result) > 32 {
		return nil, errors.New("At most 32 Codex profiles can be observed")
	}
	return result, nil
}

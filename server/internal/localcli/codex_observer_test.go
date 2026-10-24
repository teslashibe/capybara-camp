package localcli

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func serverFrame(opcode byte, final bool, data []byte) []byte {
	first := opcode
	if final {
		first |= 128
	}
	result := []byte{first}
	switch {
	case len(data) < 126:
		result = append(result, byte(len(data)))
	case len(data) < 65536:
		result = append(result, 126, byte(len(data)>>8), byte(len(data)))
	default:
		result = append(result, 127)
		result = binary.BigEndian.AppendUint64(result, uint64(len(data)))
	}
	return append(result, data...)
}
func clientFrame(r io.Reader) (byte, []byte, error) {
	h := make([]byte, 2)
	if _, err := io.ReadFull(r, h); err != nil {
		return 0, nil, err
	}
	size := uint64(h[1] & 127)
	if h[1]&128 == 0 {
		return 0, nil, fmt.Errorf("client frame not masked")
	}
	if size == 126 {
		b := make([]byte, 2)
		if _, err := io.ReadFull(r, b); err != nil {
			return 0, nil, err
		}
		size = uint64(binary.BigEndian.Uint16(b))
	}
	if size == 127 {
		b := make([]byte, 8)
		if _, err := io.ReadFull(r, b); err != nil {
			return 0, nil, err
		}
		size = binary.BigEndian.Uint64(b)
	}
	if size > codexFrameLimit {
		return 0, nil, fmt.Errorf("oversized client frame")
	}
	mask := make([]byte, 4)
	if _, err := io.ReadFull(r, mask); err != nil {
		return 0, nil, err
	}
	b := make([]byte, int(size))
	if _, err := io.ReadFull(r, b); err != nil {
		return 0, nil, err
	}
	for i := range b {
		b[i] ^= mask[i%4]
	}
	return h[0] & 15, b, nil
}

func TestCodexFramesKeepPartialReadsAndControlFrames(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	c := &codexDaemon{conn: a}
	data := []byte(`{"method":"thread/status/changed","params":{"threadId":"one"}}`)
	go func() {
		b.Write(serverFrame(1, false, data[:20]))
		b.Write(serverFrame(9, true, []byte("ping")))
		_, _, _ = clientFrame(b)
		b.Write(serverFrame(0, true, data[20:]))
	}()
	value, err := c.receive(context.Background(), time.Second)
	if err != nil || str(value["method"]) != "thread/status/changed" {
		t.Fatal(value, err)
	}
	// The header can span a read deadline. Its consumed bytes must survive.
	frame := serverFrame(1, true, []byte(`{"ok":true}`))
	go func() { b.Write(frame[:1]); time.Sleep(40 * time.Millisecond); b.Write(frame[1:]) }()
	if _, err = c.receive(context.Background(), 10*time.Millisecond); err == nil {
		t.Fatal("missing deadline")
	}
	value, err = c.receive(context.Background(), time.Second)
	if err != nil || value["ok"] != true {
		t.Fatal(value, err)
	}
	if err = c.send(map[string]any{"method": "thread/resume"}); err == nil {
		t.Fatal("observer may not resume")
	}
}

func TestCodexFramesRejectInvalidOrOversizedData(t *testing.T) {
	for _, data := range [][]byte{{0x81, 0x80}, {0xc1, 0}, {0x89, 126, 0, 126}, {0x81, 127, 0, 0, 0, 0, 0, 0x20, 0, 0}, serverFrame(0, true, []byte("stray")), serverFrame(1, true, []byte(`[]`))} {
		a, b := net.Pipe()
		c := &codexDaemon{conn: a}
		go func() { b.Write(data); b.Close() }()
		_, err := c.receive(context.Background(), time.Second)
		a.Close()
		if err == nil {
			t.Fatal("accepted invalid frame", data)
		}
	}
}

func TestCodexPingDoesNotCountAgainstFragmentSize(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	c := &codexDaemon{conn: a}
	data := []byte(`{"padding":"` + strings.Repeat("x", codexFrameLimit-20) + `"}`)
	go func() {
		b.Write(serverFrame(1, false, data[:len(data)-2]))
		b.Write(serverFrame(9, true, []byte("control-ping")))
		clientFrame(b)
		b.Write(serverFrame(0, true, data[len(data)-2:]))
	}()
	if _, err := c.receive(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestRolloutTracksOnlyCompletedRecordsAndOwnedInode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	write(t, path, "{\"type\":\"session_meta\",\"payload\":{\"id\":\"one\"}}\n{\"type\":\"event_msg\",\"payload\":{\"type\":\"task_started\",\"turn_id\":\"turn\"}}\n")
	info, _ := os.Stat(path)
	st := info.Sys().(*syscall.Stat_t)
	writer := rolloutWriter{Path: path, Device: uint64(st.Dev), Inode: uint64(st.Ino)}
	r := &rolloutIndex{path: path}
	state, _, err := r.update(context.Background(), writer, 5)
	if err != nil || state != "unknown" {
		t.Fatal(state, err)
	}
	state, _, err = r.update(context.Background(), writer, 8<<20)
	if err != nil || state != "running" || r.sessionID != "one" {
		t.Fatal(state, r.sessionID, err)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	io.WriteString(f, `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"turn"}}`)
	f.Close()
	state, _, _ = r.update(context.Background(), writer, 8<<20)
	if state != "running" {
		t.Fatal("partial record ended a turn")
	}
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	io.WriteString(f, "\n")
	f.Close()
	state, _, _ = r.update(context.Background(), writer, 8<<20)
	if state != "idle" {
		t.Fatal(state)
	}
	writer.Inode++
	if _, _, err = r.update(context.Background(), writer, 8<<20); err == nil {
		t.Fatal("unowned inode accepted")
	}
	write(t, path, "{\"type\":\"session_meta\",\"payload\":{\"id\":\"two\"}}\n")
	writer.Inode--
	_, _, err = r.update(context.Background(), writer, 8<<20)
	if err != nil || r.sessionID != "two" || r.state != "unknown" {
		t.Fatal(r, err)
	}
	write(t, path, strings.Repeat("x", codexFrameLimit+1)+"\n{\"type\":\"event_msg\",\"payload\":{\"type\":\"turn_started\"}}\n")
	r = &rolloutIndex{path: path}
	state, _, err = r.update(context.Background(), writer, 8<<20)
	if err != nil || state != "running" || len(r.partial) != 0 {
		t.Fatal(state, err)
	}
}

func TestLsofRequiresWritableMatchingInodeAndSource(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "cw-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	root, _ = filepath.EvalSymlinks(root)
	source := sourceFor(root, "Codex")
	path := filepath.Join(root, "sessions", "a.jsonl")
	write(t, path, "{}\n")
	info, _ := os.Stat(path)
	st := info.Sys().(*syscall.Stat_t)
	row := func(access, inode, name string) []byte {
		return []byte("p100\x00\nf3\x00a" + access + "\x00tREG\x00D" + strconv.FormatUint(uint64(st.Dev), 16) + "\x00i" + inode + "\x00n" + name + "\x00")
	}
	valid := row("w", strconv.FormatUint(uint64(st.Ino), 10), path)
	if actual := parseCodexWriters(valid, []Source{source}); len(actual) != 1 || actual[0].PID != 100 {
		t.Fatal(actual)
	}
	for _, invalid := range [][]byte{row("r", strconv.FormatUint(uint64(st.Ino), 10), path), row("w", "1", path), row("w", strconv.FormatUint(uint64(st.Ino), 10), path+".missing")} {
		if actual := parseCodexWriters(invalid, []Source{source}); len(actual) != 0 {
			t.Fatal(actual)
		}
	}
	if actual := parseCodexWriters(valid, []Source{sourceFor(filepath.Join(root, "other"), "Codex")}); len(actual) != 0 {
		t.Fatal(actual)
	}
}

type observerServer struct {
	root      string
	listener  net.Listener
	lock      sync.Mutex
	methods   []string
	commands  chan map[string]any
	connected chan struct{}
	done      chan struct{}
}

func newObserverServer(t *testing.T) *observerServer {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "cd-")
	if err != nil {
		t.Fatal(err)
	}
	root, _ = filepath.EvalSymlinks(root)
	os.MkdirAll(filepath.Join(root, "app-server-control"), 0700)
	path := filepath.Join(root, "app-server-control", "app-server-control.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		os.RemoveAll(root)
		t.Fatal(err)
	}
	os.Chmod(path, 0600)
	s := &observerServer{root: root, listener: listener, commands: make(chan map[string]any, 8), connected: make(chan struct{}), done: make(chan struct{})}
	t.Cleanup(func() { listener.Close(); os.RemoveAll(root) })
	go func() {
		defer close(s.done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		key := ""
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(strings.ToLower(line), "sec-websocket-key:") {
				key = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
			}
			if line == "\r\n" {
				break
			}
		}
		digest := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(digest[:]))
		var writes sync.Mutex
		send := func(v any) {
			b, _ := json.Marshal(v)
			writes.Lock()
			conn.Write(serverFrame(1, true, b))
			writes.Unlock()
		}
		ready := false
		go func() {
			for {
				select {
				case v := <-s.commands:
					send(v)
				case <-s.done:
					return
				}
			}
		}()
		for {
			_, data, err := clientFrame(reader)
			if err != nil {
				return
			}
			var request map[string]any
			if json.Unmarshal(data, &request) != nil {
				return
			}
			method := str(request["method"])
			s.lock.Lock()
			s.methods = append(s.methods, method)
			s.lock.Unlock()
			var result any
			switch method {
			case "initialize":
				result = map[string]any{}
			case "initialized":
				continue
			case "thread/loaded/list":
				result = map[string]any{"data": []string{"parent", "child"}}
			case "thread/read":
				id := str(object(request["params"])["threadId"])
				kind := any("cli")
				if id == "child" {
					kind = map[string]any{"subagent": map[string]any{}}
				}
				result = map[string]any{"thread": map[string]any{"id": id, "title": "Synthetic " + id, "source": kind, "status": map[string]any{"type": "active", "activeFlags": []string{}}}}
			default:
				result = map[string]any{}
			}
			send(map[string]any{"id": request["id"], "result": result})
			if method == "thread/read" && str(object(request["params"])["threadId"]) == "child" && !ready {
				close(s.connected)
				ready = true
			}
		}
	}()
	return s
}

func TestGoCodexObserverStreamsWithoutOwningSessions(t *testing.T) {
	s := newObserverServer(t)
	a := fixture(t)
	a.systemCapture = func(ctx context.Context, _ string, _ []string, _ time.Duration, _ int) (int, []byte, error) {
		return 0, nil, ctx.Err()
	}
	source := sourceFor(s.root, "Codex")
	payload, _ := json.Marshal([]Source{source})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w := io.Pipe()
	defer r.Close()
	done := make(chan error, 1)
	go func() { err := a.WatchCodex(ctx, strings.NewReader(string(payload)), w); w.Close(); done <- err }()
	values := make(chan CodexObservation, 20)
	go func() {
		defer close(values)
		d := json.NewDecoder(r)
		for {
			var value CodexObservation
			if d.Decode(&value) != nil {
				return
			}
			values <- value
		}
	}()
	wait := func(state string) {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for {
			select {
			case value, ok := <-values:
				if !ok {
					t.Fatal("observer stopped")
				}
				for _, live := range value.Activity {
					if live.ID == source.ID+":parent" && live.State == state {
						if len(value.Sessions) != 1 || value.Sessions[0].SessionID != "parent" {
							t.Fatal("child leaked into parent cards", value)
						}
						return
					}
				}
			case <-deadline:
				t.Fatal("missing state", state)
			}
		}
	}
	wait("running")
	s.commands <- map[string]any{"id": 77, "method": "item/commandExecution/requestApproval", "params": map[string]any{}}
	s.commands <- map[string]any{"method": "thread/status/changed", "params": map[string]any{"threadId": "parent", "status": map[string]any{"type": "active", "activeFlags": []string{"waitingOnApproval"}}}}
	wait("waiting")
	s.commands <- map[string]any{"method": "thread/status/changed", "params": map[string]any{"threadId": "parent", "status": map[string]any{"type": "idle"}}}
	wait("idle")
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("observer did not stop")
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	for _, method := range s.methods {
		switch method {
		case "initialize", "initialized", "thread/loaded/list", "thread/read":
		default:
			t.Fatal("observer wrote to session", method)
		}
	}
}

func TestObserverValidationAndStatusMapping(t *testing.T) {
	for _, input := range []string{"null", "{}", "[] {}", `[{"provider":"Codex","root":"relative","id":"bad","label":"Bad"}]`} {
		if err := fixture(t).WatchCodex(context.Background(), strings.NewReader(input), io.Discard); err == nil {
			t.Fatal("invalid input accepted", input)
		}
	}
	if codexStatus(map[string]any{"type": "active"}) != "running" || codexStatus(map[string]any{"type": "active", "activeFlags": []any{1}}) != "unknown" {
		t.Fatal("bad status")
	}
	if !codexChild(`{"subagent":{}}`) || codexChild("cli") {
		t.Fatal("bad child classification")
	}
	if !reflect.DeepEqual(codexMetadata(map[string]any{"id": "one", "turns": "private"}), map[string]any{"id": "one"}) {
		t.Fatal("observer retained transcript")
	}
	value := codexMetadata(map[string]any{"id": "one", "title": strings.Repeat("x", codexFrameLimit), "status": map[string]any{"type": "active", "activeFlags": []any{strings.Repeat("x", 1000)}}})
	if len(str(value["title"])) > 8192 || codexStatus(value["status"]) != "unknown" {
		t.Fatal("unbounded retained metadata")
	}
}

func TestCodexObserverRejectsSharedSocket(t *testing.T) {
	s := newObserverServer(t)
	path := filepath.Join(s.root, "app-server-control", "app-server-control.sock")
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := openCodexDaemon(context.Background(), s.root); err == nil {
		t.Fatal("shared socket accepted")
	}
}

func TestCodexObserverRPCDoesNotAnswerApprovalIDCollision(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	client := &codexDaemon{conn: a}
	go func() {
		_, data, err := clientFrame(b)
		if err != nil {
			return
		}
		var request map[string]any
		json.Unmarshal(data, &request)
		message, _ := json.Marshal(map[string]any{"id": request["id"], "method": "item/commandExecution/requestApproval", "params": map[string]any{}})
		b.Write(serverFrame(1, true, message))
		response, _ := json.Marshal(map[string]any{"id": request["id"], "result": map[string]any{"readOnly": true}})
		b.Write(serverFrame(1, true, response))
	}()
	value, err := client.rpc(context.Background(), "thread/read", map[string]any{"threadId": "one"})
	if err != nil || value["readOnly"] != true {
		t.Fatal(value, err)
	}
}

func TestCodexObserverRepeatedStopReleasesReaders(t *testing.T) {
	before := runtime.NumGoroutine()
	for range 5 {
		s := newObserverServer(t)
		a := fixture(t)
		a.systemCapture = func(ctx context.Context, _ string, _ []string, _ time.Duration, _ int) (int, []byte, error) {
			return 0, nil, ctx.Err()
		}
		input, _ := json.Marshal([]Source{sourceFor(s.root, "Codex")})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- a.WatchCodex(ctx, strings.NewReader(string(input)), io.Discard) }()
		select {
		case <-s.connected:
		case <-time.After(2 * time.Second):
			t.Fatal("did not connect")
		}
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("did not stop")
		}
		s.listener.Close()
		select {
		case <-s.done:
		case <-time.After(time.Second):
			t.Fatal("server retained a connection")
		}
	}
	time.Sleep(20 * time.Millisecond)
	if actual := runtime.NumGoroutine(); actual > before+4 {
		t.Fatalf("reader goroutines grew: %d -> %d", before, actual)
	}
}

func TestCodexObserverBoundsUnfinishedRecordBuffers(t *testing.T) {
	root := t.TempDir()
	source := sourceFor(root, "Codex")
	observer := newCodexObserver([]Source{source})
	writers := []rolloutWriter{}
	for i := range 20 {
		path := filepath.Join(root, "sessions", fmt.Sprint(i)+".jsonl")
		write(t, path, strings.Repeat("x", 900<<10))
		info, _ := os.Stat(path)
		stat := info.Sys().(*syscall.Stat_t)
		writers = append(writers, rolloutWriter{SourceID: source.ID, Path: path, PID: 1, Device: uint64(stat.Dev), Inode: uint64(stat.Ino)})
	}
	observer.snapshot(context.Background(), writers, nil, "")
	total := 0
	for _, index := range observer.indexes {
		total += len(index.partial)
	}
	if total > 16<<20 {
		t.Fatalf("unbounded partial records: %d", total)
	}
	observer.snapshot(context.Background(), nil, nil, "")
	if len(observer.indexes) != 0 {
		t.Fatal("closed writers retained their buffers")
	}
}

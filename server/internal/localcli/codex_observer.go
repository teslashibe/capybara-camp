package localcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"sort"
	"sync"
	"time"
)

type CodexObservation struct {
	Activity []LiveState       `json:"activity"`
	Sessions []Session         `json:"sessions"`
	Errors   map[string]string `json:"errors"`
}
type observedMetadata struct {
	at      time.Time
	session Session
	kind    any
}
type observedChildren struct {
	at       time.Time
	children []Child
}
type codexObserver struct {
	sources  []Source
	indexes  map[string]*rolloutIndex
	metadata map[string]observedMetadata
	children map[string]observedChildren
}

func newCodexObserver(sources []Source) *codexObserver {
	return &codexObserver{sources: sources, indexes: map[string]*rolloutIndex{}, metadata: map[string]observedMetadata{}, children: map[string]observedChildren{}}
}

func (o *codexObserver) resolve(ctx context.Context, source Source, id, path string, row map[string]any) (Session, any) {
	key := source.ID + ":" + id
	cached, ok := o.metadata[key]
	if !ok || time.Since(cached.at) > 2*time.Second {
		cached.at = time.Now()
		cached.session, cached.kind = codexSession(ctx, source, id, path, row)
		o.metadata[key] = cached
	}
	return cached.session, cached.kind
}

func (o *codexObserver) snapshot(ctx context.Context, writers []rolloutWriter, daemon map[string]map[string]map[string]any, writerError string) CodexObservation {
	result := CodexObservation{Activity: []LiveState{}, Sessions: []Session{}, Errors: map[string]string{}}
	sources := map[string]Source{}
	for _, s := range o.sources {
		sources[s.ID] = s
		if writerError != "" && daemon[s.ID] == nil {
			result.Errors[s.ID] = writerError
		}
	}
	byID := map[string]LiveState{}
	sessions := map[string]Session{}
	paths := map[string]bool{}
	budget := int64(32 << 20)
	for _, writer := range writers {
		source, ok := sources[writer.SourceID]
		if !ok {
			continue
		}
		paths[writer.Path] = true
		index := o.indexes[writer.Path]
		if index == nil {
			index = &rolloutIndex{path: writer.Path, state: "unknown"}
			o.indexes[writer.Path] = index
		}
		limit := int64(8 << 20)
		if budget < limit {
			limit = budget
		}
		state, used, err := index.update(ctx, writer, limit)
		budget -= used
		if err != nil || index.sessionID == "" || len(index.sessionID) > 200 {
			continue
		}
		s, kind := o.resolve(ctx, source, index.sessionID, writer.Path, nil)
		if !codexChild(kind) {
			sessions[s.ID] = s
		}
		byID[s.ID] = LiveState{ID: s.ID, SourceID: source.ID, State: state, Model: s.Model, Children: []Child{}, Owner: "Codex", PID: writer.PID}
	}
	for path := range o.indexes {
		if !paths[path] {
			delete(o.indexes, path)
		}
	}
	// A collection of writers with unfinished giant lines must not retain a
	// megabyte per file. Dropped lines are skipped through their next newline.
	partialBudget := 16 << 20
	for _, index := range o.indexes {
		if len(index.partial) > partialBudget {
			index.partial = nil
			index.discard = true
		} else {
			partialBudget -= len(index.partial)
		}
	}
	for _, source := range o.sources {
		for id, row := range daemon[source.ID] {
			state := codexStatus(row["status"])
			if state == "" {
				continue
			}
			s, kind := o.resolve(ctx, source, id, "", row)
			byID[s.ID] = LiveState{ID: s.ID, SourceID: source.ID, State: state, Model: s.Model, Children: []Child{}, Owner: "Codex"}
			if !codexChild(kind) {
				sessions[s.ID] = s
			}
		}
	}
	for id, value := range byID {
		if parent, ok := sessions[id]; ok {
			cached, ok := o.children[id]
			if !ok || time.Since(cached.at) > 2*time.Second {
				cached.at = time.Now()
				cached.children, _ = codexChildren(ctx, parent)
				o.children[id] = cached
			}
			value.Children = append([]Child{}, cached.children...)
			for i, child := range value.Children {
				if observed, ok := byID[parent.SourceID+":"+child.SessionID]; ok {
					state := map[string]string{"running": "running", "idle": "done", "waiting": "waiting"}[observed.State]
					if state == "" {
						state = "unknown"
					}
					value.Children[i].Status = state
				}
			}
		}
		result.Activity = append(result.Activity, value)
	}
	for id := range o.metadata {
		if _, ok := byID[id]; !ok {
			delete(o.metadata, id)
		}
	}
	for id := range o.children {
		if _, ok := byID[id]; !ok {
			delete(o.children, id)
		}
	}
	for _, s := range sessions {
		result.Sessions = append(result.Sessions, s)
	}
	sort.Slice(result.Activity, func(i, j int) bool { return result.Activity[i].ID < result.Activity[j].ID })
	sort.Slice(result.Sessions, func(i, j int) bool { return result.Sessions[i].ID < result.Sessions[j].ID })
	return result
}

// Each profile's read-only socket reconnects independently. Only immutable
// metadata/status snapshots cross into the coordinator; no transcript content.
func watchCodexDaemon(ctx context.Context, source Source, publish func(map[string]map[string]any)) {
	delay := 500 * time.Millisecond
	for ctx.Err() == nil {
		connection, err := openCodexDaemon(ctx, source.Root)
		if err == nil {
			stopClose := context.AfterFunc(ctx, connection.close)
			snapshotCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			rows, loadErr := connection.loaded(snapshotCtx)
			cancel()
			err = loadErr
			if err == nil {
				publish(maps.Clone(rows))
				delay = 500 * time.Millisecond
			}
			for err == nil && ctx.Err() == nil {
				var event map[string]any
				if len(connection.events) > 0 {
					event = connection.events[0]
					connection.events = connection.events[1:]
				} else {
					event, err = connection.receive(ctx, 500*time.Millisecond)
				}
				if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
					err = nil
					continue
				}
				if err != nil {
					break
				}
				if _, request := event["id"]; request {
					continue
				}
				params := object(event["params"])
				id := str(params["threadId"])
				switch str(event["method"]) {
				case "thread/started":
					row := object(params["thread"])
					id = str(row["id"])
					if id != "" && len(id) <= 200 {
						rows[id] = codexMetadata(row)
					}
				case "thread/status/changed":
					if id == "" || len(id) > 200 {
						continue
					}
					row, ok := rows[id]
					if !ok {
						var response map[string]any
						response, err = connection.rpc(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": false})
						if err != nil {
							break
						}
						row = codexMetadata(object(response["thread"]))
						if str(row["id"]) != id {
							continue
						}
					}
					row = maps.Clone(row)
					row["status"] = boundedCodexStatus(params["status"])
					rows[id] = row
				case "thread/closed":
					delete(rows, id)
				default:
					continue
				}
				if len(rows) > codexThreadLimit {
					err = errors.New("Too many loaded Codex sessions")
					break
				}
				publish(maps.Clone(rows))
			}
			stopClose()
			connection.close()
		}
		publish(nil)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay < 10*time.Second {
			delay *= 2
			if delay > 10*time.Second {
				delay = 10 * time.Second
			}
		}
	}
}

func (a *App) WatchCodex(ctx context.Context, input io.Reader, output io.Writer) error {
	var sources []Source
	decoder := json.NewDecoder(io.LimitReader(input, (1<<20)+1))
	if err := decoder.Decode(&sources); err != nil {
		return errors.New("Invalid Codex profiles")
	}
	if sources == nil {
		return errors.New("Invalid Codex profiles")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("Unexpected Codex profile input")
	}
	sources, err := requireCodexSources(sources)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var lock sync.Mutex
	var workers sync.WaitGroup
	daemon := map[string]map[string]map[string]any{}
	writers := []rolloutWriter{}
	writerError := ""
	changed := make(chan struct{}, 1)
	signal := func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	for _, source := range sources {
		workers.Add(1)
		go func() {
			defer workers.Done()
			watchCodexDaemon(ctx, source, func(rows map[string]map[string]any) {
				lock.Lock()
				if rows == nil {
					delete(daemon, source.ID)
				} else {
					daemon[source.ID] = rows
				}
				lock.Unlock()
				signal()
			})
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		for ctx.Err() == nil {
			values, err := a.codexWriters(ctx, sources)
			message := ""
			if err != nil {
				message = "Codex desktop session ownership is unavailable"
				values = nil
			}
			lock.Lock()
			writers = values
			writerError = message
			lock.Unlock()
			signal()
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()
	defer func() { cancel(); workers.Wait() }()
	observer := newCodexObserver(sources)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var previous []byte
	for ctx.Err() == nil {
		lock.Lock()
		current := maps.Clone(daemon)
		files := append([]rolloutWriter{}, writers...)
		message := writerError
		lock.Unlock()
		result := observer.snapshot(ctx, files, current, message)
		wire, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if len(wire) > 2*codexFrameLimit || len(result.Activity) > 8192 {
			result = CodexObservation{Activity: []LiveState{}, Sessions: []Session{}, Errors: map[string]string{}}
			for _, source := range sources {
				result.Errors[source.ID] = "Codex activity exceeded its size limit"
			}
			wire, _ = json.Marshal(result)
		}
		if !bytes.Equal(wire, previous) {
			if file, ok := output.(*os.File); ok {
				_ = file.SetWriteDeadline(time.Now().Add(5 * time.Second))
			}
			if _, err = output.Write(append(wire, '\n')); err != nil {
				return err
			}
			previous = wire
		}
		select {
		case <-ctx.Done():
			return nil
		case <-changed:
		case <-ticker.C:
		}
	}
	return nil
}

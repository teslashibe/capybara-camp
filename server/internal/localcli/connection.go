package localcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const maxNativeEvent = 1 << 20

type nativeRejection struct{ message string }

func (e *nativeRejection) Error() string { return e.message }

type nativeEvent struct {
	value map[string]any
	err   error
}

type nativeConnection struct {
	ctx           context.Context
	cancel        context.CancelFunc
	command       *exec.Cmd
	stdin         io.WriteCloser
	stdout        io.ReadCloser
	stderr        io.ReadCloser
	events        chan nativeEvent
	backlog       []map[string]any
	allowRequests bool
	serial        int64
	writeMu       sync.Mutex
	readers       sync.WaitGroup
	waitDone      chan struct{}
	closeOnce     sync.Once
}

func (a *App) connect(ctx context.Context, s Session, args []string, allowRequests bool, env []string) (*nativeConnection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return nil, errors.New("Missing provider command")
	}
	if env == nil {
		env = a.runtimeEnvironment(s)
	}
	lookup := a.commandPath
	if lookup == nil {
		lookup = executable
	}
	path, err := lookup(args[0], env)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	c := &nativeConnection{ctx: ctx, cancel: cancel, events: make(chan nativeEvent, 16), allowRequests: allowRequests, waitDone: make(chan struct{})}
	c.command = exec.Command(path, args[1:]...)
	c.command.Env, c.command.Dir = env, s.CWD
	c.command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if c.stdin, err = c.command.StdinPipe(); err == nil {
		c.stdout, err = c.command.StdoutPipe()
	}
	if err == nil {
		c.stderr, err = c.command.StderrPipe()
	}
	if err == nil {
		err = ctx.Err()
		if err == nil {
			err = c.command.Start()
		}
	}
	if err != nil {
		contextErr := ctx.Err()
		cancel()
		for _, pipe := range []io.Closer{c.stdin, c.stdout, c.stderr} {
			if pipe != nil {
				_ = pipe.Close()
			}
		}
		if contextErr != nil {
			return nil, contextErr
		}
		return nil, errors.New("Could not start the selected provider CLI")
	}
	c.readers.Add(2)
	go c.readEvents()
	go func() {
		defer c.readers.Done()
		// Drain without retaining or reporting possible credential-bearing logs.
		_, _ = io.Copy(io.Discard, c.stderr)
	}()
	go func() {
		c.readers.Wait()
		close(c.waitDone)
	}()
	go func() {
		<-ctx.Done()
		c.Close()
	}()
	return c, nil
}

func (c *nativeConnection) publish(event nativeEvent) bool {
	select {
	case c.events <- event:
		return true
	case <-c.ctx.Done():
		return false
	}
}

func (c *nativeConnection) readEvents() {
	defer c.readers.Done()
	defer close(c.events)
	scanner := bufio.NewScanner(c.stdout)
	scanner.Buffer(make([]byte, 64<<10), maxNativeEvent+2)
	for scanner.Scan() {
		if len(scanner.Bytes()) > maxNativeEvent {
			c.publish(nativeEvent{err: errors.New("The native CLI exceeded the event size limit")})
			return
		}
		var event map[string]any
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.UseNumber()
		if err := decoder.Decode(&event); err != nil || event == nil {
			continue
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			continue
		}
		if !c.publish(nativeEvent{value: event}) {
			return
		}
	}
	if scanner.Err() != nil && c.ctx.Err() == nil {
		c.publish(nativeEvent{err: errors.New("The native CLI exceeded the event size limit or closed its output")})
	}
}

func (c *nativeConnection) Write(event map[string]any) error {
	return c.writeBefore(event, time.Now().Add(20*time.Second))
}

func (c *nativeConnection) writeBefore(event map[string]any, deadline time.Time) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if len(encoded) > maxNativeEvent {
		return errors.New("The native CLI request exceeds 1 MiB")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.ctx.Err(); err != nil {
		return err
	}
	if end, ok := c.ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	pipe, ok := c.stdin.(interface{ SetWriteDeadline(time.Time) error })
	if !ok || pipe.SetWriteDeadline(deadline) != nil {
		return errors.New("The native CLI input cannot enforce a write deadline")
	}
	defer pipe.SetWriteDeadline(time.Time{})
	_, err = c.stdin.Write(append(encoded, '\n'))
	if err != nil {
		c.cancel() // A partial write has an uncertain outcome; stop this connection.
		return errors.New("The native CLI closed its input")
	}
	return nil
}

func sameID(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	return err == nil && bytes.Equal(left, right)
}

func (c *nativeConnection) RejectTool(event map[string]any) error {
	return c.Write(map[string]any{"id": event["id"], "error": map[string]any{"code": -32000, "message": "Tools are disabled in Camp's chat preview"}})
}

func (c *nativeConnection) RPC(method string, params map[string]any, timeout time.Duration) (map[string]any, error) {
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	c.serial++
	ident := c.serial
	ctx, cancel := context.WithTimeout(c.ctx, timeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	if err := c.writeBefore(map[string]any{"id": ident, "method": method, "params": params}, deadline); err != nil {
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			if c.ctx.Err() != nil {
				return nil, c.ctx.Err()
			}
			return nil, fmt.Errorf("The native CLI did not acknowledge %s: %w", method, context.DeadlineExceeded)
		case next, ok := <-c.events:
			if !ok {
				return nil, fmt.Errorf("The native CLI closed before acknowledging %s", method)
			}
			if next.err != nil {
				return nil, next.err
			}
			event := next.value
			// Server requests have their own ID namespace. A request that
			// happens to reuse our pending ID is never a response to our RPC.
			isRequest := str(event["method"]) != ""
			if !isRequest && sameID(event["id"], ident) {
				if _, failed := event["error"]; failed {
					failure := object(event["error"])
					message := str(failure["message"])
					if message == "" {
						message = "Native request rejected"
					}
					return nil, &nativeRejection{message: message}
				}
				result, ok := event["result"].(map[string]any)
				if !ok {
					return nil, errors.New("The native CLI returned an invalid response")
				}
				return result, nil
			}
			_, hasID := event["id"]
			if isRequest && hasID && !c.allowRequests {
				if err := c.RejectTool(event); err != nil {
					return nil, err
				}
				continue
			}
			if len(c.backlog) >= 32 {
				return nil, errors.New("The native CLI sent too many events before acknowledging the request")
			}
			c.backlog = append(c.backlog, event)
		}
	}
}

// Next yields briefly so the owning turn can process steering and approvals.
func (c *nativeConnection) Next() (map[string]any, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	if len(c.backlog) > 0 {
		event := c.backlog[0]
		c.backlog[0] = nil
		c.backlog = c.backlog[1:]
		return event, nil
	}
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-c.ctx.Done():
		return nil, c.ctx.Err()
	case next, ok := <-c.events:
		if !ok {
			return nil, io.EOF
		}
		return next.value, next.err
	case <-timer.C:
		return nil, nil
	}
}

func (c *nativeConnection) Close() {
	c.closeOnce.Do(func() {
		c.cancel()
		_ = c.stdin.Close()
		_ = syscall.Kill(-c.command.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-c.waitDone:
		case <-timer.C:
		}
		timer.Stop()
		// Also stop descendants after the direct CLI exits. It may have left
		// a helper holding a pipe open; only this owned group is signalled.
		_ = killGroup(c.command.Process.Pid)
		_ = c.stdout.Close()
		_ = c.stderr.Close()
		<-c.waitDone
		// Reap only after signalling the group. Keeping the direct child PID
		// reserved until here prevents PID reuse during group cleanup.
		_ = c.command.Wait()
	})
}

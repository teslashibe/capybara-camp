package main

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

func TestIdleContextInputCanBeCancelled(t *testing.T) {
	for iteration := 0; iteration < 5; iteration++ {
		source, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		input, err := pollableInput(source)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		stop := context.AfterFunc(ctx, func() { _ = input.Close() })
		started, done := make(chan struct{}), make(chan error, 1)
		go func() {
			close(started)
			_, err := io.Copy(io.Discard, input)
			done <- err
		}()
		<-started
		// Keep the writer open: EOF must not be what unblocks the read.
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, os.ErrClosed) {
				t.Fatal("Input did not close on cancellation", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Context input remained blocked after cancellation")
		}
		stop()
		_ = input.Close()
		_ = writer.Close()
		_ = source.Close()
	}
}

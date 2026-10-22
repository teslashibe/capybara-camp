// camp-local is the desktop app's bundled JSON/stdio adapter. It opens provider
// stores read-only and does not use the shared Camp database or credentials.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/teslashibe/capybara-camp/server/internal/config"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := config.LoadLocal()
	contextTool := len(os.Args) == 3 && os.Args[1] == "context-tool"
	if err == nil && len(os.Args) != 2 && !contextTool {
		err = fmt.Errorf("Choose a local helper action")
	}
	var result any
	if err == nil {
		app := localcli.New(cfg)
		if len(os.Args) == 2 && (os.Args[1] == "conversation" || os.Args[1] == "transfer-source") {
			var input, output *os.File
			input, err = pollableInput(os.Stdin)
			if err == nil {
				defer input.Close()
				output, err = pollableInput(os.Stdout)
			}
			if err == nil {
				defer output.Close()
				stopIO := context.AfterFunc(ctx, func() { _ = input.Close(); _ = output.Close() })
				defer stopIO()
				if os.Args[1] == "transfer-source" {
					err = app.HoldTransferSource(ctx, input, output)
				} else {
					err = app.Conversation(ctx, input, output)
				}
			}
			if err == nil {
				return
			}
		} else if contextTool {
			var input *os.File
			input, err = pollableInput(os.Stdin)
			if err == nil {
				defer input.Close()
				stopInput := context.AfterFunc(ctx, func() { _ = input.Close() })
				defer stopInput()
				err = app.ServeContext(ctx, os.Args[2], input, os.Stdout)
			}
			if err == nil {
				return
			}
		} else if os.Args[1] == "codex-activity-watch" {
			err = app.WatchCodex(ctx, os.Stdin, os.Stdout)
			if err == nil {
				return
			}
		} else {
			result, err = app.Run(ctx, os.Args[1], os.Stdin)
		}
	}
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"type": "error", "message": err.Error()})
		os.Exit(1)
	}
	if err = json.NewEncoder(os.Stdout).Encode(result); err != nil {
		os.Exit(1)
	}
}

// Standard stdin can wrap a blocking pipe without Go's poller. Closing that
// wrapper does not reliably interrupt an in-flight read on macOS. Give the
// context server its own nonblocking descriptor before starting the scanner.
func pollableInput(source *os.File) (*os.File, error) {
	fd, err := syscall.Dup(int(source.Fd()))
	if err != nil {
		return nil, fmt.Errorf("Could not open context input")
	}
	syscall.CloseOnExec(fd)
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("Could not configure context input")
	}
	return os.NewFile(uintptr(fd), "context-input"), nil
}

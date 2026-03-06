// Package builder handles compiling the project.
package builder

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// Builder runs a build command.
type Builder struct {
	cmdStr string
	logger *slog.Logger
}

// New creates a Builder for the given shell command string.
func New(cmdStr string, logger *slog.Logger) *Builder {
	return &Builder{cmdStr: cmdStr, logger: logger}
}

// Build runs the build command. It returns an error if the build fails or the
// context is cancelled. If a new change comes in mid-build, the caller cancels
// the context, which aborts this build cleanly.
func (b *Builder) Build(ctx context.Context) error {
	start := time.Now()

	args := shellSplit(b.cmdStr)
	if len(args) == 0 {
		return fmt.Errorf("empty build command")
	}

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	b.logger.Debug("running build command", "cmd", b.cmdStr)

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err() // cancelled — not a real build failure
		}
		errOut := strings.TrimSpace(stderr.String())
		if errOut != "" {
			return fmt.Errorf("build failed: %w\n%s", err, errOut)
		}
		return fmt.Errorf("build failed: %w", err)
	}

	b.logger.Info("build completed", "duration", time.Since(start).Round(time.Millisecond))
	return nil
}

// shellSplit naively splits a command string on spaces, respecting quoted sections.
// This handles simple cases like: go build -o ./bin/server ./cmd/server
// For complex shell syntax users should wrap in sh -c "...".
func shellSplit(s string) []string {
	var args []string
	var current strings.Builder
	inQuote := false
	quoteChar := rune(0)

	for _, r := range s {
		switch {
		case inQuote:
			if r == quoteChar {
				inQuote = false
			} else {
				current.WriteRune(r)
			}
		case r == '"' || r == '\'':
			inQuote = true
			quoteChar = r
		case r == ' ' || r == '\t':
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

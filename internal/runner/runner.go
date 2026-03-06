// Package runner manages the lifecycle of the server process.
package runner

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// gracefulShutdownTimeout is how long we wait for a process to exit after SIGTERM.
	gracefulShutdownTimeout = 5 * time.Second

	// crashLoopThreshold — if the server dies within this window, it's considered crashed.
	crashLoopThreshold = 2 * time.Second

	// crashLoopMaxRetries — after this many fast crashes in a row, back off.
	crashLoopMaxRetries = 3

	// crashLoopBackoff — wait this long before trying again after a crash loop.
	crashLoopBackoff = 5 * time.Second
)

// Runner manages a single server process.
type Runner struct {
	cmdStr     string
	logger     *slog.Logger
	mu         sync.Mutex
	cmd        *exec.Cmd
	stopCh     chan struct{}
	crashCount int
	lastStart  time.Time
}

// New creates a Runner for the given executable command string.
func New(cmdStr string, logger *slog.Logger) *Runner {
	return &Runner{
		cmdStr: cmdStr,
		logger: logger,
	}
}

// Stop terminates the currently running server process and all its children.
// It is safe to call even if nothing is running.
func (r *Runner) Stop() {
	r.mu.Lock()
	cmd := r.cmd
	stopCh := r.stopCh
	r.cmd = nil
	r.stopCh = nil
	r.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}

	pid := cmd.Process.Pid
	r.logger.Info("stopping server", "pid", pid)

	// Signal the log-streaming goroutine to stop
	if stopCh != nil {
		select {
		case <-stopCh: // already closed
		default:
			close(stopCh)
		}
	}

	// Try graceful shutdown first: send SIGTERM to the process group
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		r.logger.Debug("SIGTERM to process group failed, trying process directly", "error", err)
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}

	// Wait for the process to exit, or force-kill after timeout
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	select {
	case <-done:
		r.logger.Debug("server exited cleanly after SIGTERM", "pid", pid)
	case <-time.After(gracefulShutdownTimeout):
		r.logger.Warn("server did not exit after SIGTERM, sending SIGKILL", "pid", pid)
		// Kill the entire process group to catch child processes
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
			r.logger.Debug("SIGKILL to process group failed, trying process directly", "error", err)
			_ = cmd.Process.Kill()
		}
		<-done
		r.logger.Debug("server killed", "pid", pid)
	}
}

// Start launches the server process. It streams stdout/stderr in real time.
// The process is started in its own process group so children can be reaped.
func (r *Runner) Start(ctx context.Context) error {
	// Crash-loop protection
	r.mu.Lock()
	if r.crashCount >= crashLoopMaxRetries {
		elapsed := time.Since(r.lastStart)
		if elapsed < crashLoopBackoff {
			wait := crashLoopBackoff - elapsed
			r.mu.Unlock()
			r.logger.Warn("crash loop detected, backing off before restart",
				"crash_count", r.crashCount,
				"waiting", wait.Round(time.Millisecond),
			)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
			r.mu.Lock()
		}
		r.crashCount = 0
	}
	r.mu.Unlock()

	args := shellSplit(r.cmdStr)
	if len(args) == 0 {
		return fmt.Errorf("empty exec command")
	}

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)

	// Put the process in its own process group so we can kill the whole group
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Capture stdout and stderr for real-time streaming
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start process: %w", err)
	}

	startTime := time.Now()
	stopCh := make(chan struct{})

	r.mu.Lock()
	r.cmd = cmd
	r.stopCh = stopCh
	r.lastStart = startTime
	r.mu.Unlock()

	r.logger.Info("server started", "pid", cmd.Process.Pid, "cmd", r.cmdStr)

	// Stream logs in real time (non-buffered)
	go streamLogs(stdout, "stdout", r.logger, stopCh)
	go streamLogs(stderr, "stderr", r.logger, stopCh)

	// Watch for unexpected crashes in the background
	go r.watchCrash(cmd, startTime, stopCh)

	return nil
}

// watchCrash detects if the server exits on its own (not via Stop).
func (r *Runner) watchCrash(cmd *exec.Cmd, startTime time.Time, stopCh chan struct{}) {
	err := cmd.Wait()

	select {
	case <-stopCh:
		// Normal Stop() was called — not a crash
		return
	default:
	}

	uptime := time.Since(startTime)
	if err != nil {
		r.logger.Error("server exited unexpectedly",
			"error", err,
			"uptime", uptime.Round(time.Millisecond),
		)
	} else {
		r.logger.Warn("server exited with code 0 unexpectedly",
			"uptime", uptime.Round(time.Millisecond),
		)
	}

	// Track crash frequency for loop detection
	r.mu.Lock()
	if uptime < crashLoopThreshold {
		r.crashCount++
		r.logger.Warn("fast crash detected", "crash_count", r.crashCount)
	} else {
		r.crashCount = 0
	}
	r.mu.Unlock()
}

// streamLogs reads from an io.Reader and emits each line as a log message.
// Lines are written immediately — no buffering/batching.
func streamLogs(r io.Reader, stream string, logger *slog.Logger, stopCh <-chan struct{}) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		select {
		case <-stopCh:
			return
		default:
		}
		line := scanner.Text()
		// Emit server output as INFO so it's clearly distinguishable
		logger.Info("[server] "+line, "stream", stream)
	}
}

// shellSplit naively splits a shell command string respecting quotes.
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

// Pid returns the PID of the currently running process, or 0 if none.
func (r *Runner) Pid() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil && r.cmd.Process != nil {
		return r.cmd.Process.Pid
	}
	return 0
}

// IsRunning returns true if there's currently a live server process.
func (r *Runner) IsRunning() bool {
	return r.Pid() != 0
}

// KillAll forcefully kills the process group — used when SIGTERM doesn't work.
func (r *Runner) KillAll() {
	r.mu.Lock()
	cmd := r.cmd
	r.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	r.logger.Warn("force-killing process group", "pgid", pid)
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

// freePort checks that the port used by a process was actually freed.
// This is a placeholder for tests/diagnostics and uses /proc on Linux.
func freePort(pid int) bool {
	// On Linux, we can check /proc/<pid>/net/tcp
	// If the /proc entry is gone, process is dead and port is freed.
	path := fmt.Sprintf("/proc/%d", pid)
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

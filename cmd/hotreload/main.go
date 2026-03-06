package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/dev-sarthak7/hotreload/internal/builder"
	"github.com/dev-sarthak7/hotreload/internal/debounce"
	"github.com/dev-sarthak7/hotreload/internal/runner"
	"github.com/dev-sarthak7/hotreload/internal/watcher"
)

const version = "1.0.0"

func main() {
	root := flag.String("root", ".", "Directory to watch for file changes")
	buildCmd := flag.String("build", "", "Command to build the project (required)")
	execCmd := flag.String("exec", "", "Command to run the built binary (required)")
	debounceMs := flag.Int("debounce", 300, "Milliseconds to wait after the last file change before rebuilding")
	verbose := flag.Bool("verbose", false, "Enable debug logging")
	ver := flag.Bool("version", false, "Print version and exit")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: hotreload --root <dir> --build \"<cmd>\" --exec \"<cmd>\"\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *ver {
		fmt.Printf("hotreload v%s\n", version)
		os.Exit(0)
	}

	if *buildCmd == "" || *execCmd == "" {
		fmt.Fprintln(os.Stderr, "Error: --build and --exec are required\n")
		flag.Usage()
		os.Exit(1)
	}

	// Structured logger via log/slog
	logLevel := slog.LevelInfo
	if *verbose {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	slog.Info("hotreload starting",
		"version", version,
		"root", *root,
		"build", *buildCmd,
		"exec", *execCmd,
		"debounce_ms", *debounceMs,
	)

	if _, err := os.Stat(*root); os.IsNotExist(err) {
		slog.Error("root directory does not exist", "path", *root)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Graceful shutdown on SIGINT / SIGTERM
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Info("signal received, shutting down", "signal", sig)
		cancel()
	}()

	b := builder.New(*buildCmd, logger)
	r := runner.New(*execCmd, logger)
	d := debounce.New(*debounceMs)

	w, err := watcher.New(*root, logger)
	if err != nil {
		slog.Error("failed to start file watcher", "error", err)
		os.Exit(1)
	}
	defer w.Close()

	if err := runEngine(ctx, w, b, r, d); err != nil && err != context.Canceled {
		slog.Error("engine error", "error", err)
		os.Exit(1)
	}

	slog.Info("hotreload stopped")
}

// runEngine is the main loop.
// It immediately triggers one build+run cycle on startup, then waits for
// debounced file-change events to trigger subsequent cycles.
//
// Each cycle gets its own cancellable child context. When a new file change
// arrives, we cancel the current cycle's context (aborting any in-progress
// build) and start a fresh cycle — so we only ever build the latest state.
func runEngine(
	ctx context.Context,
	w *watcher.Watcher,
	b *builder.Builder,
	r *runner.Runner,
	d *debounce.Debouncer,
) error {
	// cycleCancel cancels the currently running build/start goroutine.
	var cycleCancel context.CancelFunc = func() {} // no-op initially

	doRebuild := func() {
		// Abort any in-progress build
		cycleCancel()

		// Stop the running server before we rebuild
		r.Stop()

		var cycleCtx context.Context
		cycleCtx, cycleCancel = context.WithCancel(ctx)

		go func(cctx context.Context) {
			slog.Info("► build started")
			if err := b.Build(cctx); err != nil {
				if cctx.Err() != nil {
					slog.Debug("build cancelled (new change arrived or shutdown)")
					return
				}
				slog.Error("✗ build failed", "error", err)
				return
			}
			slog.Info("✔ build succeeded")

			if err := r.Start(cctx); err != nil {
				if cctx.Err() != nil {
					return
				}
				slog.Error("✗ failed to start server", "error", err)
			}
		}(cycleCtx)
	}

	// First build fires immediately — no waiting for a file change.
	doRebuild()

	changeCh := d.Wrap(ctx, w.Changes())

	for {
		select {
		case <-ctx.Done():
			cycleCancel()
			r.Stop()
			return ctx.Err()

		case _, ok := <-changeCh:
			if !ok {
				r.Stop()
				return nil
			}
			slog.Info("file change detected, rebuilding")
			doRebuild()
		}
	}
}

# hotreload

A fast, robust CLI tool for Go development that watches your project for file changes and automatically rebuilds and restarts your server.

## Features

- **Instant startup** — triggers a build immediately on launch, no need to wait for a file change
- **Recursive watching** — monitors all subdirectories, not just the root
- **Dynamic directory detection** — automatically watches newly created folders
- **Smart debouncing** — collapses rapid save events (e.g. editor autosave bursts) into a single rebuild
- **Real-time log streaming** — server logs appear immediately, not buffered and dumped at exit
- **Robust process management** — sends SIGTERM to the entire process group, falls back to SIGKILL for stubborn processes
- **Crash-loop protection** — backs off automatically if the server crashes repeatedly on startup
- **Intelligent file filtering** — ignores `.git/`, `node_modules/`, swap files, build artifacts, and editor temp files
- **OS watch-limit aware** — non-fatal on directories that can't be watched (skips with a warning)

## Installation

```bash
# Clone and build
git clone https://github.com/yourusername/hotreload.git
cd hotreload
make build

# Or install directly to $GOPATH/bin
make install
```

## Usage

```bash
hotreload --root <project-folder> --build "<build-command>" --exec "<run-command>"
```

### Example

```bash
hotreload \
  --root ./myproject \
  --build "go build -o ./bin/server ./cmd/server" \
  --exec "./bin/server"
```

### Flags

| Flag | Default | Description |
|---|---|---|
| `--root` | `.` | Directory to watch recursively |
| `--build` | *(required)* | Command to run to build the project |
| `--exec` | *(required)* | Command to run the built binary |
| `--debounce` | `300` | Milliseconds to wait after the last file change before triggering a rebuild |
| `--verbose` | `false` | Enable debug-level logging |
| `--version` | | Print version and exit |

## Demo

```bash
# Build hotreload and run it against the included test server
make demo
```

Then in another terminal:
```bash
# Hit the server
curl localhost:8080/

# Try editing testserver/cmd/server/main.go 
# (e.g. change the version constant or add a route)
# The server restarts automatically within ~1-2 seconds
```

## Running Tests

```bash
make test
```

## Architecture

```
hotreload/
├── cmd/hotreload/
│   └── main.go          # CLI entrypoint, wires components together
├── internal/
│   ├── watcher/         # Recursive fsnotify wrapper with filtering
│   ├── debounce/        # Event collapsing with configurable delay
│   ├── builder/         # Build command executor with context cancellation
│   └── runner/          # Process lifecycle: start, stream logs, stop
└── testserver/          # Demo HTTP server for testing
```

### How it works

1. **Watcher** walks the root directory, registers all non-ignored subdirectories with `fsnotify`, and emits `ChangedFile` events. It also watches for new directories being created at runtime.

2. **Debouncer** wraps the raw change channel. Each incoming event resets a timer. Only when the timer fires (quiet period elapsed) does it emit a single trigger — preventing a burst of 10 editor events from spawning 10 simultaneous builds.

3. **Builder** runs the build command using `exec.CommandContext`, so it respects context cancellation. If a new file change arrives mid-build, the engine cancels the running build and starts a fresh one.

4. **Runner** starts the server in its own process group (`Setpgid: true`), pipes stdout/stderr to the logger line-by-line (real-time, not buffered), and on `Stop()` sends `SIGTERM` to the process group. If the process doesn't exit within 5 seconds, it escalates to `SIGKILL` on the whole group — ensuring child processes don't become orphans.

5. **Crash-loop detection**: if the server exits within 2 seconds of starting, it's counted as a fast crash. After 3 consecutive fast crashes, the runner backs off for 5 seconds before trying again.

### Key design decisions

- **Process group killing** (`kill(-pgid, SIGTERM)`) instead of just `cmd.Process.Signal()` — this ensures any child processes spawned by the server are also terminated, freeing ports and resources.
- **Debounce resets on every event** — this correctly handles editors that write files in multiple steps (temp file → rename).
- **Context-based build cancellation** — if the user saves again while a build is running, we cancel the old build immediately rather than queueing. Only the latest state gets built.
- **Non-blocking change channel** — if the debouncer's output channel is full, events are dropped. This is safe because the debouncer coalesces all events into one trigger anyway.

## Ground Rules

- Does **not** use `air`, `realize`, `reflex`, or any existing hot-reload framework.
- Uses `fsnotify` only as the raw filesystem event source.
- Uses `log/slog` from the Go standard library for all logging.

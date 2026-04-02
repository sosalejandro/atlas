# Web Terminal — Concept Draft

**Status:** Concept only — not scheduled for implementation
**Date:** 2026-04-02

---

## Idea

Embed a real terminal in the `testreg serve` dashboard so developers can run commands without switching windows. The terminal runs as the same OS user at the project root — it's a local tool, not a remote shell.

## Architecture

```
Browser (xterm.js)          Go Server              OS
┌──────────────┐     WS     ┌──────────┐    PTY    ┌──────┐
│ Terminal UI   │◄──────────►│ Handler  │◄─────────►│ bash │
│ (xterm.js)   │  bidirect  │ pty.Start│  stdin/   │      │
│ renders ANSI  │  stream    │          │  stdout   │      │
└──────────────┘            └──────────┘            └──────┘
```

- **xterm.js** (~400KB) renders the terminal in-browser. Same rendering engine VS Code uses.
- **Go server** spawns a PTY via `github.com/creack/pty` at the project root.
- **WebSocket** connects them bidirectionally — keystrokes up, output down.
- Shell runs as the same user who started `testreg serve`, inheriting their environment.

## Server-side sketch

```go
import (
    "os/exec"
    "github.com/creack/pty"
    "github.com/gorilla/websocket"
)

func (s *Server) handleTerminalWS(w http.ResponseWriter, r *http.Request) {
    conn, _ := upgrader.Upgrade(w, r, nil)

    cmd := exec.Command("bash")
    cmd.Dir = s.config.ProjectRoot
    cmd.Env = os.Environ()
    ptmx, _ := pty.Start(cmd)

    // browser → PTY (keystrokes)
    go func() {
        for {
            _, msg, err := conn.ReadMessage()
            if err != nil { break }
            ptmx.Write(msg)
        }
    }()

    // PTY → browser (output)
    go func() {
        buf := make([]byte, 4096)
        for {
            n, err := ptmx.Read(buf)
            if err != nil { break }
            conn.WriteMessage(websocket.BinaryMessage, buf[:n])
        }
    }()
}
```

## Client-side sketch

```html
<script src="/static/xterm.min.js"></script>
<link rel="stylesheet" href="/static/xterm.css">

<div id="terminal"></div>
<script>
  const term = new Terminal({
    fontSize: 13,
    fontFamily: 'JetBrains Mono, ui-monospace, monospace',
    theme: {
      background: '#0d1117',
      foreground: '#e6edf3',
      cursor: '#58a6ff',
    }
  });
  term.open(document.getElementById('terminal'));

  const ws = new WebSocket(`ws://${location.host}/terminal/ws`);
  ws.binaryType = 'arraybuffer';
  ws.onmessage = (e) => term.write(new Uint8Array(e.data));
  term.onData((data) => ws.send(new TextEncoder().encode(data)));
</script>
```

## Dependencies

| Component | Size | Purpose |
|-----------|------|---------|
| `xterm.js` + `xterm.css` | ~400KB | Terminal renderer |
| `@xterm/addon-fit` | ~5KB | Auto-resize terminal to container |
| `@xterm/addon-web-links` | ~3KB | Clickable URLs in output |
| `github.com/creack/pty` | tiny | Go PTY wrapper (unix only) |
| `github.com/gorilla/websocket` | tiny | WebSocket upgrade handler |

All embeddable via `go:embed` — no npm build step needed. Bundle xterm.js as a static asset.

## UI placement options

### Option A: Bottom panel (VS Code style)
- Collapsed by default, 32px drag handle at bottom
- Drag up to open, resizable height
- Persists across page navigation
- Most familiar to developers

### Option B: Sidebar page
- "Terminal" as a navigation item
- Full-height terminal when selected
- Simpler implementation but requires page switch

### Option C: Slide-up drawer
- Floating button in bottom-right corner
- Click to open terminal overlay
- Can overlay any page without navigation

### Option D: Per-command integration
- Buttons like "Run in terminal" next to testreg commands
- Clicking pre-fills the terminal with the command
- Example: "Run tests" button on a feature detail sends `testreg run auth.login` to the terminal

**Recommendation:** Option A (bottom panel) + Option D (pre-filled commands). This matches developer expectations and adds unique value by connecting the GUI actions to the terminal.

## Pre-filled command integration examples

```
Feature detail panel → [Run tests]     → terminal: testreg run auth.login
Feature detail panel → [Trace]         → terminal: testreg trace auth.login
Gaps page            → [Fix with AI]   → terminal: testreg gaps --feature auth.login --format prompt
Sprint page          → [Export]        → terminal: testreg gaps --priority critical -n 5 --format prompt
Diagnose page        → [Diagnose]      → terminal: testreg diagnose auth.login --symptom "401"
```

The GUI becomes a visual interface that dispatches CLI commands — keeping the terminal as the source of truth.

## Terminal resize handling

```go
// Server: handle resize messages
type resizeMsg struct {
    Cols uint16 `json:"cols"`
    Rows uint16 `json:"rows"`
}

// When browser terminal resizes, send new dimensions
pty.Setsize(ptmx, &pty.Winsize{Cols: msg.Cols, Rows: msg.Rows})
```

```js
// Client: fit addon auto-resizes and notifies server
import { FitAddon } from '@xterm/addon-fit';
const fitAddon = new FitAddon();
term.loadAddon(fitAddon);
fitAddon.fit();

new ResizeObserver(() => {
  fitAddon.fit();
  ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
}).observe(document.getElementById('terminal'));
```

## Security notes

- **Local only:** `testreg serve` binds to `localhost` by default. The terminal runs as the launching user — same permissions as their regular terminal.
- **If ever network-accessible:** Would need authentication (API key, session token, or OAuth). An open WebSocket terminal = remote code execution.
- **No sandboxing:** The terminal has full access to the filesystem. This is intentional — it's a developer tool, not a hosted service.

## Platform considerations

- **Linux / macOS:** `github.com/creack/pty` works natively with PTY.
- **Windows:** PTY support via `conpty` (Windows 10+). Would need `github.com/UserExistsError/conpty` or similar. Could fall back to `cmd.exe` or PowerShell. Not a priority for v1.

## Open questions

- Should there be multiple terminal tabs? Or just one?
- Should terminal history persist across `testreg serve` restarts?
- Should the terminal auto-run `testreg scan` on startup?
- Should output from GUI-triggered scans also appear in the terminal?

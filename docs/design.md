# rex-amq-ui design

rex-amq-ui shows one Rex session's crew to the human in a floating layer of that session. This document covers how its parts fit together, what it will not do, and the Rex behaviour it relies on. The README covers installing and using it.

## Boundaries

The adapter (`rex-amq-adapter`) is the single source of truth for agents, mail, and session views. It owns every file under `~/.local/state/rex-amq-adapter/`, and rex-amq-ui reads none of them. It reads only the NDJSON that `rex-amq-adapter panel --json` writes to stdout. It acts only on its own floating layer and on the human's focus, through Rex's control API, and writes only the Graph tab's frame files, under `/tmp`.

What rex-amq-ui never does:

- It never sends, reads, or marks AMQ mail. It leaves no read receipts and never moves a message out of `inbox/new`. The Mail tab shows envelopes and only the excerpts that the adapter's view already authorises.
- It never starts, wakes, adopts, or retires an agent.
- It never changes a repository's working tree or Git state.

The adapter enforces the other side of this boundary. `panel --json` refuses an agent's pane, an agent's process tree, and `ME_UNATTENDED=1`, so only a human's terminal (the panel's own block, or the plugin's command run by the Rex server) can read it.

## Parts

```mermaid
flowchart LR
  subgraph Adapter["rex-amq-adapter"]
    Panel["panel --json<br/>(NDJSON, schema 1)"]
  end
  subgraph Rex["Rex server (control API r13)"]
    Lua["init.lua plugin<br/>amqui_toggle · amqui_waiting"]
    API["rex.control.v1<br/>WebSocket on the Unix socket"]
  end
  subgraph Bin["rex-amq-ui binary"]
    Cmd["cmd/rex-amq-ui<br/>panel · open · waiting"]
    A["internal/adapter"]
    R["internal/rexctl"]
    L["internal/layer"]
    P["internal/apc"]
    U["internal/ui + internal/theme"]
    G["internal/graph · raster<br/>kitty · pixelmouse"]
  end
  Panel -- stdout --> A
  A --> U
  Lua -- "rex.exec: open, waiting --json" --> Cmd
  Lua -- "block.method write: APC" --> P
  Lua -- "set_layer_state · move_block · float_block · focus_block" --> API
  P --> U
  U --> L --> R <--> API
  U --> G
  G -- "Kitty t=t frames, pixel mouse<br/>(the panel's own terminal)" --> Rex
```

| Package | Owns |
|---|---|
| `internal/adapter` | Finding the adapter binary (`REX_AMQ_ADAPTER_BIN`, then Homebrew, then `PATH`). Running `panel --json`, and restarting it with backoff (250 ms doubling to 10 s) when it ends, fails, writes a line that does not decode, or skips a sequence number. Decoding frames as a tolerant reader: unknown fields are ignored, and an unknown frame type or entity decodes as `Unknown`. Folding frames into `State`, an immutable projection that a snapshot replaces. |
| `internal/rexctl` | A client of `rex.control.v1`: one WebSocket on the server's Unix socket, with requests, responses, and events. The panel calls Rex in 0.1–3 ms this way, where starting the `rex` CLI for each call would take about 76 ms. `rexctltest` is a fake server that validates every request against the method schemas recorded from the real server (`rexctltest/schemas`). |
| `internal/layer` | The facts about the panel's layer: its label (`rex-amq-ui`), its two sizes (Drawer `[0.64,0.02,0.35,0.96]`, Board `[0.06,0.06,0.88,0.88]`), and the order of the calls that leave it. Creating or restoring the layer (`Open`, used by `open`). From inside the panel: zoom, minimize, jump to a pane or to another session, and follow focus, owner, size, and the layer id (`Panel.Watch`). |
| `internal/apc` | Taking the private `ESC _ amqui;… ESC \` sequences out of the panel's stdin, wherever reads split them. A 32-byte cap bounds a sequence. A lone `ESC` that waits more than 40 ms counts as the Esc key. |
| `internal/ui`, `internal/theme` | The Bubble Tea model: it folds stream updates, layer states, APC commands, and keys into one state, and renders the five tabs. Its only effects are on its own layer, on opening a hand-back for the human to read, and on the Graph tab's image (`graph.go`). `theme` holds the Everforest palette and the palette taken from the terminal's real colours. |
| `internal/graph` | The Graph tab's picture: the replay cursor's view of the mail, the selection, pulses, and the animation clock (`Model`); nodes and edges placed at a pixel size (`Scene`); drawing a scene (`Paint`). |
| `internal/raster` | Anti-aliased shapes and Go Mono text drawn into an opaque RGBA canvas in pure Go, sized from the terminal's cell size. `rastertest` compares a canvas with a reference PNG. |
| `internal/kitty` | Sending frames with the Kitty graphics protocol (`Transport`) and pacing them at 24–30 fps while something moves (`Scheduler`). |
| `internal/pixelmouse` | The escape sequences that turn SGR pixel mouse reports on and off, and the hit test from a pointer's pixel to a node. Bubble Tea parses the reports. |
| `cmd/rex-amq-ui` | The three commands: the panel, `open`, and `waiting`. The panel's terminal writer, shared under one lock by the renderer and the Graph tab, and its exit path, which tears the Graph image down. |
| `lua/rex-amq-ui/init.lua` | The Rex plugin: the two actions and their keys. |

The policy lives in Go and the Lua stays plumbing. The plugin never decides who waits or how a layer is created. It asks the binary (`waiting --json`, `open`) and makes only the few Rex calls that a key press needs to be instant: restoring or minimizing an existing layer, and moving it between tabs. It finds the panel by the same rule as `layer.Open`: a floating-layer block labelled `rex-amq-ui`, preferring the active tab's. If that rule changes, change both. The plugin adds one fallback that `open` lacks: a block labelled `rex-amq-ui` in a window's tiled layout. A cross-tab move that broke off between `move_block` and `float_block` leaves the panel there, and the next toggle floats it again instead of opening a second panel.

The plugin undoes a transition it leaves half done. If Rex refuses to minimize a layer after the plugin paused the panel, the plugin resumes the panel, because a paused panel on a visible layer would look frozen and never see a minimized-to-normal edge. With one waiter, it focuses the pane only after it has read the view and minimized the panel; if either fails, it focuses nothing, which is the order the panel's own `Enter` uses.

## Data flow

1. **Opening.** `rex-amq-ui open` attaches to the session and looks for the panel. When there is none, it measures a tiled block of the active tab (`com.superlogical.terminal.size`), works out the terminal size the layer's bounds will give, and creates the layer with `session.new_layer` and `options.size`. Without that size the first frame would be 80×24. It then calls `session.disown_block`, so the Rex app sizes the block from then on. The panel's command line is `rex-amq-ui [--adapter-bin P] --session S --focus <pane> [--tab T] [--board]`, run with `shell: "none"`.
2. **Streaming.** The panel runs the adapter and folds its frames. A restart's snapshot resynchronises everything. While the adapter is down, the header shows `◌ Reconnecting` and the last state stays on screen.
3. **Following Rex.** `Panel.Watch` keeps a control connection, reconnects with a fresh `session.attach`, and turns `session_view_changed` (fetching a newer view when the revision goes up) and `block_owner_changed` into a `layer.State`: the pane the human focused outside the panel (`▶`), the block's owner, whether the layer is minimized, and its size. After a move, Watch finds the panel's new layer id in the view on its own. While the control connection is down, the header shows `◌ Rex` and the panel's actions say it is not connected. Without `REX_SERVER` or a session, the panel still shows the stream.
4. **Acting.** `Enter` on a member minimizes the panel first and then calls `session.focus_block`, which also switches tabs. `Enter` on an Epic first calls `client.perform` of `session.select` on the Rex app, and minimizes only if that succeeds. With Remote Control off the app answers `refused`, and the panel stays up with a footer hint.
5. **The plugin.** See the README's toggle table. `amqui_waiting` runs `rex-amq-ui waiting --json`, which takes only the adapter's first snapshot (5 s at most, with no retry, since a key press is waiting on it) and always stops the adapter before it exits.

## Painting and the transparent-cell problem

A Rex floating layer shows the terminal underneath through every cell drawn with the default background. Every cell the panel emits therefore carries an explicit background:

- each span is rendered with a background;
- each line is padded to the full width with the line's background;
- each frame is padded to the full height;
- styles are never nested, because an inner reset would end the outer background;
- Lipgloss helpers that pad with unstyled spaces are never used.

`line` and `canvas` in `internal/ui/paint.go` are the only ways a view emits text. The paint guard (`TestPaintNoHoles` and the other `holes_test.go` tests) scans every cell of the model's frame across fixtures, tabs, key states, and sizes down to 0×0. Its detector has its own negative test with planted holes.

The guard proves the model's frame, not the terminal. Bubble Tea's renderer clears line tails with EL/ECH while the background is set, so those cells stay opaque only if Rex's terminal does BCE (background colour erase). Rex blocks run with `TERM=xterm-256color`, whose terminfo declares `bce`. That fact is located, not observed on screen. The alt-screen clear at startup runs before the first SGR, which may show one transparent frame.

The program forces TrueColor (`colorprofile.TrueColor`), because `NO_COLOR` or a dumb `TERM` would otherwise strip the backgrounds that keep the panel opaque.

**Colours.** Rex drops OSC 10/11 queries from a block that no app owns yet, and a freshly created block is owned by the launcher for a moment. The panel therefore starts in Everforest dark, and asks for the terminal's real foreground and background only once `block_owner_changed` names an owner. It asks again for each new owner until the terminal answers.

## Pause and resume

A minimized layer keeps its process running. Drawing into it would waste work, and on the Graph tab would leave an image behind. The panel pauses on APC `pause` (which the plugin sends before minimizing), on its own minimize, and when it sees its layer minimized in the view. While paused it folds frames, writes nothing, and asks for no colours. It resumes on APC `resume`, on its layer going from minimized to normal (an edge, not a level), or on any key, and it draws the current frame at once. `Q` exits the process.

Bubble Tea's renderer repaints on every resize, so the program's message filter (`ui.Filter`) holds a resize that arrives while the panel is paused and hands the model the size instead. The model passes the size on to the renderer when it resumes. Pausing also counts as leaving the Graph tab: the image is deleted, pixel mouse reports are turned off, and the frame loop stops.

## Graph renderer

The Graph tab draws into the cell rectangle `ui.Model.GraphRegion()` gives (`Y=3`, `H=height−6`, between the replay bar and the legend). The text frame paints that rectangle with the background, and the image is placed over it. `ui/graph.go` (`graphTab`) ties the parts together and runs only inside `Update`, so one goroutine changes it.

- **Cell size.** Before the Rex app claims a block, Rex's built-in terminal answers `CSI 16 t` with a synthetic 8×16 cell. The panel therefore asks Rex for its block's `cell_width_px` and `cell_height_px` (`layer.Panel.CellSize`, over the control connection). It asks when the tab starts showing, on a resize, when the block's owner changes, and when the control connection comes back. It never asks again just because the last answer failed, so outside Rex it does not loop. Without a cell size it draws no image.
- **Drawing.** `graph.Model` takes each new `adapter.State`. It places the lead at the top of an ellipse and the others clockwise, with up to 6 live members and a `+N` node for the rest, plus up to 2 retired members while they are shown. Edges are quadratic curves with one lane per direction, routed around other nodes, and their width grows with the message count. Styles: drained is a solid line, queued and bounced or dead-lettered mail are dashed, and an open obligation is a solid line over a translucent glow. Colours come from the panel's palette, so the image matches the cells around it. `raster` draws with signed distance fields and Go Mono text, compiled into the binary. A 2254×1372 px frame takes about 7 ms to draw and send, file included. `testdata/graph/*.png` and `testdata/raster/*.png` are the reference frames at 9×22 and 23×49 px cells; the text legend lives in `testdata/golden/graph_*.golden`.
- **Pacing.** A frame is due when the picture changed (new state, a replay step, a selection, a new cell size or colour) or moves on its own (a working member breathes; a new message's pulse runs for 1.2 s). `kitty.Scheduler` runs a frame loop at 30 fps only while one is due, and draws a last frame when the motion stops. An idle graph sends nothing. Each tick fires once, and a tick from a stopped loop changes nothing. A new showing, and a resize, waits 50 ms before its first frame, because the renderer's erase-and-repaint after a resize or a resume deletes images and can land after the frame.
- **Kitty transport.** `kitty.Transport.Show` writes the frame to `/tmp/tty-graphics-protocol-rex-amq-ui-<pid>-<seq>.rgba` and sends, in one write, `a=T,f=32,t=t,i=<1|2>,p=1,C=1,q=2` at the region's top-left cell (cursor saved and restored around it), then `a=d,d=I,i=<old>,q=2`. The two image ids alternate, so the new frame is up before the old one goes and nothing flickers. Leaving the tab, pausing, or minimizing sends `a=d,d=A,q=2`. At most 3 frame files may wait unread; past that `Show` refuses with `ErrBacklog`, the footer says the terminal has not read the last frames, and the panel tries again at the frame rate without painting.
- **Write order.** The renderer and the Graph tab write to the terminal through one locked writer (`lockOutput` in `cmd/rex-amq-ui`). Every graphics command is written synchronously from `Update`, so a teardown cannot be overtaken by the frame before it, or land after the next showing's frame. What the tab believes is on screen changes only after a write succeeds. A write cut off part way counts as possibly on screen, so the teardown stays owed and is retried on the next `Update`, and the next write starts with CAN ST to get the terminal's parser out of a half-sent sequence.
- **Mouse.** Before its first frame the tab turns on SGR pixel mouse reports (`?1002h ?1006h ?1016h`); Bubble Tea parses them into click messages. `pixelmouse.HitTest` subtracts the region's pixel origin (its rows times the real cell height) and finds the node under the pointer, the topmost when nodes overlap.
- **Exit.** `runPanel` ends through `endPanel`, which calls `Model.CloseGraphics` after Bubble Tea's own terminal reset: it deletes the image, turns pixel mouse reports off, and removes this process's unread frame files. A panic still runs it and stays an error; only the caller's cancellation counts as a normal end.

**Not seen on Rex yet.** The Graph tab is tested headless, against a fake Kitty terminal that keeps screen state. These Rex behaviours it relies on have not been observed:

- whether text written over the region, or an erase, deletes the image (the 50 ms settle exists for this, and no test can see it);
- whether Rex deletes a `t=t` file after reading it, as the Kitty protocol says; if it does not, the 3-file budget stalls the graph after three frames, and the exit sweep removes the files;
- whether the exit clear, written after the program leaves the alternate screen, reaches images placed in it;
- whether mouse pixels and `cell_*_px` use the same unit (device pixels on Retina);
- whether SGR 1016 coordinates are 1-based, as Bubble Tea assumes.

**Known risks.**

- A terminal that stops reading the panel's output blocks `Update` on its next graphics write, and the panel stalls until the terminal drains. Rex reads every block's pty continuously, minimized layers included. A writer goroutine with a queue would avoid the stall at the cost of the write-order guarantee above.
- While something animates, a Retina Board writes about 370 MB/s of frame files to `/tmp` at 30 fps. An idle graph writes nothing.
- `TestGraphTabProgramPanicCleansUp` skips under `-race`: Bubble Tea v2.1.1 closes its cancel reader on a panic without waiting for its read loop, which the race detector flags about once in ten runs. `TestGraphTabEndPanelKeepsPanic` covers the panel's own part under `-race`.

## Rex control-protocol notes

These were observed against Rex 0.1.0-preview.18 (API revision 13). Each one is marked with how it was checked.

- **`session.set_layer_state`.** `focus: true` with `normal` raises the layer, focuses its last-focused block, and makes its window active, all in one revision. Minimizing a layer that has focus returns focus to the layer used before it (from the schema notes; reproduced by `integration.sh`).
- **Moving between tabs.** There is no single call that moves a floating layer to another window. `session.move_block` places the panel's block beside a tiled block of the active tab (the anchor must be in a window's layout), and `session.float_block` floats it again at the old layer's bounds. `float_block` does not focus, so `set_layer_state normal, focus` follows it. The old tab is left with no empty floating layer (reproduced).
- **`Q`.** The panel's terminal uses Rex's default `exit.on_completion`, so when the panel exits, Rex closes its block, and the floating layer goes with it. The next toggle opens a new panel; there is never an empty layer to clean up (reproduced).
- **APC delivery.** `block.method write` with `{"data": "\u001b_amqui;tab:mail\u001b\\"}` reaches the panel's stdin. The panel switches tabs, and none of the bytes show on screen (reproduced).
- **Running the binary from Lua.** Rex's Lua raises on `io.popen` and `os.execute`. `rex.exec(argv, opts)` runs a command and returns `{ok, code, stdout, stderr, timed_out}`, or `nil, err` when the command could not start. `rex.call` returns `nil, err` and never raises. A config action has 30 seconds, and the server keeps handling keys while it waits (from the Rex Lua reference). The plugin calls both through `pcall` anyway, and guards each action as a whole.
- **The plugin's environment.** `rex.exec` children inherit the Rex server's environment. On est9's machine that includes `REX_SERVER` and a `PATH` with `/opt/homebrew/bin` and `~/.local/bin`, but not the Go bin directory. The plugin therefore passes `--session` explicitly and resolves `rex-amq-ui` through the login shell. This was read from the running server's environment (located). `integration.sh` runs the plugin through `rex do`, so it does not cover the server's own environment.
- **No toast.** Neither Rex's Lua nor the Mac client actions can show a message, so failures go to `rex.log` and to the action's result.

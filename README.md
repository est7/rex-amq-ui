# rex-amq-ui

A floating Crew and Cockpit panel for a [Rex](https://www.superlogical.com/rex) session. Press `Cmd+Shift+A` to see who is in the session, what they are doing, the mail between them, and what waits on you. Press `Cmd+Shift+U` to jump straight to the agent that waits.

It reads the session from `rex-amq-adapter stream panel` and drives its own Rex floating layer over Rex's control API. It only reads: it never sends, reads, or marks mail, never starts, wakes, adopts, or retires an agent, and never touches Git.

## What it is

Two pieces:

- **The panel**: the `rex-amq-ui` binary, a terminal UI that runs in a floating layer of the session's active tab. It sits on the right as a Drawer (35% of the width) or in the middle as a Board (88%). `z` switches between the two.
- **The Rex plugin**: `lua/rex-amq-ui/init.lua`, loaded from your Rex `init.lua`. It adds two actions, `amqui_toggle` and `amqui_waiting`, and binds them to keys.

The panel has five tabs:

| Tab | What it shows |
|---|---|
| **Overview** | System alerts first: every member waiting on you, with a turn error, or stalled; bounced and dead-lettered mail; open obligations (`owed_by → owed_to`). Then the narrator's summary, the lifecycle, and the latest hand-back as a card with its *Blocked on me* items picked out. `o` opens the full hand-back. |
| **Members** | The lead first, then *Active*, *Standing by*, and a folded *Retired* group (`r`). Each row has its number (`1`–`9`), `▶` on the pane you last focused, a status dot, `role(handle)`, model and effort, its tab (Board only), its task, and unread mail. `Enter` focuses the member's pane and hides the panel. |
| **Mail** | Every message in time order: `from → to`, age, state (`queued`, `drained`, `bounced`, `dead_lettered`), subject. `/` searches subjects, `p` shows only the selected member's mail, `f` follows new mail, `Space` expands the excerpt you are allowed to see. |
| **Graph** | A picture of who mails whom, drawn as an image: members around an ellipse, mail as arrows between them. A replay bar above it (`[` `]` step through messages, `L` back to Live, `h` hides retired members) and a legend below. Click a member to select it; click it again to go to its pane. See [Graph](#graph). |
| **Epics** | Only in a session with a live personal agent, where it is the first tab: every linked Epic with its lifecycle, pending hand-back, alert count, and the narrator's latest line. `Enter` switches Rex to that Epic's session. |

## Requirements

- macOS and Rex with control API revision 13 (tested with Rex 0.1.0-preview.18).
- `rex-amq-adapter` recent enough to have `stream panel` (its v:2 command set). The panel looks for it in this order: `$REX_AMQ_ADAPTER_BIN`, then `/opt/homebrew/opt/rex-amq-adapter/bin/rex-amq-adapter`, then `PATH`. `--adapter-bin` (or the plugin's `adapter_bin` option) overrides all three. When the adapter refuses to stream (its guard turns away an agent's pane, or it does not know an argument), the panel shows the adapter's error code, message, and suggested next command while it waits to retry, and `waiting` prints them on stderr.
- Go 1.27 to build it.
- For the Graph tab, a terminal that reads Kitty graphics from temporary files (`t=t`) and sends SGR pixel mouse reports (mode 1016) for clicks. Rex's terminal is meant to do both; the Graph tab has not yet been watched on Rex (see [docs/design.md](docs/design.md#graph-renderer)).
- Optional: `md-viewer` to read a full hand-back (`o`). Without it the panel opens a read-only copy in `$EDITOR`.

## How to install

Install from Homebrew (pulls in `est7/tap/rex-amq-adapter` as well):

```sh
brew install est7/tap/rex-amq-ui
```

Then add the plugin to your Rex config (`~/.config/rex/init.lua`):

```lua
dofile("/opt/homebrew/opt/rex-amq-ui/share/rex-amq-ui/init.lua").setup{ toggle_key = "cmd+shift+a", waiting_key = "cmd+shift+u" }
```

Or build from a local checkout:

```sh
go install ./cmd/rex-amq-ui      # puts rex-amq-ui in $(go env GOPATH)/bin
rex-amq-ui --version
```

and point `dofile(...)` at `<checkout>/lua/rex-amq-ui/init.lua`.

Check and reload the config:

```sh
rex config check && rex config reload
```

Check first that the two keys are free (`rex keymap`). To jump to another Epic's session from the Epics tab, turn on **Rex Settings > Rex Server > Remote Control**. Without it Rex refuses the switch, and the panel says so in its footer.

To uninstall, remove the `dofile` line and reload.

## How to configure

Every option is optional. These are the defaults:

```lua
dofile(".../rex-amq-ui/lua/rex-amq-ui/init.lua").setup{
  toggle_key = "cmd+shift+a",  -- false to bind "amqui_toggle" yourself
  waiting_key = "cmd+shift+u", -- false to bind "amqui_waiting" yourself
  bin = false,                 -- path to rex-amq-ui; else $REX_AMQ_UI_BIN, else your login shell's PATH
  adapter_bin = false,         -- path to rex-amq-adapter, passed to the panel as --adapter-bin
  board = false,               -- open new panels as a Board instead of a Drawer
}
```

An unknown option or a value of the wrong type stops `setup` with a `rex-amq-ui:` message, which `rex config check` shows.

The plugin finds `rex-amq-ui` through your login shell (`$SHELL -l -c 'command -v rex-amq-ui'`) once per config load, because the Rex server's own `PATH` usually lacks `$(go env GOPATH)/bin`.

## Keys

| Where | Key | What it does |
|---|---|---|
| Rex (plugin) | `Cmd+Shift+A` | `amqui_toggle`: show the panel in the current tab, or hide it when it has focus (see below) |
| Rex (plugin) | `Cmd+Shift+U` | `amqui_waiting`: with one member waiting on you, focus its pane; with several, show the panel on Overview |
| Panel | `Tab` / `Shift+Tab` | Next / previous tab |
| Panel | `z` | Drawer ↔ Board |
| Panel | `j` `k`, `↑` `↓`, `g` `G` | Move; first / last |
| Panel | `1`–`9` | Select that row in Members or Epics |
| Panel | `Enter` | Focus the member's pane (or switch to the Epic's session) and hide the panel |
| Overview | `o` | Open the full hand-back with `md-viewer` (or `$EDITOR`) |
| Mail | `/`, `p`, `f`, `Space` | Search subjects, this member's mail only, follow, expand excerpt |
| Graph | `[` `]`, `L`, `h` | Previous / next message, back to Live, hide retired |
| Graph | click a member | Select it (Members follows); a second click focuses its pane and hides the panel |
| Panel | `Esc` / `q` | Step back (clear a filter, close an excerpt); at the top, hide the panel |
| Panel | `Q` | Quit the panel; Rex closes its layer, and the next toggle opens a new one |

What `Cmd+Shift+A` does depends on where the panel is:

| The panel is | Toggle |
|---|---|
| not open in this session | opens it (`rex-amq-ui open`) in the current tab |
| up and focused in this tab | pauses it and minimizes its layer; focus returns to your pane |
| minimized, or up but not focused, in this tab | brings it back to normal, focuses it, and resumes it |
| in another tab of the session | moves it into this tab at the same size, then shows it |
| left in a tab's layout by a move that broke off | floats it again in this tab, then shows it |

`Cmd+Shift+U` asks `rex-amq-ui waiting --json` who waits on you. Nobody: it logs `rex-amq-ui: nobody waits`. One member whose pane still exists: it hides the panel if it is up and focuses that pane, switching tabs if needed. Several, or one whose pane is gone: it shows the panel on Overview, where the alerts are.

Rex's Lua has no toast, so a key press that finds nobody waiting, or fails, shows nothing on screen. The plugin writes a `rex-amq-ui: …` line with `rex.log` and returns the outcome as the action's result. `rex do amqui_toggle --json` prints both, which is the way to see why a key did nothing. The Rex docs do not say where `rex.log` lines from a key press go. The plugin never raises an error dialog.

## Command line

```sh
rex-amq-ui [--session S] [--adapter-bin P] [--focus B] [--tab T] [--board]
rex-amq-ui open [--session S] [--tab T] [--board] [--adapter-bin P]
rex-amq-ui waiting --json [--session S] [--adapter-bin P]
rex-amq-ui --version
```

- The bare command is the panel itself, which runs inside the floating layer. `open` starts it there.
- `open` opens the session's panel, or restores the one already open, and prints one JSON line: `{"layer_id":"…","block_id":"…","restored":true|false}`. It needs `$REX_SERVER`, and a session from `--session` or `$REX_SESSION`. It exits 2 on a usage error (an unknown flag or `--tab`, no server or session) and 1 when Rex fails.
- `waiting` runs the adapter until its first snapshot and prints one JSON line: `{"session":"…","members":[{"handle":"…","block_id":"…","reasons":["waiting_on_the_human","turn_error","stalled"]}]}`. Members come in handle order. `reasons` lists each mark kind once. `block_id` is left out when the member's pane is gone. It exits 1 when the adapter fails or sends no snapshot within 5 seconds, and 2 on a usage error.
- `--tab` takes `overview`, `members`, `mail`, `graph`, or `epics`, and names a panel tab, not a Rex window.

## Control sequences

The plugin talks to a running panel by typing private APC sequences into its terminal (`block.method` `write`, one sequence or a resume-and-tab pair per write):

```
ESC _ amqui;pause ESC \        stop drawing (the plugin sends it before minimizing)
ESC _ amqui;resume ESC \       draw the current frame now
ESC _ amqui;zoom ESC \         Drawer ↔ Board
ESC _ amqui;tab:<name> ESC \   show a tab: overview, members, mail, graph, epics
```

The panel takes these sequences out of its input wherever the reads split them, so none of their bytes reach the keys or the screen. It ignores an unknown verb, and `tab:epics` outside a personal session. Any other APC passes through.

While paused, the panel keeps folding the adapter's frames but writes nothing. It also pauses when it sees its layer minimized, and it resumes on `resume`, when its layer goes from minimized to normal, or on any key.

## Graph

The Graph tab draws its picture as an image, through the Kitty graphics protocol, between the replay bar and the legend.

- **What it shows.** Members sit around an ellipse, the lead at the top and the rest clockwise. Up to 6 live members get a node each, and the others fold into one `+N` node. While retired members are shown (`h` toggles them), up to 2 of them appear dimmed. Each pair of members that mailed has one arrow per direction, thicker the more mail it carries. A solid green arrow is drained mail, a dashed blue one is queued mail, and a dashed red one is bounced or dead-lettered mail. A yellow arrow with a glow is an open obligation. A working member's node breathes, and new mail sends a pulse along its arrow for 1.2 seconds. Text is Go Mono, compiled into the binary, so a frame looks the same on every machine.
- **Replay.** `[` and `]` move the cursor one message back or forward, and the picture shows the mail up to it, pulsing the message the cursor reaches. `L` (or `Esc`) goes back to Live, and the mail the replay hid pulses once.
- **Clicks.** A left click on a node selects that member, here and in Members. A second click on the selected node does what `Enter` does on Members: it focuses that member's pane and hides the panel.
- **Cost.** A frame is drawn only when something changed or something moves on its own (a working member, a pulse), at up to 30 frames a second. An idle graph draws nothing. The panel draws no image while it is paused or on another tab, and removes the one it showed. Each frame goes to the terminal as a file in `/tmp`, which the Kitty protocol has the terminal delete once read (not yet observed on Rex). When the terminal has not read the last 3 frames, the panel skips frames and says so in the footer. On exit the panel removes the image, turns pixel mouse reports off, and deletes any of its frame files left in `/tmp`.

[docs/design.md](docs/design.md#graph-renderer) covers how frames are drawn and sent, and which Rex behaviour it relies on that no test has seen yet.

## How to test

```sh
go test -race ./...                                    # headless: fake Rex control server, recorded adapter streams
luajit lua/rex-amq-ui/tests/run.lua                    # the plugin, against a stub rex global
lua/rex-amq-ui/tests/traps.sh                          # integration.sh cleans up when interrupted (fake rex, no server)
lua/rex-amq-ui/tests/integration.sh                    # live: needs a running local Rex server
REX_AMQ_ADAPTER_BIN=/path/to/rex-amq-adapter go test -run TestAdapterPanelIntegration ./internal/adapter
```

`go test` and the Lua harness never touch Rex. `integration.sh` builds the binary and creates its own Rex session, which it kills on exit. It drives the plugin there through `rex do`, with a fake adapter that replays `testdata/panel` streams. It never selects that session in the app, reloads your config, or touches another session. An interrupted run (Ctrl-C, `TERM`, `HUP`) still kills the session. The last command runs the panel's stream against a real adapter build. The Graph tab's frames are compared with reference PNGs in `testdata/graph` and `testdata/raster`, and its text with `testdata/golden`; `go test ./internal/graph ./internal/raster ./internal/ui -update` rewrites them after an intended change.

## Design

See [docs/design.md](docs/design.md) for how the packages fit together, what the panel will not do, and the Rex quirks it works around.

## License

[MIT](LICENSE)

#!/bin/sh
# Live check of the Rex plugin against a running local Rex server.
#
# It creates its own disposable session, drives the plugin there through
# `rex do <fixture> --action …` (Rex's own Lua runs init.lua), and kills the
# session on exit. It never selects that session in the app, never reloads
# your config, and touches no other session. The panel reads a fake adapter
# that replays testdata streams, so no agent or mail is involved.
#
# Needs: go, python3, and a running Rex server. Not part of `go test`.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
# Never act on the session or pane this script was started from.
unset REX_SESSION REX_BLOCK

tmp=$(mktemp -d)
session=""
cleanup() {
  if [ -n "$session" ]; then rex kill "$session" >/dev/null 2>&1 || true; fi
  rm -rf "$tmp"
}
trap cleanup EXIT
# dash skips the EXIT trap on a fatal signal: exit on each, so cleanup runs
# (tests/traps.sh checks this under sh and dash).
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

go -C "$root" build -o "$tmp/rex-amq-ui" ./cmd/rex-amq-ui
cat >"$tmp/rex-amq-adapter" <<EOF
#!/bin/sh
cat "$tmp/stream.ndjson"
exec sleep 30
EOF
chmod +x "$tmp/rex-amq-adapter"
cat >"$tmp/fixture.lua" <<EOF
-- Loaded by rex do; returns nothing, since rex do prints a script's result.
dofile("$root/lua/rex-amq-ui/init.lua").setup{
  toggle_key = false, waiting_key = false,
  bin = "$tmp/rex-amq-ui", adapter_bin = "$tmp/rex-amq-adapter",
}
EOF

json() { expr=$1; shift; python3 -c "import json,sys;d=json.load(sys.stdin);print($expr)" "$@"; }
view() { rex api call -s "$session" session.view; }
# summary prints the session's windows and layers, one line each.
summary() {
  view | python3 -c '
import json,sys
d=json.load(sys.stdin)
print("  active", d.get("active_window_id"))
for w in d.get("windows") or []:
    print("  window", w["window_id"], "focused", w.get("focused_block_id"))
    for l in w.get("layers") or []:
        print("    layer", l["layer_id"], l["kind"], l.get("state","normal"), [round(x,2) for x in l.get("bounds",[])],
              [(b.get("label") or "-", b["block_id"]) for b in l.get("blocks") or []])'
}
act() { rex do -s "$session" -b "$block" "$tmp/fixture.lua" --action "$1" --json; }
panel_block() { view | json '([b["block_id"] for w in d["windows"] for l in w.get("layers",[]) if l["kind"]=="floating" for b in l.get("blocks") or [] if b.get("label")=="rex-amq-ui"] or [""])[0]'; }
panel_state() { view | json '([l.get("state","normal") for w in d["windows"] for l in w.get("layers",[]) for b in l.get("blocks") or [] if b.get("label")=="rex-amq-ui"] or ["none"])[0]'; }
focused() { view | json '[w.get("focused_block_id") for w in d["windows"] if w["window_id"]==d["active_window_id"]][0]'; }
fail() { echo "FAIL: $*"; summary; exit 1; }
step() { echo; echo "== $*"; }
wait_screen() { # block, text
  for _ in $(seq 1 40); do
    if rex capture -s "$session" -b "$1" | grep -q "$2"; then return 0; fi
    sleep 0.25
  done
  return 1
}

# Streams the fake adapter replays: two members wait, one waits (on this
# session's first pane), nobody waits.
cp "$root/testdata/panel/edited-crew-attention.ndjson" "$tmp/two.ndjson"
cp "$root/testdata/panel/edited-personal-cockpit.ndjson" "$tmp/none.ndjson"
cp "$tmp/two.ndjson" "$tmp/stream.ndjson"

session=$(rex new amqui-integration --json -- sleep 600 | json 'd["session_id"]')
block=$(rex session inspect "$session" --json | json 'd["windows"][0]["focused_block_id"]')
window1=$(view | json 'd["active_window_id"]')
echo "session $session, window $window1, pane $block"
head -n 1 "$tmp/two.ndjson" | python3 -c '
import json,sys
d=json.load(sys.stdin)
d["panel"]["attention"]={"claude-02":["turn_error@2026-11-16T09:00:00Z"]}
for a in d["agents"]:
    if a["handle"]=="claude-02": a["block_id"]=sys.argv[1]
print(json.dumps(d))' "$block" >"$tmp/one.ndjson"

step "toggle with no panel: opens one"
act amqui_toggle
panel=$(panel_block)
[ -n "$panel" ] || fail "no rex-amq-ui layer"
wait_screen "$panel" "SYSTEM ALERTS" || fail "panel did not draw its Overview"
[ "$(focused)" = "$panel" ] || fail "new panel is not focused"
summary

step "toggle on the focused panel: minimizes it"
act amqui_toggle
[ "$(panel_state)" = minimized ] || fail "panel not minimized"
[ "$(focused)" = "$block" ] || fail "focus did not return to the pane"
summary

step "toggle on the minimized panel: restores it with focus"
act amqui_toggle
[ "$(panel_state)" = normal ] || fail "panel not normal"
[ "$(focused)" = "$panel" ] || fail "restored panel is not focused"

step "APC written through block.method switches the panel's tab"
printf '{"block_id":"%s","method":"write","args":{"data":"\\u001b_amqui;tab:mail\\u001b\\\\"}}' "$panel" >"$tmp/write.json"
rex api call -s "$session" block.method "@$tmp/write.json"
wait_screen "$panel" "Follow" || fail "tab:mail did not show the Mail tab"
if rex capture -s "$session" -b "$panel" | grep -q "amqui"; then fail "APC bytes reached the screen"; fi
echo "  Mail tab shown, no APC bytes on screen"

step "a second tab becomes active: toggle brings the panel over"
window2=$(rex window new -s "$session" --json -- sleep 600 | json 'd["window_id"]')
[ "$(view | json 'd["active_window_id"]')" = "$window2" ] || fail "new window not active"
summary
act amqui_toggle
summary
in2=$(view | json '[l.get("state","normal") for w in d["windows"] if w["window_id"]==sys.argv[1] for l in w.get("layers",[]) if l["kind"]=="floating" for b in l.get("blocks") or [] if b.get("label")=="rex-amq-ui"]' "$window2")
echo "  panel layers in the new tab: $in2"
[ "$in2" = "['normal']" ] || fail "panel not floating normal in the new tab"
left=$(view | json 'sum(1 for w in d["windows"] if w["window_id"]==sys.argv[1] for l in w.get("layers",[]) if l["kind"]=="floating")' "$window1")
echo "  floating layers left in the first tab: $left"
[ "$left" = 0 ] || fail "the move left a floating layer in the first tab"
[ "$(focused)" = "$panel" ] || fail "moved panel is not focused"
wait_screen "$panel" "Follow" || fail "moved panel stopped drawing"

step "waiting with several waiters: panel on Overview"
act amqui_toggle >/dev/null # hide it first
[ "$(panel_state)" = minimized ] || fail "panel not minimized before waiting"
act amqui_waiting
[ "$(panel_state)" = normal ] || fail "panel not restored for several waiters"
wait_screen "$panel" "SYSTEM ALERTS" || fail "panel not on Overview"

step "waiting with one waiter: panel minimized, the pane focused"
cp "$tmp/one.ndjson" "$tmp/stream.ndjson"
act amqui_waiting
[ "$(panel_state)" = minimized ] || fail "panel still up after jumping to the waiter"
[ "$(focused)" = "$block" ] || fail "waiter's pane not focused"
[ "$(view | json 'd["active_window_id"]')" = "$window1" ] || fail "waiter's tab not active"

step "waiting with nobody: a log line, nothing moves"
cp "$tmp/none.ndjson" "$tmp/stream.ndjson"
before=$(view | json 'd["revision"]')
act amqui_waiting
[ "$(view | json 'd["revision"]')" = "$before" ] || fail "nobody waiting still changed the session"

step "Q quits the panel: what is left of its layer"
cp "$tmp/two.ndjson" "$tmp/stream.ndjson"
act amqui_toggle >/dev/null
[ "$(panel_state)" = normal ] || fail "panel not up before Q"
rex send -s "$session" -b "$panel" Q
for _ in $(seq 1 20); do
  [ -z "$(panel_block)" ] && break
  sleep 0.25
done
summary
[ -z "$(panel_block)" ] || fail "after Q the panel's block $(panel_block) is still there"
gone=$(view | json 'sum(1 for w in d["windows"] for l in w.get("layers",[]) for b in l.get("blocks") or [] if b["block_id"]==sys.argv[1])' "$panel")
[ "$gone" = 0 ] || fail "after Q the old block $panel is still in the session"
echo "  after Q the panel's block and its layer are gone"
act amqui_toggle
old=$panel
panel=$(panel_block)
[ -n "$panel" ] || fail "toggle after Q opened no panel"
[ "$panel" != "$old" ] || fail "toggle after Q found the old block $old"
[ "$(panel_state)" = normal ] || fail "reopened panel not normal"
[ "$(focused)" = "$panel" ] || fail "reopened panel not focused"
wait_screen "$panel" "SYSTEM ALERTS" || fail "the reopened panel did not draw"
summary

echo
echo "PASS: integration: open, minimize, restore, APC tab, cross-tab move, waiting (several, one, nobody), reopen after Q"

#!/bin/sh
# Checks that integration.sh kills its disposable session when it is
# interrupted (INT, TERM, HUP), not only when it exits on its own.
#
# It runs integration.sh against fake rex and go commands placed first on
# PATH, so it never reaches a Rex server: the fake rex hangs in `rex do`, and
# the test signals the script's process group there, as a terminal or a
# supervisor would. It runs the script under each POSIX shell it finds: bash
# (macOS's /bin/sh) runs an EXIT trap on a fatal signal, dash does not.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
fake=$(mktemp -d)
trap 'rm -rf "$fake"' EXIT

cat >"$fake/rex" <<'EOF'
#!/bin/sh
echo "$*" >>"$FAKE_REX_LOG"
case "$1 $2" in
  "new "*) echo '{"session_id":"session:fake"}' ;;
  "session inspect") echo '{"windows":[{"window_id":"window:1","focused_block_id":"block:a"}]}' ;;
  "api call") echo '{"active_window_id":"window:1","revision":1,"windows":[{"window_id":"window:1","focused_block_id":"block:a","layers":[]}]}' ;;
  "do "*) exec sleep 30 ;;
  "kill "*) ;;
  *) echo "fake rex: unexpected: $*" >&2; exit 1 ;;
esac
EOF
printf '#!/bin/sh\nexit 0\n' >"$fake/go"
chmod +x "$fake/rex" "$fake/go"

failures=0
for shell in /bin/sh /bin/dash; do
  [ -x "$shell" ] || continue
  for sig in INT TERM HUP; do
    case $sig in INT) want=130 ;; TERM) want=143 ;; HUP) want=129 ;; esac
    log="$fake/$sig.log"
    : >"$log"
    # Job control gives the script its own process group, as a terminal's
    # foreground job has, and keeps INT from being ignored in the background.
    set -m
    FAKE_REX_LOG="$log" PATH="$fake:$PATH" "$shell" "$here/integration.sh" >"$fake/$sig.out" 2>&1 &
    pid=$!
    set +m
    for _ in $(seq 1 100); do
      grep -q '^do ' "$log" && break
      sleep 0.1
    done
    if ! grep -q '^do ' "$log"; then
      echo "FAIL $shell $sig: integration.sh never reached rex do"
      cat "$fake/$sig.out"
      failures=$((failures + 1))
      kill -KILL -- "-$pid" 2>/dev/null || true
      continue
    fi
    kill -s "$sig" -- "-$pid"
    status=0
    wait "$pid" 2>/dev/null || status=$?
    if ! grep -q '^kill session:fake$' "$log"; then
      echo "FAIL $shell $sig: the session was not killed"
      failures=$((failures + 1))
    fi
    if [ "$status" != "$want" ]; then
      echo "FAIL $shell $sig: exit status $status, want $want"
      failures=$((failures + 1))
    fi
    if grep -v -E '^(new |session inspect |api call |do |kill session:fake$)' "$log" | grep -q .; then
      echo "FAIL $shell $sig: unexpected rex calls:"
      cat "$log"
      failures=$((failures + 1))
    fi
  done
done
if [ "$failures" -gt 0 ]; then
  echo "$failures failure(s)"
  exit 1
fi
echo "PASS: traps: under sh and dash, INT, TERM and HUP each kill the disposable session and exit 128+signal"

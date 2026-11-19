-- Unit tests for init.lua: luajit lua/rex-amq-ui/tests/run.lua
-- A stub `rex` global records every call the plugin makes and answers with
-- scripted views and command results. No Rex server is needed.
local here = assert(debug.getinfo(1, "S").source:gsub("^@", ""):match("^(.*/)tests/"),
  "run.lua: run it by path")
local PLUGIN = here .. "init.lua"
local ESC = "\27"
local S = "session:t"

local failures, count = 0, 0
local function check(name, ok, detail)
  count = count + 1
  if not ok then
    failures = failures + 1
    print("FAIL " .. name .. (detail and (": " .. tostring(detail)) or ""))
  end
end

-- JSON --------------------------------------------------------------------------

-- A small JSON decoder for the stub's rex.json.decode (Rex's turns null into nil).
local function decode(text)
  local pos = 1
  local function ws() pos = text:find("[^ \t\r\n]", pos) or #text + 1 end
  local value
  local function str()
    local out = {}
    pos = pos + 1
    while true do
      local c = text:sub(pos, pos)
      if c == "" then error("unterminated string") end
      if c == '"' then pos = pos + 1 break end
      if c == "\\" then
        local e = text:sub(pos + 1, pos + 1)
        local map = { n = "\n", t = "\t", r = "\r", b = "\b", f = "\f", ['"'] = '"', ["\\"] = "\\", ["/"] = "/" }
        if e == "u" then
          out[#out + 1] = string.char(tonumber(text:sub(pos + 2, pos + 5), 16) % 256)
          pos = pos + 6
        else
          out[#out + 1] = assert(map[e], "bad escape")
          pos = pos + 2
        end
      else
        out[#out + 1] = c
        pos = pos + 1
      end
    end
    return table.concat(out)
  end
  function value()
    ws()
    local c = text:sub(pos, pos)
    if c == "{" then
      local t = {}
      pos = pos + 1
      ws()
      if text:sub(pos, pos) == "}" then pos = pos + 1 return t end
      while true do
        ws()
        local k = str()
        ws()
        assert(text:sub(pos, pos) == ":", "want :")
        pos = pos + 1
        t[k] = value()
        ws()
        local d = text:sub(pos, pos)
        pos = pos + 1
        if d == "}" then return t end
        assert(d == ",", "want , or }")
      end
    elseif c == "[" then
      local t = {}
      pos = pos + 1
      ws()
      if text:sub(pos, pos) == "]" then pos = pos + 1 return t end
      while true do
        t[#t + 1] = value()
        ws()
        local d = text:sub(pos, pos)
        pos = pos + 1
        if d == "]" then return t end
        assert(d == ",", "want , or ]")
      end
    elseif c == '"' then
      return str()
    elseif text:sub(pos, pos + 3) == "true" then pos = pos + 4 return true
    elseif text:sub(pos, pos + 4) == "false" then pos = pos + 5 return false
    elseif text:sub(pos, pos + 3) == "null" then pos = pos + 4 return nil
    end
    local num = text:match("^-?%d+%.?%d*[eE]?[-+]?%d*", pos)
    if not num or num == "" then error("bad JSON at " .. pos) end
    pos = pos + #num
    return tonumber(num)
  end
  local ok, v = pcall(value)
  if not ok then return nil, v end
  return v
end

-- The stub Rex ---------------------------------------------------------------------

local function show(v)
  if type(v) == "string" then return string.format("%q", v) end
  if type(v) ~= "table" then return tostring(v) end
  local keys = {}
  for k in pairs(v) do keys[#keys + 1] = k end
  table.sort(keys, function(a, b) return tostring(a) < tostring(b) end)
  local parts = {}
  for _, k in ipairs(keys) do parts[#parts + 1] = tostring(k) .. "=" .. show(v[k]) end
  return "{" .. table.concat(parts, ",") .. "}"
end

local env -- the environment os.getenv reads in a test
local real_getenv = os.getenv
os.getenv = function(k) return env[k] end

-- world sets up a fresh stub: rex.call answers from `calls` (method -> function
-- or value), rex.exec from `exec` (function(argv, opts)). It returns the log of
-- what the plugin did: rex.call and rex.exec in order, rex.log lines, and the
-- actions and bindings it registered.
local function world(answers)
  answers = answers or {}
  local w = { seq = {}, logs = {}, actions = {}, binds = {} }
  env = answers.env or {}
  rex = {
    action = function(spec) w.actions[spec.name] = spec end,
    bind = function(key, name) w.binds[#w.binds + 1] = key .. "=" .. name end,
    log = function(level, message) w.logs[#w.logs + 1] = level .. " " .. message end,
    json = { decode = decode },
    call = function(method, args)
      w.seq[#w.seq + 1] = { method, args }
      local a = (answers.calls or {})[method]
      if type(a) == "function" then return a(args) end
      if a ~= nil then return a end
      return { revision = 7 }
    end,
    exec = function(argv, opts)
      w.seq[#w.seq + 1] = { "exec", argv, opts }
      if answers.exec then return answers.exec(argv, opts) end
      return nil, "no such program"
    end,
  }
  w.plugin = dofile(PLUGIN)
  return w
end

local function ran(stdout) return { ok = true, code = 0, stdout = stdout, stderr = "", timed_out = false } end
local function exited(code, stderr) return { ok = false, code = code, stdout = "", stderr = stderr, timed_out = false } end

-- steps renders the call log as one line per call, for exact comparison.
local function steps(w)
  local out = {}
  for _, s in ipairs(w.seq) do
    if s[1] == "exec" then
      out[#out + 1] = "exec " .. table.concat(s[2], " ")
    elseif s[1] == "block.method" then
      out[#out + 1] = "write " .. s[2].block_id .. " " .. string.format("%q", s[2].args.data)
      if s[2].method ~= "write" or s[2].session_id ~= S then out[#out] = out[#out] .. " BAD " .. show(s[2]) end
    else
      local a = {}
      for k, v in pairs(s[2] or {}) do if k ~= "session_id" then a[#a + 1] = k .. "=" .. show(v) end end
      table.sort(a)
      out[#out + 1] = s[1] .. " " .. table.concat(a, " ")
      if (s[2] or {}).session_id ~= S then out[#out] = out[#out] .. " BAD session " .. show((s[2] or {}).session_id) end
    end
  end
  return out
end

local function same(name, w, want)
  local got = steps(w)
  local ok = #got == #want
  for i = 1, math.max(#got, #want) do ok = ok and got[i] == want[i] end
  check(name, ok, "\n  got:  " .. table.concat(got, "\n        ") .. "\n  want: " .. table.concat(want, "\n        "))
end

local function has_log(w, pattern)
  for _, l in ipairs(w.logs) do if l:find(pattern) then return true end end
  return false
end

-- Views ------------------------------------------------------------------------------

-- view builds a session.view: windows of { id, focused, tiled = {blocks},
-- floating = {{ id, block, label, state, bounds }} }.
local function view(active, windows)
  local v = { session_id = S, revision = 3, active_window_id = active, windows = {} }
  for _, spec in ipairs(windows) do
    local w = { window_id = spec.id, focused_block_id = spec.focused, layers = {} }
    local tiled = { layer_id = "layer:tiled-" .. spec.id, kind = "tiled", bounds = { 0, 0, 1, 1 }, blocks = {} }
    for _, b in ipairs(spec.tiled or {}) do tiled.blocks[#tiled.blocks + 1] = { block_id = b, label = (spec.labels or {})[b] or "" } end
    w.layers[1] = tiled
    for _, f in ipairs(spec.floating or {}) do
      w.layers[#w.layers + 1] = { layer_id = f.id, kind = "floating", state = f.state,
        bounds = f.bounds or { 0.57, 0.02, 0.42, 0.96 }, blocks = { { block_id = f.block, label = f.label or "rex-amq-ui" } } }
    end
    v.windows[#v.windows + 1] = w
  end
  return v
end

local PAUSE, RESUME = ESC .. "_amqui;pause" .. ESC .. "\\", ESC .. "_amqui;resume" .. ESC .. "\\"
local OVERVIEW = ESC .. "_amqui;tab:overview" .. ESC .. "\\"
local function q(s) return string.format("%q", s) end
local BIN = "/opt/bin/rex-amq-ui"

-- setup ------------------------------------------------------------------------------

do
  local w = world()
  w.plugin.setup()
  check("setup registers amqui_toggle", type((w.actions.amqui_toggle or {}).run) == "function")
  check("setup registers amqui_waiting", type((w.actions.amqui_waiting or {}).run) == "function")
  check("default keys", table.concat(w.binds, " ") == "cmd+shift+a=amqui_toggle cmd+shift+u=amqui_waiting",
    table.concat(w.binds, " "))
  check("setup itself calls nothing", #w.seq == 0)
end

do
  local w = world()
  w.plugin.setup{ toggle_key = "ctrl+alt+p", waiting_key = false }
  check("configured key, false unbinds", table.concat(w.binds, " ") == "ctrl+alt+p=amqui_toggle", table.concat(w.binds, " "))
end

local function setup_fails(name, opts, want)
  local w = world()
  local ok, err = pcall(w.plugin.setup, opts)
  check(name, not ok and tostring(err):find(want, 1, true) ~= nil, err)
end
setup_fails("unknown option", { toggle = "x" }, "rex-amq-ui: unknown option toggle")
setup_fails("key of wrong type", { toggle_key = 1 }, "rex-amq-ui: toggle_key must be a shortcut or false")
setup_fails("empty key", { waiting_key = "" }, "rex-amq-ui: waiting_key must be")
setup_fails("board not boolean", { board = "yes" }, "rex-amq-ui: board must be true or false")
setup_fails("empty bin", { bin = "" }, "rex-amq-ui: bin must be")
setup_fails("adapter_bin of wrong type", { adapter_bin = true }, "rex-amq-ui: adapter_bin must be")
setup_fails("options not a table", "cmd+a", "rex-amq-ui: setup takes a table")

-- toggle --------------------------------------------------------------------------------

local function toggle(answers, opts)
  local w = world(answers)
  w.plugin.setup(opts or { bin = BIN })
  w.result = w.actions.amqui_toggle.run({ session_id = S, block_id = "block:a" })
  return w
end

do -- no panel: the binary opens one
  local w = toggle({
    calls = { ["session.view"] = view("w1", { { id = "w1", focused = "block:a", tiled = { "block:a" } } }) },
    exec = function() return ran('{"layer_id":"layer:new","block_id":"block:p","restored":false}\n') end,
  })
  same("toggle with no panel opens one", w, { "session.view ", "exec " .. BIN .. " open --session " .. S })
  check("opened result", w.result.action == "opened" and w.result.layer_id == "layer:new" and w.result.block_id == "block:p",
    show(w.result))
  check("exec has a timeout", (w.seq[2][3] or {}).timeout == 20, show(w.seq[2][3]))
end

do -- board and adapter_bin reach the launcher
  local w = toggle({
    calls = { ["session.view"] = view("w1", { { id = "w1", focused = "block:a", tiled = { "block:a" } } }) },
    exec = function() return ran('{"layer_id":"layer:new","block_id":"block:p"}') end,
  }, { bin = BIN, board = true, adapter_bin = "/x/rex-amq-adapter" })
  same("open passes --board and --adapter-bin", w,
    { "session.view ", "exec " .. BIN .. " open --board --session " .. S .. " --adapter-bin /x/rex-amq-adapter" })
end

do -- the launcher fails: a log line, no error raised
  local w = toggle({
    calls = { ["session.view"] = view("w1", { { id = "w1", focused = "block:a", tiled = { "block:a" } } }) },
    exec = function() return exited(1, "rex-amq-ui open: dial: refused\n") end,
  })
  check("open failure is a result", w.result.action == "failed" and w.result.error:find("dial: refused", 1, true), show(w.result))
  check("open failure is logged", has_log(w, "^error rex%-amq%-ui: rex%-amq%-ui open: dial: refused$"), table.concat(w.logs, "|"))
end

do -- up and focused: pause, then minimize
  local w = toggle({ calls = { ["session.view"] = view("w1", {
    { id = "w1", focused = "block:p", tiled = { "block:a" }, floating = { { id = "layer:p", block = "block:p" } } } }) } })
  same("toggle hides a focused panel", w, {
    "session.view ",
    "write block:p " .. q(PAUSE),
    "session.set_layer_state layer_id=\"layer:p\" state=\"minimized\"",
  })
  check("hidden result", w.result.action == "minimized", show(w.result))
end

do -- minimized: normal with focus, then resume
  local w = toggle({ calls = { ["session.view"] = view("w1", {
    { id = "w1", focused = "block:a", tiled = { "block:a" }, floating = { { id = "layer:p", block = "block:p", state = "minimized" } } } }) } })
  same("toggle restores a minimized panel", w, {
    "session.view ",
    "session.set_layer_state focus=true layer_id=\"layer:p\" state=\"normal\"",
    "write block:p " .. q(RESUME),
  })
  check("restored result", w.result.action == "restored", show(w.result))
end

do -- up in active tab, even when another pane has focus: hide it in one keypress
  local w = toggle({ calls = { ["session.view"] = view("w1", {
    { id = "w1", focused = "block:a", tiled = { "block:a" }, floating = { { id = "layer:p", block = "block:p" } } } }) } })
  same("toggle hides a visible unfocused panel in one keypress", w, {
    "session.view ",
    "write block:p " .. q(PAUSE),
    "session.set_layer_state layer_id=\"layer:p\" state=\"minimized\"",
  })
  check("hidden unfocused result", w.result.action == "minimized", show(w.result))
end

do -- in another tab: move it here at its size, float it, show it
  local w = toggle({ calls = {
    ["session.view"] = view("w2", {
      { id = "w1", focused = "block:p", tiled = { "block:a" }, floating = { { id = "layer:p", block = "block:p", bounds = { 0.06, 0.06, 0.88, 0.88 } } } },
      { id = "w2", focused = "block:c", tiled = { "block:b", "block:c" } } }),
    ["session.float_block"] = { layer_id = "layer:q", revision = 9 },
  } })
  same("toggle brings the panel over from another tab", w, {
    "session.view ",
    "session.move_block anchor_block_id=\"block:c\" block_id=\"block:p\"",
    "session.float_block block_id=\"block:p\" bounds={1=0.06,2=0.06,3=0.88,4=0.88}",
    "session.set_layer_state focus=true layer_id=\"layer:q\" state=\"normal\"",
    "write block:p " .. q(RESUME),
  })
  check("moved result names the new layer", w.result.action == "moved" and w.result.layer_id == "layer:q", show(w.result))
end

do -- the active tab's focus is in a floating layer: anchor on its first tiled pane
  local w = toggle({ calls = {
    ["session.view"] = view("w2", {
      { id = "w1", focused = "block:a", tiled = { "block:a" }, floating = { { id = "layer:p", block = "block:p", state = "minimized" } } },
      { id = "w2", focused = "block:f", tiled = { "block:b", "block:c" }, floating = { { id = "layer:f", block = "block:f", label = "other" } } } }),
    ["session.float_block"] = { layer_id = "layer:q", revision = 9 },
  } })
  check("anchor is a tiled pane", steps(w)[2] == "session.move_block anchor_block_id=\"block:b\" block_id=\"block:p\"", steps(w)[2])
end

do -- a panel in the active tab wins over one elsewhere
  local w = toggle({ calls = { ["session.view"] = view("w2", {
    { id = "w1", focused = "block:p1", tiled = { "block:a" }, floating = { { id = "layer:p1", block = "block:p1" } } },
    { id = "w2", focused = "block:p2", tiled = { "block:b" }, floating = { { id = "layer:p2", block = "block:p2" } } } }) } })
  same("toggle acts on the active tab's panel", w, {
    "session.view ",
    "write block:p2 " .. q(PAUSE),
    "session.set_layer_state layer_id=\"layer:p2\" state=\"minimized\"",
  })
end

do -- ... whichever order the view lists the tabs in
  local w = toggle({ calls = { ["session.view"] = view("w1", {
    { id = "w1", focused = "block:p1", tiled = { "block:a" }, floating = { { id = "layer:p1", block = "block:p1" } } },
    { id = "w2", focused = "block:p2", tiled = { "block:b" }, floating = { { id = "layer:p2", block = "block:p2" } } } }) } })
  check("the active tab's panel wins when listed first", steps(w)[2] == "write block:p1 " .. q(PAUSE), steps(w)[2])
end

do -- a panel block in a layout (a move broke off there) is floated again, not opened twice
  local v = view("w1", { { id = "w1", focused = "block:a", tiled = { "block:a" } } })
  v.windows[1].layers[1].blocks[2] = { block_id = "block:t", label = "rex-amq-ui" }
  local w = toggle({ calls = { ["session.view"] = v, ["session.float_block"] = { layer_id = "layer:t", revision = 4 } },
    exec = function() return ran('{"layer_id":"layer:new","block_id":"block:p"}') end })
  same("a panel left in the active tab's layout is floated in place", w, {
    "session.view ",
    "session.float_block block_id=\"block:t\" bounds={1=0.57,2=0.02,3=0.42,4=0.96}",
    "session.set_layer_state focus=true layer_id=\"layer:t\" state=\"normal\"",
    "write block:t " .. q(RESUME),
  })
end

do -- a write that raises does not stop the minimize
  local w = toggle({ calls = {
    ["session.view"] = view("w1", { { id = "w1", focused = "block:p", tiled = { "block:a" }, floating = { { id = "layer:p", block = "block:p" } } } }),
    ["block.method"] = function() error("closed") end,
  } })
  check("pause failure still minimizes", w.result.action == "minimized" and has_log(w, "^warn .*closed"), show(w.result) .. table.concat(w.logs, "|"))
end

do -- whatever an action raises stays inside it
  local w = world({ calls = { ["session.view"] = { windows = "not a list" } } })
  w.plugin.setup{ bin = BIN }
  local ok, r = pcall(w.actions.amqui_toggle.run, { session_id = S })
  check("a raising action is a logged failure", ok and type(r) == "table" and r.action == "failed" and #w.logs == 1, tostring(r))
end

do -- a block with another label is not the panel
  local w = toggle({
    calls = { ["session.view"] = view("w1", { { id = "w1", focused = "block:f", tiled = { "block:a" },
      floating = { { id = "layer:f", block = "block:f", label = "history" } } } }) },
    exec = function() return ran('{"layer_id":"layer:new","block_id":"block:p"}') end,
  })
  check("other floating layers are not the panel", steps(w)[2] == "exec " .. BIN .. " open --session " .. S, steps(w)[2])
end

do -- session.view fails: nothing else runs
  local w = toggle({ calls = { ["session.view"] = function() return nil, "session_not_found" end } })
  same("a failed view stops the toggle", w, { "session.view " })
  check("failed view is logged", has_log(w, "session%.view: session_not_found") and w.result.action == "failed", table.concat(w.logs, "|"))
end

do -- rex.call raising is caught too
  local w = toggle({ calls = { ["session.view"] = function() error("boom") end } })
  check("a raising rex.call is a logged failure", w.result.action == "failed" and has_log(w, "boom"), show(w.result))
end

do -- restoring fails: no resume is written
  local w = toggle({ calls = {
    ["session.view"] = view("w1", { { id = "w1", focused = "block:a", tiled = { "block:a" },
      floating = { { id = "layer:p", block = "block:p", state = "minimized" } } } }),
    ["session.set_layer_state"] = function() return nil, "bad_request" end,
  } })
  same("a failed restore writes nothing", w, { "session.view ", "session.set_layer_state focus=true layer_id=\"layer:p\" state=\"normal\"" })
  check("failed restore result", w.result.action == "failed", show(w.result))
end

do -- the move fails: nothing more
  local w = toggle({ calls = {
    ["session.view"] = view("w2", {
      { id = "w1", focused = "block:a", tiled = { "block:a" }, floating = { { id = "layer:p", block = "block:p" } } },
      { id = "w2", focused = "block:b", tiled = { "block:b" } } }),
    ["session.move_block"] = function() return nil, "conflict" end,
  } })
  same("a failed move stops", w, { "session.view ", "session.move_block anchor_block_id=\"block:b\" block_id=\"block:p\"" })
end

do -- no session in ctx
  local w = world()
  w.plugin.setup{ bin = BIN }
  local r = w.actions.amqui_toggle.run({})
  check("no session is a logged failure", r.action == "failed" and #w.seq == 0 and has_log(w, "no session"), show(r))
end

-- the binary --------------------------------------------------------------------------------

local empty = view("w1", { { id = "w1", focused = "block:a", tiled = { "block:a" } } })
local opened = '{"layer_id":"layer:new","block_id":"block:p"}'

do -- $REX_AMQ_UI_BIN when no bin option
  local w = toggle({ env = { REX_AMQ_UI_BIN = "/env/rex-amq-ui" }, calls = { ["session.view"] = empty },
    exec = function() return ran(opened) end }, {})
  check("REX_AMQ_UI_BIN is used", steps(w)[2] == "exec /env/rex-amq-ui open --session " .. S, steps(w)[2])
end

do -- the bin option wins over the environment
  local w = toggle({ env = { REX_AMQ_UI_BIN = "/env/rex-amq-ui" }, calls = { ["session.view"] = empty },
    exec = function() return ran(opened) end }, { bin = BIN })
  check("bin option wins", steps(w)[2] == "exec " .. BIN .. " open --session " .. S, steps(w)[2])
end

do -- otherwise the login shell's PATH, asked once
  local w = world({ env = { SHELL = "/opt/homebrew/bin/fish" }, calls = { ["session.view"] = empty },
    exec = function(argv)
      if argv[1] == "/opt/homebrew/bin/fish" then return ran("Welcome!\n/Users/me/go/bin/rex-amq-ui\n") end
      return ran(opened)
    end })
  w.plugin.setup{}
  w.actions.amqui_toggle.run({ session_id = S })
  w.actions.amqui_toggle.run({ session_id = S })
  same("login shell resolves the binary once", w, {
    "session.view ",
    "exec /opt/homebrew/bin/fish -l -c command -v rex-amq-ui",
    "exec /Users/me/go/bin/rex-amq-ui open --session " .. S,
    "session.view ",
    "exec /Users/me/go/bin/rex-amq-ui open --session " .. S,
  })
end

do -- not found anywhere
  local w = world({ env = {}, calls = { ["session.view"] = empty },
    exec = function(argv) if argv[1] == "/bin/sh" then return exited(1, "") end return ran(opened) end })
  w.plugin.setup{}
  local r = w.actions.amqui_toggle.run({ session_id = S })
  same("no binary: only the lookup runs", w, { "session.view ", "exec /bin/sh -l -c command -v rex-amq-ui" })
  check("no binary says how to fix it", r.action == "failed" and r.error:find("set bin in setup{} or REX_AMQ_UI_BIN", 1, true), show(r))
end

do -- the binary does not run at all
  local w = toggle({ calls = { ["session.view"] = empty } }) -- the stub's rex.exec answers nil, "no such program"
  check("a binary that cannot run is named", w.result.action == "failed" and w.result.error == "rex-amq-ui open: no such program", show(w.result))
end

do -- output that is not JSON
  local w = toggle({ calls = { ["session.view"] = empty }, exec = function() return ran("oops") end })
  check("unreadable launcher output is a failure", w.result.action == "failed" and w.result.error:find("unreadable output", 1, true), show(w.result))
end

-- waiting ------------------------------------------------------------------------------------

local function waiting(answers, opts)
  local w = world(answers)
  w.plugin.setup(opts or { bin = BIN })
  w.result = w.actions.amqui_waiting.run({ session_id = S, block_id = "block:a" })
  return w
end

local function who(members)
  return function(argv)
    if argv[2] == "waiting" then return ran('{"session":"' .. S .. '","members":' .. members .. '}\n') end
    return ran(opened)
  end
end

do -- nobody waits
  local w = waiting({ exec = who("[]") })
  same("nobody waits: only the question", w, { "exec " .. BIN .. " waiting --json --session " .. S })
  check("nobody waits is said", w.result.action == "nobody" and has_log(w, "^info rex%-amq%-ui: nobody waits$"), table.concat(w.logs, "|"))
end

do -- members null (decoded as nil) counts as nobody
  local w = waiting({ exec = who("null") })
  check("null members is nobody", w.result.action == "nobody", show(w.result))
end

do -- one waits, the panel is up: hide it, then go to the pane
  local w = waiting({ exec = who('[{"handle":"claude-02","block_id":"block:c2","reasons":["turn_error"]}]'),
    calls = { ["session.view"] = view("w1", { { id = "w1", focused = "block:p", tiled = { "block:a" },
      floating = { { id = "layer:p", block = "block:p" } } } }) } }, { bin = BIN, adapter_bin = "/x/adapter" })
  same("one waiter: minimize the panel, focus the pane", w, {
    "exec " .. BIN .. " waiting --json --session " .. S .. " --adapter-bin /x/adapter",
    "session.view ",
    "write block:p " .. q(PAUSE),
    "session.set_layer_state layer_id=\"layer:p\" state=\"minimized\"",
    "session.focus_block block_id=\"block:c2\"",
  })
  check("focused result", w.result.action == "focused" and w.result.handle == "claude-02", show(w.result))
end

do -- one waits, the panel is minimized: just go
  local w = waiting({ exec = who('[{"handle":"pi-04","block_id":"block:p4","reasons":["stalled"]}]'),
    calls = { ["session.view"] = view("w1", { { id = "w1", focused = "block:a", tiled = { "block:a" },
      floating = { { id = "layer:p", block = "block:p", state = "minimized" } } } }) } })
  same("one waiter with the panel down: focus only", w, {
    "exec " .. BIN .. " waiting --json --session " .. S,
    "session.view ",
    "session.focus_block block_id=\"block:p4\"",
  })
end

do -- one waits but its pane is gone: the panel's alerts instead
  local w = waiting({ exec = who('[{"handle":"pi-04","reasons":["stalled"]}]'), calls = { ["session.view"] = empty } })
  same("one waiter without a pane: open on overview", w, {
    "exec " .. BIN .. " waiting --json --session " .. S,
    "session.view ",
    "exec " .. BIN .. " open --tab overview --session " .. S,
  })
end

do -- several wait, the panel is minimized: restore it on Overview
  local w = waiting({ exec = who('[{"handle":"a","block_id":"block:1","reasons":["stalled"]},{"handle":"b","block_id":"block:2","reasons":["turn_error"]}]'),
    calls = { ["session.view"] = view("w1", { { id = "w1", focused = "block:a", tiled = { "block:a" },
      floating = { { id = "layer:p", block = "block:p", state = "minimized" } } } }) } })
  same("several waiters: restore on Overview", w, {
    "exec " .. BIN .. " waiting --json --session " .. S,
    "session.view ",
    "session.set_layer_state focus=true layer_id=\"layer:p\" state=\"normal\"",
    "write block:p " .. q(RESUME .. OVERVIEW),
  })
end

do -- several wait, no panel: open it on Overview
  local w = waiting({ exec = who('[{"handle":"a","block_id":"block:1","reasons":["stalled"]},{"handle":"b","block_id":"block:2","reasons":["turn_error"]}]'),
    calls = { ["session.view"] = empty } }, { bin = BIN, board = true })
  same("several waiters, no panel: open on Overview", w, {
    "exec " .. BIN .. " waiting --json --session " .. S,
    "session.view ",
    "exec " .. BIN .. " open --tab overview --board --session " .. S,
  })
end

do -- several wait, the panel is in another tab: bring it over on Overview
  local w = waiting({ exec = who('[{"handle":"a","block_id":"block:1","reasons":["stalled"]},{"handle":"b","block_id":"block:2","reasons":["turn_error"]}]'),
    calls = {
      ["session.view"] = view("w2", {
        { id = "w1", focused = "block:a", tiled = { "block:a" }, floating = { { id = "layer:p", block = "block:p" } } },
        { id = "w2", focused = "block:b", tiled = { "block:b" } } }),
      ["session.float_block"] = { layer_id = "layer:q", revision = 9 },
    } })
  same("several waiters, panel elsewhere: move it here on Overview", w, {
    "exec " .. BIN .. " waiting --json --session " .. S,
    "session.view ",
    "session.move_block anchor_block_id=\"block:b\" block_id=\"block:p\"",
    "session.float_block block_id=\"block:p\" bounds={1=0.57,2=0.02,3=0.42,4=0.96}",
    "session.set_layer_state focus=true layer_id=\"layer:q\" state=\"normal\"",
    "write block:p " .. q(RESUME .. OVERVIEW),
  })
end

do -- the question fails
  local w = waiting({ exec = function() return exited(1, "rex-amq-ui waiting: adapter: refused\n") end })
  same("a failed waiting stops", w, { "exec " .. BIN .. " waiting --json --session " .. S })
  check("failed waiting is logged", w.result.action == "failed" and has_log(w, "adapter: refused"), table.concat(w.logs, "|"))
end

do -- focusing fails
  local w = waiting({ exec = who('[{"handle":"a","block_id":"block:1","reasons":["stalled"]}]'),
    calls = { ["session.view"] = empty, ["session.focus_block"] = function() return nil, "not_found" end } })
  check("a failed focus is a logged failure", w.result.action == "failed" and has_log(w, "not_found"), show(w.result))
end

-- review fixes: half-done transitions ---------------------------------------------------------

do -- paused, then the minimize is refused: resume it, or it stays up but frozen
  local w = toggle({ calls = {
    ["session.view"] = view("w1", { { id = "w1", focused = "block:p", tiled = { "block:a" }, floating = { { id = "layer:p", block = "block:p" } } } }),
    ["session.set_layer_state"] = function() return nil, "bad_request: last visible pane" end,
  } })
  same("a refused minimize resumes the paused panel", w, {
    "session.view ",
    "write block:p " .. q(PAUSE),
    "session.set_layer_state layer_id=\"layer:p\" state=\"minimized\"",
    "write block:p " .. q(RESUME),
  })
  check("refused minimize is a failure naming why", w.result.action == "failed" and w.result.error:find("last visible pane", 1, true), show(w.result))
end

do -- the pause never arrived: nothing to undo when the minimize is refused
  local w = toggle({ calls = {
    ["session.view"] = view("w1", { { id = "w1", focused = "block:p", tiled = { "block:a" }, floating = { { id = "layer:p", block = "block:p" } } } }),
    ["block.method"] = function() return nil, "not_found" end,
    ["session.set_layer_state"] = function() return nil, "bad_request" end,
  } })
  check("no resume after a pause that failed", #steps(w) == 3 and w.result.action == "failed", table.concat(steps(w), " | "))
end

local one = '[{"handle":"claude-02","block_id":"block:c2","reasons":["turn_error"]}]'

do -- one waiter, but the view cannot be read: no focus
  local w = waiting({ exec = who(one), calls = { ["session.view"] = function() return nil, "session_not_found" end } })
  same("one waiter, failed view: no focus", w, { "exec " .. BIN .. " waiting --json --session " .. S, "session.view " })
  check("failed view is the result", w.result.action == "failed" and w.result.error:find("session_not_found", 1, true), show(w.result))
end

do -- one waiter, the panel will not minimize: no focus behind it
  local w = waiting({ exec = who(one), calls = {
    ["session.view"] = view("w1", { { id = "w1", focused = "block:p", tiled = { "block:a" }, floating = { { id = "layer:p", block = "block:p" } } } }),
    ["session.set_layer_state"] = function() return nil, "bad_request" end,
  } })
  local got = steps(w)
  local focused = false
  for _, l in ipairs(got) do if l:find("^session%.focus_block") then focused = true end end
  check("one waiter, refused minimize: no focus", not focused and w.result.action == "failed", table.concat(got, " | "))
end

-- A Rex stub whose view follows move_block and float_block, so a move that
-- fails halfway leaves what Rex would leave.
local function rex_with_moves(float_fails)
  local wins = {
    { id = "w1", focused = "block:a", tiled = { "block:a" }, floating = { { id = "layer:p", block = "block:p", bounds = { 0.06, 0.06, 0.88, 0.88 } } } },
    { id = "w2", focused = "block:b", tiled = { "block:b" } },
  }
  local fails = float_fails
  return {
    ["session.view"] = function() return view("w2", wins) end,
    ["session.move_block"] = function(a)
      wins[1].floating = {}
      wins[2].tiled[#wins[2].tiled + 1] = a.block_id
      wins[2].labels = { [a.block_id] = "rex-amq-ui" }
      return { created_split_id = "split:1", revision = 8 }
    end,
    ["session.float_block"] = function(a)
      if fails > 0 then fails = fails - 1 return nil, "conflict" end
      for i, b in ipairs(wins[2].tiled) do if b == a.block_id then table.remove(wins[2].tiled, i) end end
      wins[2].floating = { { id = "layer:q", block = a.block_id, bounds = a.bounds } }
      return { layer_id = "layer:q", revision = 9 }
    end,
  }
end

do -- moved into the layout, the float refused: the next toggle repairs it, never opens a second panel
  local w = world({ calls = rex_with_moves(1), exec = function() return ran('{"layer_id":"layer:x","block_id":"block:x"}') end })
  w.plugin.setup{ bin = BIN }
  local first = w.actions.amqui_toggle.run({ session_id = S })
  check("a refused float is a failure", first.action == "failed" and first.error:find("conflict", 1, true), show(first))
  local n = #w.seq
  local second = w.actions.amqui_toggle.run({ session_id = S })
  local after = {}
  for i = n + 1, #w.seq do after[#after + 1] = steps(w)[i] end
  local want = {
    "session.view ",
    "session.float_block block_id=\"block:p\" bounds={1=0.57,2=0.02,3=0.42,4=0.96}",
    "session.set_layer_state focus=true layer_id=\"layer:q\" state=\"normal\"",
    "write block:p " .. q(RESUME),
  }
  local ok = #after == #want
  for i = 1, #want do ok = ok and after[i] == want[i] end
  check("a panel left in a layout is floated again, not opened twice", ok and second.layer_id == "layer:q",
    "\n  got:  " .. table.concat(after, "\n        "))
end

do -- a floating panel elsewhere wins over a labelled block in this tab's layout
  local v = view("w2", {
    { id = "w1", focused = "block:a", tiled = { "block:a" }, floating = { { id = "layer:p", block = "block:p", state = "minimized" } } },
    { id = "w2", focused = "block:b", tiled = { "block:b", "block:t" }, labels = { ["block:t"] = "rex-amq-ui" } } })
  local w = toggle({ calls = { ["session.view"] = v, ["session.float_block"] = { layer_id = "layer:q", revision = 4 } } })
  check("floating beats a stranded block", steps(w)[2] == "session.move_block anchor_block_id=\"block:b\" block_id=\"block:p\"", steps(w)[2])
end

do -- with board = true a stranded panel comes back as a Board
  local v = view("w1", { { id = "w1", focused = "block:a", tiled = { "block:a", "block:t" }, labels = { ["block:t"] = "rex-amq-ui" } } })
  local w = toggle({ calls = { ["session.view"] = v, ["session.float_block"] = { layer_id = "layer:t", revision = 4 } } }, { bin = BIN, board = true })
  check("stranded panel at Board size", steps(w)[2] == "session.float_block block_id=\"block:t\" bounds={1=0.06,2=0.06,3=0.88,4=0.88}", steps(w)[2])
end

do -- a stranded panel with focus is floated again, never "minimized" (a layout cannot be)
  local v = view("w1", { { id = "w1", focused = "block:t", tiled = { "block:a", "block:t" }, labels = { ["block:t"] = "rex-amq-ui" } } })
  local w = toggle({ calls = { ["session.view"] = v, ["session.float_block"] = { layer_id = "layer:t", revision = 4 } } })
  check("focused stranded panel is floated, not hidden", steps(w)[2] == "session.float_block block_id=\"block:t\" bounds={1=0.57,2=0.02,3=0.42,4=0.96}"
    and w.result.action == "moved", steps(w)[2])
end

do -- APC bytes are exactly the panel's grammar
  check("pause bytes", PAUSE == "\27_amqui;pause\27\\" and #PAUSE == 15)
end

os.getenv = real_getenv
if failures > 0 then
  print(failures .. " of " .. count .. " checks failed")
  os.exit(1)
end
print("ok " .. count .. " checks")

-- rex-amq-ui: the session's Crew and Cockpit panel in a Rex floating layer.
--
-- Load from your Rex init.lua, then `rex config check && rex config reload`:
--   dofile("/path/to/rex-amq-ui/lua/rex-amq-ui/init.lua").setup{ toggle_key = "cmd+shift+a", waiting_key = "cmd+shift+u" }
--
-- Two actions:
--   amqui_toggle   show the panel in the active tab (open it, restore it, or
--                  bring it over from another tab), or hide it when it is visible
--                  in the active tab
--   amqui_waiting  go to whoever waits on you: nobody, one member's pane, or
--                  the panel's alerts when several wait
--
-- The rex-amq-ui binary owns the decisions: `rex-amq-ui open` creates the
-- panel's layer, and `rex-amq-ui waiting` says who waits. This file only moves
-- the layer and tells the panel to pause and resume (its APC protocol).
local M = {}

local LABEL = "rex-amq-ui" -- the panel's block label; one panel per session
local ESC = "\27"
local EXEC_TIMEOUT = 20 -- seconds; `open` gives Rex 15, `waiting` the adapter 5
local DRAWER, BOARD = { 0.57, 0.02, 0.42, 0.96 }, { 0.06, 0.06, 0.88, 0.88 } -- internal/layer's sizes

local defaults = { toggle_key = "cmd+shift+a", waiting_key = "cmd+shift+u", bin = false, adapter_bin = false, board = false }
local checks = {
  toggle_key = { "a shortcut or false", function(v) return v == false or (type(v) == "string" and v ~= "") end },
  waiting_key = { "a shortcut or false", function(v) return v == false or (type(v) == "string" and v ~= "") end },
  bin = { "a path to the rex-amq-ui binary", function(v) return v == false or (type(v) == "string" and v ~= "") end },
  adapter_bin = { "a path to rex-amq-adapter", function(v) return v == false or (type(v) == "string" and v ~= "") end },
  board = { "true or false", function(v) return type(v) == "boolean" end },
}

local options = {}
local resolved_bin -- the binary found on the login shell's PATH

local function apc(verb)
  return ESC .. "_amqui;" .. verb .. ESC .. "\\"
end

local function trim(s)
  return (tostring(s or ""):gsub("^%s+", ""):gsub("%s+$", ""))
end

local function last_line(s)
  local line = ""
  for l in tostring(s or ""):gmatch("[^\n]+") do
    if trim(l) ~= "" then line = trim(l) end
  end
  return line
end

local function log(level, message)
  pcall(rex.log, level, "rex-amq-ui: " .. message)
end

-- A failed action is a log line and a result, never a raised error: Rex
-- reports a raised error to whoever pressed the key.
local function failed(message)
  log("error", message)
  return { action = "failed", error = message }
end

-- call is rex.call: the result, or nil and why.
local function call(method, args)
  local ok, result, err = pcall(rex.call, method, args)
  if not ok then return nil, method .. ": " .. tostring(result) end
  if result == nil then return nil, method .. ": " .. tostring(err or "no result") end
  return result
end

-- exec runs argv to completion: its stdout, or nil and why.
local function exec(argv)
  local ok, result, err = pcall(rex.exec, argv, { timeout = EXEC_TIMEOUT })
  if not ok then return nil, tostring(result) end
  if result == nil then return nil, tostring(err or "did not run") end
  if not result.ok then
    local why = trim(result.stderr)
    if why == "" then why = result.timed_out and "timed out" or ("exit " .. tostring(result.code)) end
    return nil, why
  end
  return result.stdout or ""
end

-- binary is the rex-amq-ui to run: the bin option, then $REX_AMQ_UI_BIN,
-- then rex-amq-ui on the login shell's PATH (Rex's own PATH lacks ~/go/bin
-- and the like).
local function binary()
  if options.bin then return options.bin end
  local env = os.getenv("REX_AMQ_UI_BIN")
  if env and env ~= "" then return env end
  if resolved_bin then return resolved_bin end
  local shell = os.getenv("SHELL")
  if not shell or shell == "" then shell = "/bin/sh" end
  local out = exec({ shell, "-l", "-c", "command -v rex-amq-ui" })
  local path = last_line(out)
  if path:sub(1, 1) ~= "/" then
    return nil, "rex-amq-ui is not on your login shell's PATH: set bin in setup{} or REX_AMQ_UI_BIN"
  end
  resolved_bin = path
  return path
end

-- run runs `rex-amq-ui <args> --session S [--adapter-bin P]` and decodes the
-- one JSON line it prints.
local function run(session, args)
  local bin, err = binary()
  if not bin then return nil, err end
  local argv = { bin }
  for _, a in ipairs(args) do argv[#argv + 1] = a end
  argv[#argv + 1] = "--session"
  argv[#argv + 1] = session
  if options.adapter_bin then
    argv[#argv + 1] = "--adapter-bin"
    argv[#argv + 1] = options.adapter_bin
  end
  local out, why = exec(argv)
  if not out then
    -- The binary names itself in its errors; a failure to run it does not.
    if not why:find("^rex%-amq%-ui ") then why = "rex-amq-ui " .. args[1] .. ": " .. why end
    return nil, why
  end
  local value = rex.json.decode(last_line(out))
  if type(value) ~= "table" then
    return nil, "rex-amq-ui " .. args[1] .. ": unreadable output " .. string.format("%q", last_line(out))
  end
  return value
end

-- find_panel is the panel's block in view: in a floating layer, preferring
-- the active window's (the rule `rex-amq-ui open` uses). Failing that, a
-- panel block left in a window's layout by a move that broke off halfway
-- (floating = false), so the plugin floats it again instead of opening a
-- second panel.
local function find_panel(view)
  local found, best = nil, -1
  for _, w in ipairs(view.windows or {}) do
    for _, l in ipairs(w.layers or {}) do
      for _, b in ipairs(l.blocks or {}) do
        if b.label == LABEL then
          local floating = l.kind == "floating"
          local rank = (floating and 2 or 0) + (w.window_id == view.active_window_id and 1 or 0)
          if rank > best then
            found, best = { window = w, layer = l, block = b.block_id, floating = floating }, rank
          end
        end
      end
    end
  end
  return found
end

local function window_of(view, id)
  for _, w in ipairs(view.windows or {}) do
    if w.window_id == id then return w end
  end
end

-- tiled_anchor is a block of window's tiled layout to move a block beside:
-- its focused block when that is tiled, else the layout's first.
local function tiled_anchor(window)
  local tiled = window and window.layers and window.layers[1]
  if not tiled or tiled.kind ~= "tiled" then return nil end
  local first
  for _, b in ipairs(tiled.blocks or {}) do
    if b.block_id == window.focused_block_id then return b.block_id end
    first = first or b.block_id
  end
  return first
end

local function write(session, block, data)
  return call("block.method", { session_id = session, block_id = block, method = "write", args = { data = data } })
end

local function is_up(panel)
  return panel.floating and panel.layer.state ~= "minimized"
end

-- hide pauses the panel, then minimizes its layer; Rex gives focus back to
-- the pane used before it.
local function hide(session, panel)
  local _, paused_err = write(session, panel.block, apc("pause"))
  if paused_err then log("warn", paused_err) end -- the panel also pauses when it sees its layer minimized
  local ok, why = call("session.set_layer_state", { session_id = session, layer_id = panel.layer.layer_id, state = "minimized" })
  if not ok then
    -- The layer stays up: a panel left paused there would look frozen, and
    -- no minimized-to-normal edge would ever wake it.
    if not paused_err then
      local _, resume_err = write(session, panel.block, apc("resume"))
      if resume_err then why = why .. "; resuming the panel failed too: " .. resume_err end
    end
    return failed(why)
  end
  return { action = "minimized", layer_id = panel.layer.layer_id, block_id = panel.block }
end

-- bring_here floats the panel in the active tab: from another tab it goes
-- into the active window's layout first (move_block), then out into a new
-- floating layer at the size it had (float_block). A panel already in a
-- layout, where a move that broke off left it, gets the default size. The
-- panel finds its new layer by itself.
local function bring_here(session, view, panel)
  if panel.window.window_id ~= view.active_window_id then
    local anchor = tiled_anchor(window_of(view, view.active_window_id))
    if not anchor then return nil, "the active tab has no pane to bring the panel beside" end
    local ok, err = call("session.move_block", { session_id = session, block_id = panel.block, anchor_block_id = anchor })
    if not ok then return nil, err end
  end
  local bounds = panel.floating and panel.layer.bounds or (options.board and BOARD or DRAWER)
  local floated, err = call("session.float_block", { session_id = session, block_id = panel.block, bounds = bounds })
  if not floated then
    -- The next toggle finds the panel in the layout and floats it again.
    return nil, err
  end
  return floated.layer_id
end

-- show makes the panel the active tab's focused layer, showing tab when
-- given: opened by the binary when there is none, else restored here.
local function show(session, view, tab)
  local panel = find_panel(view)
  if not panel then
    local args = { "open" }
    if tab then
      args[#args + 1] = "--tab"
      args[#args + 1] = tab
    end
    if options.board then args[#args + 1] = "--board" end
    local opened, err = run(session, args)
    if not opened then return failed(err) end
    return { action = "opened", layer_id = opened.layer_id, block_id = opened.block_id }
  end
  local layer, action = panel.layer.layer_id, "restored"
  if not panel.floating or panel.window.window_id ~= view.active_window_id then
    local err
    layer, err = bring_here(session, view, panel)
    if not layer then return failed(err) end
    action = "moved"
  end
  local ok, err = call("session.set_layer_state", { session_id = session, layer_id = layer, state = "normal", focus = true })
  if not ok then return failed(err) end
  local say = apc("resume")
  if tab then say = say .. apc("tab:" .. tab) end
  local _, werr = write(session, panel.block, say)
  if werr then log("warn", werr) end -- any key resumes the panel too
  return { action = action, layer_id = layer, block_id = panel.block }
end

local function view_of(session)
  return call("session.view", { session_id = session })
end

function M.toggle(ctx)
  local session = ctx and ctx.session_id
  if not session then return failed("no session to act on") end
  local view, err = view_of(session)
  if not view then return failed(err) end
  local panel = find_panel(view)
  if panel and is_up(panel) and panel.window.window_id == view.active_window_id then
    return hide(session, panel)
  end
  return show(session, view, nil)
end

function M.waiting(ctx)
  local session = ctx and ctx.session_id
  if not session then return failed("no session to act on") end
  local who, err = run(session, { "waiting", "--json" })
  if not who then return failed(err) end
  local members = who.members or {}
  if #members == 0 then
    log("info", "nobody waits")
    return { action = "nobody" }
  end
  local view
  view, err = view_of(session)
  if not view then return failed(err) end
  if #members == 1 and members[1].block_id then
    -- Focus only once the panel is out of the way, as the panel's own Enter does.
    local panel = find_panel(view)
    if panel and is_up(panel) then
      local hidden = hide(session, panel)
      if hidden.action ~= "minimized" then return hidden end
    end
    local ok, why = call("session.focus_block", { session_id = session, block_id = members[1].block_id })
    if not ok then return failed(why) end
    return { action = "focused", handle = members[1].handle, block_id = members[1].block_id }
  end
  return show(session, view, "overview")
end

-- guarded runs an action so that nothing it raises reaches Rex.
local function guarded(fn)
  return function(ctx)
    local ok, result = pcall(fn, ctx)
    if not ok then return failed(tostring(result)) end
    return result
  end
end

function M.setup(user)
  assert(user == nil or type(user) == "table", "rex-amq-ui: setup takes a table of options")
  options, resolved_bin = {}, nil
  for key, value in pairs(defaults) do options[key] = value end
  for key, value in pairs(user or {}) do
    local check = checks[key]
    assert(check, "rex-amq-ui: unknown option " .. tostring(key))
    assert(check[2](value), "rex-amq-ui: " .. key .. " must be " .. check[1])
    options[key] = value
  end

  rex.action{
    name = "amqui_toggle",
    title = "Toggle rex-amq-ui",
    description = "Show the session's rex-amq-ui panel in this tab, or hide it",
    category = "Agents",
    keywords = "amq crew cockpit panel agents mail",
    run = guarded(M.toggle),
  }
  rex.action{
    name = "amqui_waiting",
    title = "Go to Who Waits",
    description = "Focus the one member waiting on you, or show the panel's alerts when several wait",
    category = "Agents",
    keywords = "amq waiting blocked attention human",
    run = guarded(M.waiting),
  }
  if options.toggle_key then rex.bind(options.toggle_key, "amqui_toggle") end
  if options.waiting_key then rex.bind(options.waiting_key, "amqui_waiting") end
  return M
end

return M

# WRAPI — Wrapper API for Google AI Mode

Localhost API that queries **Google AI Mode** (`udm=50`) through a real
Chromium driven by Playwright and speaks both a native JSON API and the
**OpenAI `/v1/chat/completions` format** (SSE streaming included) — so any
harness, TUI or script that talks to OpenAI-style endpoints can use it as an
extra "local model".

```
prompt → WRAPI (127.0.0.1:8787) → Playwright → google.com/search?q=…&udm=50&csuir=1 → JSON/SSE
```

## Quick start

```sh
go build -o wrapi ./cmd/wrapi
cp wrapi ~/.local/bin/                 # one-time install
systemctl --user enable --now wrapi.service   # or: wrapi serve
```

The systemd user unit (`~/.config/systemd/user/wrapi.service`) starts the
server on login, parks the browser silently on workspace 8 and restarts it
on failure.

## Subcommands

| Command            | Description |
|--------------------|-------------|
| `wrapi say "…"`    | ask in the **current session** (remembers it between calls, auto-continues the conversation) |
| `wrapi ask "…"`    | one-shot query in a fresh session (`-sources` adds URLs) |
| `wrapi chat`       | interactive REPL over one conversation session (`-new` = fresh) |
| `wrapi pipe`       | JSONL bridge: `{"prompt":…}` lines in, result objects out |
| `wrapi new`        | start a new session, make it current, print its id |
| `wrapi clear`      | reset the current (or `-session`) conversation |
| `wrapi sessions`   | list sessions (`*` marks the current one) |
| `wrapi rm ID`      | delete a session |
| `wrapi window`     | show where the browser window lives |
| `wrapi window MODE`| re-place it: `workspace` \| `mini` \| `visible` |
| `wrapi status`     | service + engine + window + sessions overview |
| `wrapi logs [-n N]`| service journal |
| `wrapi start/stop/restart/enable/disable/is-active` | systemd control |
| `wrapi serve`      | run the HTTP server in the foreground (default without args) |

`say` keeps its current session in `~/.local/state/wrapi/session`; pass
`-session NAME` to target any session explicitly.

## HTTP API

### OpenAI-compatible

| Endpoint                  | Description |
|---------------------------|-------------|
| `GET  /v1/models`         | model list (`wrapi-google-ai`) |
| `POST /v1/chat/completions` | standard fields (`model`, `messages`, `stream`); WRAPI extras: `session` — conversation id. Without a session each request flattens the transcript into one fresh query; with a session the conversation lives on Google's side via follow-ups (auto-detected). |

Streaming: OpenAI SSE chunks with content deltas, final chunk carries a
`wrapi` field (sources, session, mode), then `data: [DONE]`.

```sh
curl -s http://127.0.0.1:8787/v1/chat/completions -d '{
  "model": "wrapi-google-ai",
  "messages": [{"role":"user","content":"What is the capital of Poland?"}]
}'
```

### Native

| Endpoint | Description |
|----------|-------------|
| `POST /v1/ask`          | `{"prompt", "session", "stream", "timeout_s"}` — fresh query |
| `POST /v1/say`          | smart routing: fresh query on an empty session, follow-up otherwise |
| `POST /v1/followup`     | follow-up in the session's conversation |
| `GET/POST/DELETE /v1/sessions` | list / create / delete conversation sessions |
| `POST /v1/reset?session=ID` | clear a session page |
| `GET  /v1/window`, `POST /v1/window` | inspect / re-place the browser window (`{"mode": "mini"}`) |
| `GET  /v1/screenshot?session=ID`, `GET /v1/page.html?session=ID` | debug |

Native streaming (`"stream": true`) emits NDJSON: `{"delta": "…"}` lines,
terminated by one full result object.

Answer shape (native):

```json
{
  "answer": "The capital of Poland is Warsaw …",
  "sources": [
    { "site": "Wikipedia", "title": "Warsaw - Wikipedia", "url": "https://en.wikipedia.org/wiki/Warsaw", "snippet": "…" }
  ],
  "url": "https://www.google.com/search?q=…&udm=50&csuir=1&hl=en",
  "mode": "fresh",
  "session": "default",
  "elapsed_ms": 3400
}
```

Errors: `403` + `{"error": "… /sorry/ wall …"}` when Google bot-walls the
request, `4xx/5xx` otherwise.

## Window modes (Hyprland)

The browser window is tagged with WM class `wrapi` and placed via
`hyprctl eval` (Hyprland ≥ 0.57 Lua IPC). Without Hyprland the window simply
opens normally.

| Mode (`-mode`) | Behaviour |
|----------------|-----------|
| `workspace` *(default)* | parked **silently** on workspace 8 (`-workspace N`) — never visible, your active workspace is not switched |
| `mini` | floated as a tiny bar in the corner of the focused monitor (`-mini-width`/`-mini-height`, default 500×120). Chromium's own minimum width is ~500px, so 10×10 is not physically possible; height can be tiny. **Only sticks when the browser launches while that workspace is focused** — Hyprland's float dispatch no-ops on inactive workspaces, so at runtime `wrapi window mini` safely falls back to workspace mode |
| `visible` | deterministic: moved (non-silently) to workspace 8 and focused |
| `headless` | no window at all — **experimental, Google bot-walls headless** |

Env equivalents: `WRAPI_MODE`, `WRAPI_WORKSPACE`, `WRAPI_MINI_WIDTH`,
`WRAPI_MINI_HEIGHT`. Other settings: `WRAPI_ADDR`, `WRAPI_PROFILE_DIR`,
`WRAPI_TIMEOUT`, `WRAPI_HL`.

## Anti-bot notes

- Headed Chromium with `--disable-blink-features=AutomationControlled`
  (no `navigator.webdriver`); the profile at `data/profile` keeps cookies
  and clearance across restarts.
- Google cookie-consent dialog ("Before you continue to Google") is
  auto-dismissed on fresh profiles (prefers *Reject all*).
- Headless gets walled; headed does not. The `/sorry/` wall can also hit
  the IP after heavy use — it decays after a while.
- If a wall/captcha needs manual solving: in `visible` mode the window is
  on workspace 8, switch there and click through once — the profile keeps
  the clearance.

## Extraction ground truth

Probed against the live DOM (see `tools/probe*.mjs`):

- AI panes: `div[data-subtree="aimc"]` — follow-ups spawn **new** panes;
  the newest content is always in the last pane, last
  `[data-scope-id="turn"]` (when markers exist; the engine falls back to
  text-growth detection otherwise)
- Answer body: `[data-container-id="main-col"]`
- Inline citations: `a.gQWhZ`; source cards: `li.Ioqnib`
- Encrypted `/goto?url=…` links are resolved to real URLs through the
  browser's request context (redirect-following)

If Google changes the DOM, dump the live page with `/v1/page.html` and
adjust the selectors in `internal/engine/engine.go`.

## Layout

```
cmd/wrapi/          entry point: serve / ask / chat / pipe
internal/engine/    Playwright lifecycle, Hyprland placement, sessions, extraction
internal/server/    native + OpenAI-compatible HTTP handlers
tools/              DOM / Hyprland probe scripts (dev-only)
data/profile/       browser profile (gitignored)
```

## License

MIT — see [LICENSE](LICENSE).

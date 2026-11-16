# WRAPI — Wrapper API for Google AI Mode

Localhost HTTP API that queries **Google AI Mode** (`udm=50`) through a real
Chromium driven by Playwright, and returns the AI answer + sources as JSON.
Any local script or program can use it — no Google API keys involved.

```
prompt → WRAPI (127.0.0.1:8787) → Playwright → google.com/search?q=…&udm=50&csuir=1 → JSON
```

## Quick start

```sh
go build -o wrapi ./cmd/wrapi
./wrapi                          # listens on 127.0.0.1:8787
```

On first start the Playwright driver is fetched automatically; the bundled
Chromium is reused from `~/.cache/ms-playwright` if present.

## API

| Endpoint              | Method | Body                                | Description |
|-----------------------|--------|-------------------------------------|-------------|
| `/health`             | GET    | —                                   | liveness |
| `/v1/ask`             | POST   | `{"prompt": "…", "fresh": true, "timeout_s": 90}` | one AI Mode query (fresh search session by default) |
| `/v1/followup`        | POST   | `{"prompt": "…"}`                   | follow-up through the on-page input, keeps conversation context (experimental) |
| `/v1/reset`           | POST   | —                                   | close all pages, keep cookies/profile |
| `/v1/screenshot`      | GET    | —                                   | PNG of the current page (debug) |
| `/v1/page.html`       | GET    | —                                   | current DOM of the last page (debug, for selector tuning) |

Example:

```sh
curl -s http://127.0.0.1:8787/v1/ask -d '{"prompt":"What is the capital of Poland?"}'
```

Response shape:

```json
{
  "answer": "The capital of Poland is Warsaw (Polish: Warszawa). …",
  "sources": [
    { "site": "Wikipedia", "title": "Warsaw - Wikipedia", "snippet": "…", "url": "https://en.wikipedia.org/wiki/Warsaw" }
  ],
  "url": "https://www.google.com/search?q=…&udm=50&csuir=1&hl=en",
  "mode": "fresh",
  "elapsed_ms": 9421
}
```

Errors: `403` with `{"error": "… /sorry/ wall …"}` when Google bot-walls the
request, `500` with details otherwise.

## Configuration (env vars)

| Var               | Default          | Meaning |
|-------------------|------------------|---------|
| `WRAPI_ADDR`      | `127.0.0.1:8787` | listen address |
| `WRAPI_PROFILE_DIR` | `data/profile` | persistent Chromium profile (cookies survive restarts) |
| `WRAPI_HEADLESS`  | `false`          | headless Chromium **gets bot-walled by Google** — leave off |
| `WRAPI_HIDE_WINDOW` | `true`         | headed browser parked off-screen (`--window-position=-32000,-32000`) |
| `WRAPI_HL`        | `en`             | `hl=` UI language parameter |
| `WRAPI_TIMEOUT`   | `90s`            | default per-request timeout |

## Why headed mode

Google serves the `/sorry/` bot wall to headless browsers. WRAPI runs a real
headed Chromium with `--disable-blink-features=AutomationControlled` (so
`navigator.webdriver` is not exposed) and parks the window off-screen, which
keeps the browser fingerprint genuine while staying invisible. If you still
get walled, run `WRAPI_HIDE_WINDOW=0 ./wrapi`, trigger one query and solve
the captcha manually — the persistent profile keeps the clearance.

## Extraction notes

Selector ground truth (probed 2026-11-17, see `tools/probe*.mjs`):

- AI answer pane: `div[data-subtree="aimc"]`
- Answer body: `[data-container-id="main-col"]`
- Completion signal: `[data-complete="true"]` (streaming marker)
- Inline citations: `a.gQWhZ` (encrypted `/goto?url=…` links, resolved via
  the browser's request context with redirect-following)
- Source cards: `li.Ioqnib` (site name, title, snippet)

If Google changes the DOM, check `/v1/page.html` after a failing ask and
update the selectors in `internal/engine/engine.go`.

## Layout

```
cmd/wrapi/          entry point (env config, HTTP server wiring)
internal/engine/    Playwright lifecycle, query + extraction logic
internal/server/    JSON HTTP handlers
tools/              DOM probe scripts (Node + Playwright, dev-only)
data/profile/       browser profile (gitignored)
```

## License

MIT — see [LICENSE](LICENSE).

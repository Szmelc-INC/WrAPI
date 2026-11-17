// Package engine drives a real Chromium via Playwright against Google AI
// Mode (https://www.google.com/search?q=PROMPT&udm=50&csuir=1) and extracts
// the AI answer together with its sources.
//
// Window placement goes through Hyprland IPC (hyprctl eval, Lua API): the
// browser is tagged with WM class "wrapi" and then either parked silently on
// a dedicated workspace (default), floated as a tiny corner bar, or shown
// normally. Headless exists but Google bot-walls it.
package engine

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	pw "github.com/mxschmitt/playwright-go"
)

// ErrBlocked is returned when Google serves its automation /sorry/ wall.
var ErrBlocked = errors.New("google blocked the request (/sorry/ wall) - try clearing the profile or solving the captcha once in headed mode")

// Window modes.
const (
	ModeWorkspace = "workspace" // silent park on a dedicated workspace (default)
	ModeMini      = "mini"      // floated tiny bar in the corner of the focused monitor
	ModeVisible   = "visible"   // normal window on the dedicated workspace (focused)
	ModeHeadless  = "headless"  // no window at all (experimental: Google walls this)
)

// WindowClass tags the browser window for hyprctl targeting.
const WindowClass = "wrapi"

const (
	aiModeSelector = `div[data-subtree="aimc"]`
	// Candidate probe: main-col of the last turn of the LAST aimc pane
	// (follow-ups append turns and spawn new panes — freshest content is
	// always last).
	candidateJS = `() => {
		const p = document.querySelectorAll('div[data-subtree="aimc"]');
		const aimc = p.length ? p[p.length-1] : null;
		if (!aimc) return { ready: false };
		const turns = aimc.querySelectorAll('[data-scope-id="turn"]');
		const scope = turns.length ? turns[turns.length - 1] : aimc;
		const main = scope.querySelector('[data-container-id="main-col"]') || scope;
		return { ready: true, text: main.innerText, complete: !!scope.querySelector('[data-complete="true"]') };
	}`
	turnCountJS = `() => document.querySelectorAll('div[data-subtree="aimc"] [data-scope-id="turn"]').length`
	aimcTextJS  = `() => { const p = document.querySelectorAll('div[data-subtree="aimc"]'); const a = p.length ? p[p.length-1] : null; return a ? a.innerText : ''; }`
	// Final extraction: answer text plus every source entry on the page.
	extractJS = `() => {
		const p = document.querySelectorAll('div[data-subtree="aimc"]');
		const aimc = p.length ? p[p.length-1] : null;
		if (!aimc) return null;
		const turns = aimc.querySelectorAll('[data-scope-id="turn"]');
		const scope = turns.length ? turns[turns.length - 1] : aimc;
		const main = scope.querySelector('[data-container-id="main-col"]') || scope;
		const out = { answer: main.innerText, sources: [], turns: turns.length };
		const seen = new Set();
		const lines = t => (t || '').split('\n').map(s => s.trim()).filter(Boolean);
		const add = (a, site, title, snippet) => {
			const href = a.getAttribute('href') || '';
			if (!href) return;
			if (seen.has(href)) return;
			seen.add(href);
			out.sources.push({ site: (site || '').trim(), title: (title || '').trim(), snippet: (snippet || '').trim(), href });
		};
		scope.querySelectorAll('li.Ioqnib').forEach(li => {
			const a = li.querySelector('a[href^="/goto"]');
			if (!a) return;
			const l = lines(li.innerText);
			add(a, l[0] || '', l[1] || '', l.slice(2).join(' '));
		});
		scope.querySelectorAll('a.gQWhZ').forEach(a => {
			const l = lines(a.innerText);
			add(a, l[0] || '', l[1] || '', l.slice(2).join(' '));
		});
		return out;
	}`
	// The follow-up composer, scoped to the AI Mode input plate.
	inputSel = `[data-xid="aim-mars-input-plate"] textarea, textarea[aria-label="Ask anything"]`
)

// Source is a single citation from the AI answer.
type Source struct {
	Site    string `json:"site,omitempty"`
	Title   string `json:"title,omitempty"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

// Answer is the result of one AI Mode query.
type Answer struct {
	Answer  string   `json:"answer"`
	Sources []Source `json:"sources"`
	URL     string   `json:"url"`
	Mode    string   `json:"mode"` // "fresh" or "followup"
}

// Config controls the engine. Values come from env vars / CLI flags.
type Config struct {
	Addr       string
	ProfileDir string
	WindowMode string // workspace | mini | visible | headless
	Workspace  int    // dedicated workspace number for workspace/visible modes
	MiniWidth  int
	MiniHeight int
	HL         string // hl= URL parameter (UI language)
	Timeout    time.Duration
}

// Engine owns the browser process and the session table.
type Engine struct {
	cfg      Config
	mu       sync.Mutex // guards sessions and browser lifecycle
	pw       *pw.Playwright
	ctx      pw.BrowserContext
	sessions map[string]*Session
}

// Session is one conversation: a dedicated browser page plus its state.
type Session struct {
	ID      string
	page    pw.Page
	mu      sync.Mutex // serializes asks on this session
	URL     string
	Turns   int
	Created time.Time
}

// Start launches the persistent browser context, places its window
// according to cfg.WindowMode and creates the default session.
func Start(cfg Config) (*Engine, error) {
	if err := pw.Install(); err != nil {
		return nil, fmt.Errorf("playwright driver install: %w", err)
	}
	e := &Engine{cfg: cfg, sessions: map[string]*Session{}}
	if err := e.launch(); err != nil {
		return nil, err
	}
	if _, err := e.NewSession("default"); err != nil {
		_ = e.Close()
		return nil, err
	}
	return e, nil
}

func (e *Engine) launch() error {
	var err error
	e.pw, err = pw.Run()
	if err != nil {
		return fmt.Errorf("starting playwright driver: %w", err)
	}
	if err := os.MkdirAll(e.cfg.ProfileDir, 0o700); err != nil {
		return err
	}
	abs, err := filepath.Abs(e.cfg.ProfileDir)
	if err != nil {
		abs = e.cfg.ProfileDir
	}
	args := []string{
		"--no-first-run",
		"--no-default-browser-check",
		// Google serves the /sorry/ wall when navigator.webdriver is exposed.
		"--disable-blink-features=AutomationControlled",
		"--class=" + WindowClass,
	}
	headless := e.cfg.WindowMode == ModeHeadless
	opts := pw.BrowserTypeLaunchPersistentContextOptions{
		Headless:          pw.Bool(headless),
		Locale:            pw.String("en-US"),
		Args:              args,
		IgnoreDefaultArgs: []string{"--enable-automation"},
	}
	e.ctx, err = e.pw.Chromium.LaunchPersistentContext(abs, opts)
	if err != nil {
		return fmt.Errorf("launching chromium: %w", err)
	}
	if !headless {
		e.placeWindow()
	}
	return nil
}

// placeWindow moves the just-launched browser window via Hyprland IPC.
// Chromium keeps resizing itself for a moment after startup (Playwright
// viewport emulation), so geometry is applied verify-and-retry style.
// Failure is logged, not fatal: without hyprctl the window simply opens
// where the compositor puts it.
func (e *Engine) placeWindow() {
	addr, err := waitWindowByClass(WindowClass, 15*time.Second)
	if err != nil {
		log.Printf("window placement skipped: %v", err)
		return
	}
	switch e.cfg.WindowMode {
	case ModeMini:
		x, y, ok := monitorCorner(e.cfg.MiniWidth, e.cfg.MiniHeight)
		for attempt := 0; attempt < 5; attempt++ {
			time.Sleep(700 * time.Millisecond)
			_ = hyprEval(fmt.Sprintf(`hl.dispatch(hl.dsp.window.float({address='%s'}))`, addr))
			_ = hyprEval(fmt.Sprintf(`hl.dispatch(hl.dsp.window.resize({address='%s', x=%d, y=%d}))`, addr, e.cfg.MiniWidth, e.cfg.MiniHeight))
			if ok {
				_ = hyprEval(fmt.Sprintf(`hl.dispatch(hl.dsp.window.move({address='%s', x=%d, y=%d}))`, addr, x, y))
			}
			if win, err := windowByAddress(addr); err == nil && win.Floating &&
				near(win.Size[0], e.cfg.MiniWidth) && near(win.Size[1], e.cfg.MiniHeight) &&
				(!ok || (near(win.At[0], x) && near(win.At[1], y))) {
				return // settled
			}
		}
		log.Printf("mini placement did not fully settle (chromium/compositor race)")
	case ModeVisible:
		// Focused (non-silent) move: the workspace is switched to, so the
		// window is actually seen; deterministic, never under the cursor.
		if err := hyprEval(fmt.Sprintf(`hl.dispatch(hl.dsp.window.move({address='%s', workspace=%d, silent=false}))`, addr, e.cfg.Workspace)); err != nil {
			log.Printf("visible placement failed: %v", err)
		}
	default: // workspace
		for attempt := 0; attempt < 4; attempt++ {
			if err := hyprEval(fmt.Sprintf(`hl.dispatch(hl.dsp.window.move({address='%s', workspace=%d, silent=true}))`, addr, e.cfg.Workspace)); err != nil {
				log.Printf("workspace placement failed: %v", err)
				return
			}
			time.Sleep(500 * time.Millisecond)
			if win, err := windowByAddress(addr); err == nil && win.Workspace.ID == e.cfg.Workspace {
				return
			}
		}
	}
}

func near(a, b int) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= 4
}

// NewSession creates a session with the given id (random when empty).
func (e *Engine) NewSession(id string) (*Session, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if id == "" {
		id = randID()
	}
	if _, exists := e.sessions[id]; exists {
		return e.sessions[id], nil
	}
	page, err := e.ctx.NewPage()
	if err != nil {
		return nil, fmt.Errorf("opening page: %w", err)
	}
	s := &Session{ID: id, page: page, Created: time.Now()}
	e.sessions[id] = s
	return s, nil
}

// Session returns the session with the given id (creating "default" when
// missing). Unknown ids return an error so callers can distinguish.
func (e *Engine) Session(id string) (*Session, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if s, ok := e.sessions[id]; ok {
		return s, nil
	}
	if id == "default" {
		page, err := e.ctx.NewPage()
		if err != nil {
			return nil, err
		}
		s := &Session{ID: id, page: page, Created: time.Now()}
		e.sessions[id] = s
		return s, nil
	}
	return nil, fmt.Errorf("session %q not found", id)
}

// Sessions lists session metadata (for GET /v1/sessions).
func (e *Engine) Sessions() []map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]map[string]any, 0, len(e.sessions))
	for _, s := range e.sessions {
		out = append(out, map[string]any{
			"id": s.ID, "turns": s.Turns, "url": s.URL,
			"created": s.Created.UTC().Format(time.RFC3339),
		})
	}
	return out
}

// DeleteSession closes the session page and drops it. The default session
// is reset instead of removed.
func (e *Engine) DeleteSession(id string) error {
	e.mu.Lock()
	s, ok := e.sessions[id]
	if ok {
		delete(e.sessions, id)
	}
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	if id == "default" {
		return s.Reset()
	}
	return s.page.Close()
}

// Close shuts the browser and the playwright driver down.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx != nil {
		_ = e.ctx.Close()
	}
	if e.pw != nil {
		_ = e.pw.Stop()
	}
	return nil
}

func (e *Engine) searchURL(prompt string) string {
	return "https://www.google.com/search?q=" + url.QueryEscape(prompt) +
		"&udm=50&csuir=1&hl=" + url.QueryEscape(e.cfg.HL)
}

// Ask runs a fresh AI Mode query in the session's page (a new q= URL resets
// the conversation). onDelta, when non-nil, receives streaming text chunks.
func (s *Session) Ask(eng *Engine, prompt string, timeout time.Duration, onDelta func(string)) (*Answer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.page.Goto(eng.searchURL(prompt), pw.PageGotoOptions{
		WaitUntil: pw.WaitUntilStateDomcontentloaded,
		Timeout:   pw.Float(float64(timeout.Milliseconds())),
	}); err != nil {
		return nil, fmt.Errorf("goto: %w", err)
	}
	if strings.Contains(s.page.URL(), "/sorry/") {
		return nil, ErrBlocked
	}
	eng.dismissConsent(s.page)
	if _, err := s.page.WaitForSelector(aiModeSelector, pw.PageWaitForSelectorOptions{
		Timeout: pw.Float(float64(timeout.Milliseconds())),
		State:   pw.WaitForSelectorStateVisible,
	}); err != nil {
		return nil, fmt.Errorf("ai mode pane did not appear (page: %s): %w", s.page.URL(), err)
	}
	if err := s.streamAnswer(s.page, timeout, "", onDelta); err != nil {
		return nil, err
	}
	return s.finish(eng, "fresh", prompt, "")
}

// FollowUp sends a follow-up question through the on-page composer of the
// session's page, keeping the conversation context. onDelta streams chunks.
func (s *Session) FollowUp(eng *Engine, prompt string, timeout time.Duration, onDelta func(string)) (*Answer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.page.WaitForSelector(inputSel, pw.PageWaitForSelectorOptions{
		Timeout: pw.Float(10000),
		State:   pw.WaitForSelectorStateVisible,
	}); err != nil {
		return nil, fmt.Errorf("follow-up input not found (run a fresh ask first): %w", err)
	}
	// The composer only accepts input when focused and needs a beat after
	// fill before Enter registers (mirrors manual interaction).
	comp := s.page.Locator(inputSel).First()
	if err := comp.Click(); err != nil {
		return nil, err
	}
	if err := comp.Fill(prompt); err != nil {
		return nil, err
	}
	time.Sleep(400 * time.Millisecond)
	turnsBefore := countTurns(s.page)
	baseline := aimcText(s.page)
	if err := comp.Press("Enter"); err != nil {
		return nil, err
	}
	// Enter sometimes does not submit (focus, composer state). Fall back to
	// the Send button. Turn markers may be absent and the question bubble
	// lives outside the aimc subtree, so aimc text change is the reliable
	// signal that submission happened.
	if !waitSubmitted(s.page, turnsBefore, 6*time.Second, prompt, baseline) {
		send := s.page.Locator(`[data-xid="input-plate-send-button"]`).First()
		if btn, err := send.ElementHandle(); err == nil && btn != nil {
			if v, err := btn.Evaluate("el => !el.disabled"); err == nil {
				if enabled, ok := v.(bool); ok && enabled {
					_ = send.Click()
				}
			}
		}
		if !waitSubmitted(s.page, turnsBefore, 8*time.Second, prompt, baseline) {
			return nil, fmt.Errorf("follow-up was not submitted (no new answer appeared)")
		}
	}
	if err := s.streamAnswer(s.page, timeout, baseline, onDelta); err != nil {
		return nil, err
	}
	return s.finish(eng, "followup", prompt, baseline)
}

// Reset clears the session page and state, keeping the page alive.
func (s *Session) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.page.Goto("about:blank"); err != nil {
		return err
	}
	s.URL, s.Turns = "", 0
	return nil
}

// ScreenshotPNG returns a PNG of the session page (debug endpoint).
func (s *Session) ScreenshotPNG() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.page.Screenshot(pw.PageScreenshotOptions{FullPage: pw.Bool(false)})
}

// PageHTML returns the current DOM of the session page (debug endpoint).
func (s *Session) PageHTML() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.page.Content()
}

// streamAnswer polls the candidate answer until it stops growing (and the
// turn is marked complete when Google sets that attribute), invoking
// onDelta with each text growth. baseline is the pre-submit aimc text for
// follow-ups: streaming only starts once the candidate diverges from it.
func (s *Session) streamAnswer(page pw.Page, timeout time.Duration, baseline string, onDelta func(string)) error {
	deadline := time.Now().Add(timeout)
	started := baseline == ""
	emitted := ""
	stable, lastLen := 0, -1
	for time.Now().Before(deadline) {
		time.Sleep(700 * time.Millisecond)
		raw, err := page.Evaluate(candidateJS)
		if err != nil {
			return fmt.Errorf("candidate probe: %w", err)
		}
		m, _ := raw.(map[string]interface{})
		if m == nil || m["ready"] != true {
			continue
		}
		text := toString(m["text"])
		if !started {
			if text != baseline && len(text) > 0 {
				started = true
				emitted = ""
			} else {
				continue
			}
		}
		if onDelta != nil && strings.HasPrefix(text, emitted) && len(text) > len(emitted) {
			onDelta(runeSafeSlice(text, len(emitted)))
			emitted = text
		}
		length := len(text)
		if length == lastLen {
			stable++
		} else {
			stable = 0
		}
		lastLen = length
		complete, _ := m["complete"].(bool)
		// Two consecutive polls with no growth: done. When Google marks
		// the turn complete a single stable poll is enough.
		if (complete && stable >= 1) || stable >= 2 {
			return nil
		}
	}
	if lastLen <= 0 {
		return fmt.Errorf("answer never rendered (timeout %s)", timeout)
	}
	return nil // best effort on deadline
}

// finish runs the final extraction and normalizes the answer.
func (s *Session) finish(eng *Engine, mode, prompt, baseline string) (*Answer, error) {
	raw, err := s.page.Evaluate(extractJS)
	if err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}
	s.URL = s.page.URL()

	m, ok := raw.(map[string]interface{})
	if !ok || m == nil {
		return nil, fmt.Errorf("extraction returned no data")
	}
	answer := strings.TrimSpace(toString(m["answer"]))
	turns := int(toFloat(m["turns"]))
	s.Turns += 1
	if mode == "followup" && turns == 0 {
		// DOM without turn markers: the whole conversation is in the answer
		// text — keep only what is new: content after the echo of the
		// current prompt, or after the longest common prefix with the
		// pre-submit baseline.
		echo := "AI Mode reply for " + prompt
		if i := strings.LastIndex(answer, echo); i >= 0 {
			answer = answer[i+len(echo):]
		} else if baseline != "" {
			n := commonPrefixLen(baseline, answer)
			// Avoid cutting mid-rune when old text re-rendered differently.
			for n < len(answer) && !utf8.RuneStart(answer[n]) {
				n++
			}
			answer = answer[n:]
		}
	}
	answer = stripPromptEcho(answer, prompt)
	answer = stripDisclaimer(answer)

	var sources []Source
	if list, ok := m["sources"].([]interface{}); ok {
		for _, item := range list {
			src, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			out := Source{
				Site:    toString(src["site"]),
				Title:   toString(src["title"]),
				Snippet: toString(src["snippet"]),
			}
			href := toString(src["href"])
			if strings.HasPrefix(href, "/goto") {
				out.URL = eng.resolveGoto(href)
			} else {
				out.URL = href
			}
			if out.Title == "" && out.Site == "" {
				out.Title = out.URL
			}
			sources = append(sources, out)
		}
	}
	if sources == nil {
		sources = []Source{}
	}
	return &Answer{Answer: answer, Sources: sources, URL: s.URL, Mode: mode}, nil
}

// dismissConsent best-effort dismisses the Google cookie-consent dialog
// ("Before you continue to Google") that covers fresh profiles and blocks
// clicks. Rejecting is preferred over accepting.
func (e *Engine) dismissConsent(page pw.Page) {
	const clickJS = `() => {
		const names = ['reject all', 'alle ablehnen', 'odrzuć wszystko', 'odrzucam wszystko',
			'accept all', 'alle akzeptieren', 'zaakceptuj wszystko', 'akceptuję wszystko'];
		const btns = [...document.querySelectorAll('button')];
		for (const name of names) {
			const b = btns.find(x => (x.innerText || '').trim().toLowerCase() === name);
			if (b) { b.click(); return name; }
		}
		return null;
	}`
	v, err := page.Evaluate(clickJS)
	if err != nil || v == nil {
		return
	}
	log.Printf("dismissed Google consent dialog (%v)", v)
	// The button submits a form: give the navigation a moment to settle.
	_ = page.WaitForLoadState(pw.PageWaitForLoadStateOptions{State: pw.LoadStateNetworkidle})
	time.Sleep(1500 * time.Millisecond)
}

// resolveGoto follows Google's encrypted redirect link and returns the real
// target URL. The request context shares browser cookies and follows
// redirects; failures fall back to the raw /goto link.
func (e *Engine) resolveGoto(href string) string {
	full := "https://www.google.com" + href
	resp, err := e.ctx.Request().Get(full, pw.APIRequestContextGetOptions{MaxRedirects: pw.Int(10)})
	if err != nil || resp == nil {
		log.Printf("goto resolve failed (%v), keeping raw link", err)
		return full
	}
	return resp.URL()
}

// waitSubmitted polls until the conversation shows a new answer (turn
// markers, prompt echo, or aimc text change) or the deadline passes.
func waitSubmitted(page pw.Page, before int, wait time.Duration, prompt, baseline string) bool {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		if countTurns(page) > before {
			return true
		}
		cur := aimcText(page)
		if (cur != baseline && len(cur) > 50) || (prompt != "" && strings.Contains(cur, prompt)) {
			return true
		}
	}
	return false
}

func countTurns(page pw.Page) int {
	v, err := page.Evaluate(turnCountJS)
	if err != nil {
		return 0
	}
	return int(toFloat(v))
}

func aimcText(page pw.Page) string {
	v, err := page.Evaluate(aimcTextJS)
	if err != nil {
		return ""
	}
	return toString(v)
}

// --- Hyprland IPC helpers ---

// hyprSig caches the discovered Hyprland instance signature. Outside a
// logged-in shell (systemd services) HYPRLAND_INSTANCE_SIGNATURE is not
// set, so the live instance is found by probing /run/user/$UID/hypr/*.
var hyprSig string

// hyprRun runs hyprctl with the given args, injecting the discovered
// instance signature when the environment does not provide one.
func hyprRun(args ...string) ([]byte, error) {
	run := func(sig string) ([]byte, error) {
		a := args
		if sig != "" {
			a = append([]string{"-i", sig}, args...)
		}
		return exec.Command("hyprctl", a...).CombinedOutput()
	}
	if hyprSig == "" {
		if v := os.Getenv("HYPRLAND_INSTANCE_SIGNATURE"); v != "" {
			hyprSig = v
		} else {
			hyprSig = discoverHyprSig()
		}
	}
	out, err := run(hyprSig)
	if err != nil || strings.Contains(string(out), "Couldn't connect") {
		// Hyprland may have restarted: re-discover and retry once.
		if sig := discoverHyprSig(); sig != "" && sig != hyprSig {
			hyprSig = sig
			return run(hyprSig)
		}
	}
	return out, err
}

// discoverHyprSig finds a live Hyprland instance by probing every
// signature directory under the user runtime dir. Most recent first.
func discoverHyprSig() string {
	uid := os.Getuid()
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = fmt.Sprintf("/run/user/%d", uid)
	}
	entries, err := os.ReadDir(base + "/hypr")
	if err != nil {
		return ""
	}
	// ReadDir is sorted by name; probe all and keep the newest by mtime.
	var best string
	var bestMt int64 = -1
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		sig := e.Name()
		if _, err := os.Stat(fmt.Sprintf("%s/hypr/%s/.socket.sock", base, sig)); err != nil {
			continue
		}
		// Probe: only a live instance answers.
		if out, err := exec.Command("hyprctl", "-i", sig, "-j", "activeworkspace").Output(); err == nil && len(out) > 0 {
			if info.ModTime().Unix() > bestMt {
				best, bestMt = sig, info.ModTime().Unix()
			}
		}
	}
	return best
}

// hyprEval runs a Lua snippet through `hyprctl eval` and reports whether
// Hyprland accepted it (exit codes are useless; errors come as text).
func hyprEval(lua string) error {
	out, err := hyprRun("eval", lua)
	if err != nil {
		return fmt.Errorf("hyprctl eval: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if s := string(out); !strings.HasPrefix(s, "ok") {
		return fmt.Errorf("hyprctl rejected: %s", strings.TrimSpace(s))
	}
	return nil
}

// hyprWindow is the subset of `hyprctl -j clients` entries we need.
type hyprWindow struct {
	Address   string `json:"address"`
	Class     string `json:"class"`
	Title     string `json:"title"`
	Floating  bool   `json:"floating"`
	At        []int  `json:"at"`
	Size      []int  `json:"size"`
	Workspace struct {
		ID int `json:"id"`
	} `json:"workspace"`
}

// windowByAddress returns the current state of the window with the given
// Hyprland address.
func windowByAddress(addr string) (hyprWindow, error) {
	out, err := hyprRun("-j", "clients")
	if err != nil {
		return hyprWindow{}, err
	}
	var wins []hyprWindow
	if err := json.Unmarshal(out, &wins); err != nil {
		return hyprWindow{}, err
	}
	for _, w := range wins {
		if w.Address == addr {
			return w, nil
		}
	}
	return hyprWindow{}, fmt.Errorf("window %s not found", addr)
}

// waitWindowByClass polls hyprctl until a window with the given class
// appears (the browser takes a moment to map its first window).
func waitWindowByClass(class string, wait time.Duration) (string, error) {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		out, err := hyprRun("-j", "clients")
		if err == nil {
			var wins []hyprWindow
			if json.Unmarshal(out, &wins) == nil {
				for _, w := range wins {
					if w.Class == class {
						return w.Address, nil
					}
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return "", fmt.Errorf("no hyprctl window with class %q (is this Hyprland?)", class)
}

// hyprMonitor is the subset of `hyprctl -j monitors` entries we need.
type hyprMonitor struct {
	X       int  `json:"x"`
	Y       int  `json:"y"`
	Width   int  `json:"width"`
	Height  int  `json:"height"`
	Focused bool `json:"focused"`
}

// monitorCorner returns the bottom-right corner position for a w x h
// floating window on the focused monitor.
func monitorCorner(w, h int) (int, int, bool) {
	out, err := hyprRun("-j", "monitors")
	if err != nil {
		return 0, 0, false
	}
	var mons []hyprMonitor
	if json.Unmarshal(out, &mons) != nil || len(mons) == 0 {
		return 0, 0, false
	}
	mon := mons[0]
	for _, m := range mons {
		if m.Focused {
			mon = m
		}
	}
	return mon.X + mon.Width - w - 10, mon.Y + mon.Height - h - 10, true
}

// Place re-places the browser window in the given mode at runtime
// (POST /v1/window). Returns the resulting window state.
func (e *Engine) Place(mode string) (map[string]any, error) {
	switch mode {
	case ModeWorkspace, ModeMini, ModeVisible:
	default:
		return nil, fmt.Errorf("invalid mode %q (want workspace|mini|visible)", mode)
	}
	e.cfg.WindowMode = mode
	e.placeWindow()
	return e.WindowState()
}

// WindowState reports where the browser window currently lives
// (GET /v1/window). "present": false when Hyprland or the window is not
// found — reported, not errored, so clients can treat it as state.
func (e *Engine) WindowState() (map[string]any, error) {
	addr, err := waitWindowByClass(WindowClass, 2*time.Second)
	if err != nil {
		return map[string]any{"present": false, "error": err.Error()}, nil
	}
	win, err := windowByAddress(addr)
	if err != nil {
		return map[string]any{"present": false, "error": err.Error()}, nil
	}
	return map[string]any{
		"present": true, "address": win.Address, "workspace": win.Workspace.ID,
		"floating": win.Floating, "at": win.At, "size": win.Size, "title": win.Title,
		"mode": e.cfg.WindowMode,
	}, nil
}

// --- small helpers ---

func randID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func runeSafeSlice(s string, start int) string {
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	if start > len(s) {
		return ""
	}
	return s[start:]
}

func commonPrefixLen(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func stripPromptEcho(answer, prompt string) string {
	echo := "AI Mode reply for " + prompt
	if strings.HasPrefix(answer, echo) {
		answer = strings.TrimPrefix(answer, echo)
		answer = strings.TrimLeft(answer, " \n")
	}
	return answer
}

func stripDisclaimer(answer string) string {
	const marker = "AI responses may include mistakes"
	if i := strings.LastIndex(answer, marker); i >= 0 {
		answer = answer[:i]
	}
	return strings.TrimSpace(answer)
}

func toString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func toFloat(v interface{}) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	default:
		return 0
	}
}

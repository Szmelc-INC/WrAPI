// Package engine drives a real Chromium via Playwright against AI chats on
// real web pages. Everything site-specific lives in declarative driver
// specs (see internal/driver and drivers/*.json): where to type, how to
// submit, how to read the streaming answer and where sources live. The
// engine itself is one generic state machine.
//
// Window placement goes through Hyprland IPC (hyprctl eval, Lua API): the
// browser is tagged with WM class "wrapi" and parked silently on a
// dedicated workspace by default — it never appears on the user's desktop.
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

	"wrapi/internal/driver"

	pw "github.com/mxschmitt/playwright-go"
)

// ErrBlocked is returned when the site serves its bot wall (e.g. Google's
// /sorry/ page).
var ErrBlocked = errors.New("the site blocked the request (bot wall) - wait for the rate limit to decay or solve the challenge once in visible mode")

// Window modes.
const (
	ModeWorkspace = "workspace" // silent park on a dedicated workspace (default)
	ModeMini      = "mini"      // floated tiny bar in the corner of the focused monitor
	ModeVisible   = "visible"   // focused on the dedicated workspace
	ModeHeadless  = "headless"  // no window at all (experimental: gets bot-walled)
)

// WindowClass tags the browser window for hyprctl targeting.
const WindowClass = "wrapi"

// Source is a single citation from an answer.
type Source struct {
	Site    string `json:"site,omitempty"`
	Title   string `json:"title,omitempty"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

// Answer is the result of one query.
type Answer struct {
	Answer  string   `json:"answer"`
	Sources []Source `json:"sources"`
	URL     string   `json:"url"`
	Mode    string   `json:"mode"` // "fresh" or "followup"
}

// Config controls the engine. Values come from env vars / CLI flags.
type Config struct {
	Addr          string
	ProfileDir    string
	DriversDir    string
	DefaultDriver string
	WindowMode    string // workspace | mini | visible | headless
	Workspace     int    // dedicated workspace number
	MiniWidth     int
	MiniHeight    int
	HL            string // hl= URL parameter (UI language)
	Timeout       time.Duration
}

// Engine owns the browser process, the driver registry and the session
// table.
type Engine struct {
	cfg      Config
	drivers  *driver.Registry
	mu       sync.Mutex // guards sessions and browser lifecycle
	pw       *pw.Playwright
	ctx      pw.BrowserContext
	sessions map[string]*Session
}

// Session is one conversation: a dedicated browser page, its driver spec
// and its state.
type Session struct {
	ID      string
	Driver  string
	spec    *driver.Spec
	page    pw.Page
	mu      sync.Mutex // serializes asks on this session
	URL     string
	Turns   int
	Created time.Time
}

// Start launches the persistent browser context, places its window
// according to cfg.WindowMode and creates the default session.
func Start(cfg Config) (*Engine, error) {
	registry, err := driver.Load(cfg.DriversDir, cfg.DefaultDriver)
	if err != nil {
		return nil, err
	}
	if err := pw.Install(); err != nil {
		return nil, fmt.Errorf("playwright driver install: %w", err)
	}
	e := &Engine{cfg: cfg, drivers: registry, sessions: map[string]*Session{}}
	if err := e.launch(); err != nil {
		return nil, err
	}
	if _, err := e.NewSession("default", cfg.DefaultDriver); err != nil {
		_ = e.Close()
		return nil, err
	}
	return e, nil
}

// Drivers exposes the driver registry (for /v1/models etc.).
func (e *Engine) Drivers() *driver.Registry { return e.drivers }

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
		// Sites serve bot walls when navigator.webdriver is exposed.
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
// SAFETY: this never targets the user's active workspace — only the
// dedicated workspace (silent or focused per mode) or floating geometry.
// Failure is logged, not fatal: without hyprctl the window simply opens
// where the compositor puts it.
func (e *Engine) placeWindow() bool {
	addr, err := waitWindowByClass(WindowClass, 15*time.Second)
	if err != nil {
		log.Printf("window placement skipped: %v", err)
		return false
	}
	switch e.cfg.WindowMode {
	case ModeMini:
		// Floating a window that sits on an inactive workspace silently
		// no-ops in Hyprland, so mini geometry only sticks when the window
		// is on the focused workspace (i.e. right at launch). When it does
		// not settle, fall back to the hidden workspace mode.
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
				return true // settled
			}
		}
		log.Printf("mini placement did not settle (floating requires the focused workspace); falling back to workspace mode")
		e.cfg.WindowMode = ModeWorkspace
		return e.moveToWorkspace(addr, true)
	case ModeVisible:
		// Focused (non-silent) move: the workspace is switched to, so the
		// window is actually seen; deterministic, never under the cursor.
		if err := hyprEval(fmt.Sprintf(`hl.dispatch(hl.dsp.window.move({address='%s', workspace=%d, silent=false}))`, addr, e.cfg.Workspace)); err != nil {
			log.Printf("visible placement failed: %v", err)
			return false
		}
		return true
	default: // workspace
		return e.moveToWorkspace(addr, true)
	}
}

// moveToWorkspace silently parks the window on the dedicated workspace,
// verifying it actually landed there.
func (e *Engine) moveToWorkspace(addr string, silent bool) bool {
	for attempt := 0; attempt < 4; attempt++ {
		if err := hyprEval(fmt.Sprintf(`hl.dispatch(hl.dsp.window.move({address='%s', workspace=%d, silent=%v}))`, addr, e.cfg.Workspace, silent)); err != nil {
			log.Printf("workspace placement failed: %v", err)
			return false
		}
		time.Sleep(500 * time.Millisecond)
		if win, err := windowByAddress(addr); err == nil && win.Workspace.ID == e.cfg.Workspace {
			return true
		}
	}
	return false
}

// NewSession creates a session with the given id (random when empty) bound
// to the named driver (default when empty).
func (e *Engine) NewSession(id, driverID string) (*Session, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	spec, err := e.drivers.Get(driverID)
	if err != nil {
		return nil, err
	}
	if id == "" {
		id = randID()
	}
	if s, exists := e.sessions[id]; exists {
		return s, nil
	}
	page, err := e.ctx.NewPage()
	if err != nil {
		return nil, fmt.Errorf("opening page: %w", err)
	}
	s := &Session{ID: id, Driver: spec.ID, spec: spec, page: page, Created: time.Now()}
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
		spec := e.drivers.Default()
		page, err := e.ctx.NewPage()
		if err != nil {
			return nil, err
		}
		s := &Session{ID: id, Driver: spec.ID, spec: spec, page: page, Created: time.Now()}
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
			"id": s.ID, "driver": s.Driver, "turns": s.Turns, "url": s.URL,
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

// OpenLogin navigates a dedicated page to the driver's login URL so a
// human can log in once (QR scan, credentials); the persistent profile
// keeps the session afterwards. Returns the human-facing hint.
func (e *Engine) OpenLogin(driverID string) (string, error) {
	spec, err := e.drivers.Get(driverID)
	if err != nil {
		return "", err
	}
	if spec.LoginURL == "" {
		return "", fmt.Errorf("driver %q has no login_url", spec.ID)
	}
	s, err := e.NewSession("login-"+spec.ID, spec.ID)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.page.Goto(spec.LoginURL, pw.PageGotoOptions{
		WaitUntil: pw.WaitUntilStateDomcontentloaded,
		Timeout:   pw.Float(60000),
	}); err != nil {
		return "", fmt.Errorf("opening login page: %w", err)
	}
	hint := spec.LoginHint
	if hint == "" {
		hint = "Log in in the browser window (workspace " + fmt.Sprintf("%d", e.cfg.Workspace) + "); the profile keeps the session."
	}
	return hint, nil
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

// Ask runs a fresh query in the session's page following the driver spec:
// the "url" strategy encodes the prompt in the start URL (search-style
// sites), the "input" strategy opens the page, clicks the open button and
// types into the composer. onDelta, when non-nil, receives streaming text
// chunks.
func (s *Session) Ask(eng *Engine, prompt string, timeout time.Duration, onDelta func(string)) (*Answer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	spec := s.spec
	ms := float64(timeout.Milliseconds())

	switch spec.Start.Strategy {
	case "input":
		if _, err := s.page.Goto(spec.Start.URL, pw.PageGotoOptions{
			WaitUntil: pw.WaitUntilStateDomcontentloaded, Timeout: pw.Float(ms),
		}); err != nil {
			return nil, fmt.Errorf("goto: %w", err)
		}
		s.dismissConsent()
		if err := s.waitVisible(spec.Start.WaitSelector, timeout); err != nil {
			return nil, err
		}
		if spec.Start.OpenSelector != "" {
			if err := s.page.Locator(spec.Start.OpenSelector).First().Click(pw.LocatorClickOptions{
				Timeout: pw.Float(15000),
			}); err != nil {
				log.Printf("%s: open selector click failed: %v", spec.ID, err)
			}
		}
		baseline := s.paneText()
		turnsBefore := s.countTurns()
		if err := s.typePrompt(prompt); err != nil {
			return nil, err
		}
		if err := s.submit(); err != nil {
			return nil, err
		}
		if !s.waitSubmitted(turnsBefore, baseline, prompt, timeout/3) {
			return nil, fmt.Errorf("prompt was not submitted (no new answer appeared; is the site logged in? try `wrapi login %s`)", spec.ID)
		}
	default: // url
		target := strings.ReplaceAll(spec.Start.URL, "{prompt}", url.QueryEscape(prompt))
		target = strings.ReplaceAll(target, "{hl}", url.QueryEscape(eng.cfg.HL))
		if _, err := s.page.Goto(target, pw.PageGotoOptions{
			WaitUntil: pw.WaitUntilStateDomcontentloaded, Timeout: pw.Float(ms),
		}); err != nil {
			return nil, fmt.Errorf("goto: %w", err)
		}
		if spec.BlockedOnURL != "" && strings.Contains(s.page.URL(), spec.BlockedOnURL) {
			return nil, ErrBlocked
		}
		s.dismissConsent()
		if err := s.waitVisible(spec.Start.WaitSelector, timeout); err != nil {
			if spec.BlockedOnURL != "" && strings.Contains(s.page.URL(), spec.BlockedOnURL) {
				return nil, ErrBlocked
			}
			return nil, err
		}
	}

	if err := s.streamAnswer(timeout, "", onDelta); err != nil {
		return nil, err
	}
	return s.finish(eng, "fresh", prompt, "")
}

// FollowUp sends a follow-up through the on-page composer, keeping the
// conversation context. onDelta streams chunks.
func (s *Session) FollowUp(eng *Engine, prompt string, timeout time.Duration, onDelta func(string)) (*Answer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	spec := s.spec
	if err := s.waitVisible(spec.Followup.InputSelector, 10*time.Second); err != nil {
		return nil, fmt.Errorf("composer not found (run a fresh ask first, or log in: `wrapi login %s`): %w", spec.ID, err)
	}
	baseline := s.paneText()
	turnsBefore := s.countTurns()
	if err := s.typePrompt(prompt); err != nil {
		return nil, err
	}
	if err := s.submit(); err != nil {
		return nil, err
	}
	if !s.waitSubmitted(turnsBefore, baseline, prompt, timeout/3) {
		return nil, fmt.Errorf("follow-up was not submitted (no new answer appeared)")
	}
	if err := s.streamAnswer(timeout, baseline, onDelta); err != nil {
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

// --- generic driver-spec mechanics ---

// waitVisible waits for a selector to become visible.
func (s *Session) waitVisible(selector string, timeout time.Duration) error {
	_, err := s.page.WaitForSelector(selector, pw.PageWaitForSelectorOptions{
		Timeout: pw.Float(float64(timeout.Milliseconds())),
		State:   pw.WaitForSelectorStateVisible,
	})
	if err != nil {
		return fmt.Errorf("%s: element %q did not appear (page: %s): %w", s.spec.ID, selector, s.page.URL(), err)
	}
	return nil
}

// typePrompt puts text into the composer per the spec's input mode.
func (s *Session) typePrompt(prompt string) error {
	spec := s.spec
	comp := s.page.Locator(spec.Followup.InputSelector).First()
	if spec.Followup.FocusClick {
		if err := comp.Click(pw.LocatorClickOptions{Timeout: pw.Float(10000)}); err != nil {
			return err
		}
	}
	switch spec.Followup.InputMode {
	case "type":
		if err := s.page.Keyboard().Type(prompt, pw.KeyboardTypeOptions{Delay: pw.Float(8)}); err != nil {
			return err
		}
	default:
		if err := comp.Fill(prompt); err != nil {
			return err
		}
	}
	time.Sleep(400 * time.Millisecond)
	return nil
}

// submit sends the typed prompt: Enter first, Send button fallback.
func (s *Session) submit() error {
	if err := s.page.Keyboard().Press("Enter"); err != nil {
		return err
	}
	spec := s.spec
	if spec.Followup.SendButton == "" {
		return nil
	}
	time.Sleep(600 * time.Millisecond)
	send := s.page.Locator(spec.Followup.SendButton).First()
	if btn, err := send.ElementHandle(); err == nil && btn != nil {
		if v, err := btn.Evaluate("el => !el.disabled && el.offsetParent !== null"); err == nil {
			if enabled, ok := v.(bool); ok && enabled {
				return send.Click(pw.LocatorClickOptions{Timeout: pw.Float(5000)})
			}
		}
	}
	return nil
}

// waitSubmitted polls until the conversation shows a new answer (turn
// count growth, prompt echo in the pane, or pane text change).
func (s *Session) waitSubmitted(before int, baseline, prompt string, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		if s.countTurns() > before {
			return true
		}
		cur := s.paneText()
		if cur != baseline && len(cur) > s.spec.StreamMinText {
			return true
		}
		if prompt != "" && strings.Contains(cur, prompt) {
			return true
		}
	}
	return false
}

// state runs the spec's state probe.
func (s *Session) state() map[string]interface{} {
	raw, err := s.page.Evaluate(s.spec.StateJS)
	if err != nil {
		return nil
	}
	m, _ := raw.(map[string]interface{})
	return m
}

func (s *Session) countTurns() int {
	if s.spec.TurnCountJS == "" {
		return 0
	}
	v, err := s.page.Evaluate(s.spec.TurnCountJS)
	if err != nil {
		return 0
	}
	return int(toFloat(v))
}

func (s *Session) paneText() string {
	if s.spec.PaneTextJS == "" {
		return ""
	}
	v, err := s.page.Evaluate(s.spec.PaneTextJS)
	if err != nil {
		return ""
	}
	return toString(v)
}

// dismissConsent best-effort clicks through popups/consent dialogs that
// cover the page (spec-provided JS).
func (s *Session) dismissConsent() {
	if s.spec.ConsentJS == "" {
		return
	}
	v, err := s.page.Evaluate(s.spec.ConsentJS)
	if err != nil || v == nil {
		return
	}
	log.Printf("%s: dismissed dialog (%v)", s.spec.ID, v)
	_ = s.page.WaitForLoadState(pw.PageWaitForLoadStateOptions{State: pw.LoadStateNetworkidle})
	time.Sleep(1200 * time.Millisecond)
}

// streamAnswer polls the candidate answer until it stops growing (and the
// driver's complete flag is set when provided), invoking onDelta with each
// text growth. baseline is the pre-submit pane text: streaming only starts
// once the candidate diverges from it.
func (s *Session) streamAnswer(timeout time.Duration, baseline string, onDelta func(string)) error {
	deadline := time.Now().Add(timeout)
	started := baseline == ""
	emitted := ""
	stable, lastLen := 0, -1
	for time.Now().Before(deadline) {
		time.Sleep(700 * time.Millisecond)
		m := s.state()
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
		// Two consecutive polls with no growth: done. When the driver
		// reports completeness a single stable poll is enough.
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
	raw, err := s.page.Evaluate(s.spec.ExtractJS)
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
	s.Turns++
	if mode == "followup" && turns == 0 {
		// DOM without turn markers: the whole conversation is in the answer
		// text — keep only what is new: content after the echo of the
		// current prompt, or after the longest common prefix with the
		// pre-submit baseline.
		echo := strings.ReplaceAll(s.spec.Cleanup.StripPrefix, "{prompt}", prompt)
		if echo != "" {
			if i := strings.LastIndex(answer, echo); i >= 0 {
				answer = answer[i+len(echo):]
			} else if baseline != "" {
				n := commonPrefixLen(baseline, answer)
				for n < len(answer) && !utf8.RuneStart(answer[n]) {
					n++
				}
				answer = answer[n:]
			}
		} else if baseline != "" {
			n := commonPrefixLen(baseline, answer)
			for n < len(answer) && !utf8.RuneStart(answer[n]) {
				n++
			}
			answer = answer[n:]
		}
	}
	if p := strings.ReplaceAll(s.spec.Cleanup.StripPrefix, "{prompt}", prompt); p != "" && strings.HasPrefix(answer, p) {
		answer = strings.TrimLeft(strings.TrimPrefix(answer, p), " \n")
	}
	if m := s.spec.Cleanup.StripSuffixContains; m != "" {
		if i := strings.LastIndex(answer, m); i >= 0 {
			answer = answer[:i]
		}
	}
	answer = strings.TrimSpace(answer)

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
			if s.spec.ResolveGoto && strings.HasPrefix(href, "/goto") {
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

// resolveGoto follows an encrypted redirect link (Google's /goto?url=)
// and returns the real target URL. The request context shares browser
// cookies and follows redirects; failures fall back to the raw link.
func (e *Engine) resolveGoto(href string) string {
	full := "https://www.google.com" + href
	resp, err := e.ctx.Request().Get(full, pw.APIRequestContextGetOptions{MaxRedirects: pw.Int(10)})
	if err != nil || resp == nil {
		log.Printf("goto resolve failed (%v), keeping raw link", err)
		return full
	}
	return resp.URL()
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
// signature directory under the user runtime dir, newest first.
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

// waitWindowByClass polls hyprctl until a window with the given class
// appears (the browser takes a moment to map its first window).
func waitWindowByClass(class string, wait time.Duration) (string, error) {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if out, err := hyprRun("-j", "clients"); err == nil {
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

func near(a, b int) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= 4
}

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

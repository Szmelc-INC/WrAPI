// Package engine drives a real Chromium via Playwright against Google AI
// Mode (https://www.google.com/search?q=PROMPT&udm=50&csuir=1) and extracts
// the AI answer together with its sources.
package engine

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	pw "github.com/mxschmitt/playwright-go"
)

// ErrBlocked is returned when Google serves its automation /sorry/ wall.
var ErrBlocked = errors.New("google blocked the request (/sorry/ wall) - try clearing the profile or solving the captcha once in headed mode")

const (
	// Follow-ups create a NEW aimc subtree per conversation section; the
	// freshest content always lives in the LAST one.
	aiModeSelector = `div[data-subtree="aimc"]`
	lastPaneJS     = `() => { const p = document.querySelectorAll('div[data-subtree="aimc"]'); return p.length ? p[p.length-1] : null; }`
	aimcTextJS     = `() => { const p = document.querySelectorAll('div[data-subtree="aimc"]'); const a = p.length ? p[p.length-1] : null; return a ? a.innerText : ''; }`
	turnCountJS    = `() => document.querySelectorAll('div[data-subtree="aimc"] [data-scope-id="turn"]').length`
	mainColFinder  = `() => {
		const p = document.querySelectorAll('div[data-subtree="aimc"]');
		const aimc = p.length ? p[p.length-1] : null;
		if (!aimc) return { ready: false };
		// Follow-up turns append to the conversation: the newest answer is
		// the last [data-scope-id="turn"] element.
		const turns = aimc.querySelectorAll('[data-scope-id="turn"]');
		const scope = turns.length ? turns[turns.length - 1] : aimc;
		const main = scope.querySelector('[data-container-id="main-col"]') || scope;
		return { ready: true, len: main.innerText.length, complete: !!scope.querySelector('[data-complete="true"]') };
	}`
	// Extracts the answer body (last turn, newest aimc pane) and all source
	// entries from the current page.
	extractJS = `() => {
		const p = document.querySelectorAll('div[data-subtree="aimc"]');
		const aimc = p.length ? p[p.length-1] : null;
		if (!aimc) return null;
		const turns = aimc.querySelectorAll('[data-scope-id="turn"]');
		const scope = turns.length ? turns[turns.length - 1] : aimc;
		const main = scope.querySelector('[data-container-id="main-col"]') || scope;
		const out = { answer: main.innerText, sources: [], turns: turns.length };
		const seen = new Set();
		const lines = t => (t || '').split('\\n').map(s => s.trim()).filter(Boolean);
		const add = (a, site, title, snippet) => {
			const href = a.getAttribute('href') || '';
			if (!href) return;
			if (seen.has(href)) return;
			seen.add(href);
			out.sources.push({ site: (site || '').trim(), title: (title || '').trim(), snippet: (snippet || '').trim(), href });
		};
		// Source cards: li.Ioqnib holds site name, title and snippet.
		aimc.querySelectorAll('li.Ioqnib').forEach(li => {
			const a = li.querySelector('a[href^="/goto"]');
			if (!a) return;
			const l = lines(li.innerText);
			add(a, l[0] || '', l[1] || '', l.slice(2).join(' '));
		});
		// Inline citation chips (class gQWhZ): text is usually the site name,
		// but a chip can wrap a whole card, so split multiline text too.
		scope.querySelectorAll('a.gQWhZ').forEach(a => {
			const l = lines(a.innerText);
			add(a, l[0] || '', l[1] || '', l.slice(2).join(' '));
		});
		return out;
	}`
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

// Config controls the engine. Values come from env vars (see README).
type Config struct {
	Addr       string
	ProfileDir string
	Headless   bool
	HideWindow bool
	HL         string // hl= URL parameter (UI language)
	Timeout    time.Duration
}

// Engine owns the browser process. It is safe for concurrent HTTP use:
// requests are serialized through a mutex, because they share one page.
type Engine struct {
	cfg     Config
	mu      sync.Mutex
	pw      *pw.Playwright
	ctx     pw.BrowserContext
	page    pw.Page
	lastURL string
}

// Start launches the persistent browser context. The profile directory is
// kept between runs, so consent cookies and bot-wall clearance survive
// restarts. Driver/browser install happens on first start if needed.
func Start(cfg Config) (*Engine, error) {
	if err := pw.Install(); err != nil {
		return nil, fmt.Errorf("playwright driver install: %w", err)
	}
	e := &Engine{cfg: cfg}
	if err := e.launch(); err != nil {
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
	}
	// Google serves the /sorry/ wall when navigator.webdriver is exposed.
	args = append(args, "--disable-blink-features=AutomationControlled")
	if e.cfg.HideWindow && !e.cfg.Headless {
		// Headed (to keep a real fingerprint) but parked off-screen.
		args = append(args, "--window-position=-32000,-32000")
	}
	opts := pw.BrowserTypeLaunchPersistentContextOptions{
		Headless:          pw.Bool(e.cfg.Headless),
		Locale:            pw.String("en-US"),
		Args:              args,
		IgnoreDefaultArgs: []string{"--enable-automation"},
	}
	e.ctx, err = e.pw.Chromium.LaunchPersistentContext(abs, opts)
	if err != nil {
		return fmt.Errorf("launching chromium: %w", err)
	}
	e.page, err = e.ctx.NewPage()
	if err != nil {
		return fmt.Errorf("opening page: %w", err)
	}
	return nil
}

func (e *Engine) searchURL(prompt string) string {
	return "https://www.google.com/search?q=" + url.QueryEscape(prompt) +
		"&udm=50&csuir=1&hl=" + url.QueryEscape(e.cfg.HL)
}

// Ask runs one AI Mode query in a fresh search session and returns the
// extracted answer. If fresh is false the current page is reused (cheaper,
// but Google may contextualize repeat queries).
func (e *Engine) Ask(prompt string, fresh bool, timeout time.Duration) (*Answer, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	target := e.page
	if fresh {
		var err error
		target, err = e.ctx.NewPage()
		if err != nil {
			return nil, err
		}
	}

	start := time.Now()
	if _, err := target.Goto(e.searchURL(prompt), pw.PageGotoOptions{
		WaitUntil: pw.WaitUntilStateDomcontentloaded,
		Timeout:   pw.Float(float64(timeout.Milliseconds())),
	}); err != nil {
		if fresh {
			target.Close()
		}
		return nil, fmt.Errorf("goto: %w", err)
	}
	if strings.Contains(target.URL(), "/sorry/") {
		if fresh {
			target.Close()
		}
		return nil, ErrBlocked
	}

	if _, err := target.WaitForSelector(aiModeSelector, pw.PageWaitForSelectorOptions{
		Timeout: pw.Float(float64(timeout.Milliseconds())),
		State:   pw.WaitForSelectorStateVisible,
	}); err != nil {
		if fresh {
			target.Close()
		}
		return nil, fmt.Errorf("ai mode pane did not appear (page: %s): %w", target.URL(), err)
	}

	if err := e.waitStable(target, timeout); err != nil {
		if fresh {
			target.Close()
		}
		return nil, err
	}

	raw, err := target.Evaluate(extractJS)
	if err != nil {
		if fresh {
			target.Close()
		}
		return nil, fmt.Errorf("extract: %w", err)
	}
	pageURL := target.URL()
	if fresh {
		// Keep the answered page as the session page so /v1/followup can
		// continue the conversation; retire the previous one.
		old := e.page
		e.page = target
		if old != nil && old != target {
			_ = old.Close()
		}
	} else {
		e.lastURL = pageURL
	}

	a, err := e.buildAnswer(raw, pageURL, "fresh", prompt, "", start)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// FollowUp sends a follow-up question through the on-page input box of the
// current AI Mode session, keeping the conversation context.
func (e *Engine) FollowUp(prompt string, timeout time.Duration) (*Answer, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	start := time.Now()
	// The follow-up composer: scoped to the AI Mode input plate; the generic
	// aria-label catches DOM reshuffles (probed: textarea "Ask anything").
	inputSel := `[data-xid="aim-mars-input-plate"] textarea, textarea[aria-label="Ask anything"]`
	if _, err := e.page.WaitForSelector(inputSel, pw.PageWaitForSelectorOptions{
		Timeout: pw.Float(10000),
		State:   pw.WaitForSelectorStateVisible,
	}); err != nil {
		return nil, fmt.Errorf("follow-up input not found (run a fresh /v1/ask first): %w", err)
	}
	// The composer only accepts input when focused and needs a beat after
	// fill before Enter registers (mirrors manual interaction).
	comp := e.page.Locator(inputSel).First()
	if err := comp.Click(); err != nil {
		return nil, err
	}
	if err := comp.Fill(prompt); err != nil {
		return nil, err
	}
	time.Sleep(400 * time.Millisecond)
	turnsBefore := countTurns(e.page)
	baseline := aimcText(e.page)
	if err := comp.Press("Enter"); err != nil {
		return nil, err
	}
	// Enter sometimes does not submit (focus, composer state). Fall back to
	// the Send button and confirm a new answer actually appeared. Turn
	// markers may be absent and the question bubble lives outside the
	// aimc subtree, so aimc text growth is the reliable signal.
	submitted := waitTurnGrowth(e.page, turnsBefore, 6*time.Second, prompt, baseline)
	if !submitted {
		send := e.page.Locator(`[data-xid="input-plate-send-button"]`).First()
		if btn, err := send.ElementHandle(); err == nil && btn != nil {
			if v, err := btn.Evaluate("el => !el.disabled"); err == nil {
				if enabled, ok := v.(bool); ok && enabled {
					_ = send.Click()
				}
			}
		}
		if !waitTurnGrowth(e.page, turnsBefore, 8*time.Second, prompt, baseline) {
			return nil, fmt.Errorf("follow-up was not submitted (no new turn appeared)")
		}
	}
	if _, err := e.page.WaitForSelector(aiModeSelector, pw.PageWaitForSelectorOptions{
		Timeout: pw.Float(float64(timeout.Milliseconds())),
		State:   pw.WaitForSelectorStateVisible,
	}); err != nil {
		return nil, err
	}
	if err := e.waitStable(e.page, timeout); err != nil {
		return nil, err
	}
	raw, err := e.page.Evaluate(extractJS)
	if err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}
	pageURL := e.page.URL()
	e.lastURL = pageURL
	return e.buildAnswer(raw, pageURL, "followup", prompt, baseline, start)
}

// Reset closes all pages and opens a fresh one, keeping the browser and
// profile cookies alive.
func (e *Engine) Reset() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	pages := e.ctx.Pages()
	for _, p := range pages {
		if err := p.Close(); err != nil {
			log.Printf("reset: closing page: %v", err)
		}
	}
	page, err := e.ctx.NewPage()
	if err != nil {
		return err
	}
	e.page = page
	e.lastURL = ""
	return nil
}

// ScreenshotPNG returns a PNG of the current page (debug endpoint).
func (e *Engine) ScreenshotPNG() ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.page.Screenshot(pw.PageScreenshotOptions{FullPage: pw.Bool(false)})
}

// PageHTML returns the current DOM of the last used page (debug endpoint).
// Call it right after a failing /v1/ask to tune selectors.
func (e *Engine) PageHTML() (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.page.Content()
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

// waitStable polls the AI answer until its length stops growing (and the
// turn is marked complete when Google sets that attribute) or until the
// deadline passes. Streaming answers grow over several seconds, so a
// fixed wait would be either slow or premature.
func (e *Engine) waitStable(page pw.Page, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	stable, lastLen := 0, -1
	for time.Now().Before(deadline) {
		time.Sleep(800 * time.Millisecond)
		raw, err := page.Evaluate(mainColFinder)
		if err != nil {
			return fmt.Errorf("stability probe: %w", err)
		}
		m, _ := raw.(map[string]interface{})
		if m == nil || m["ready"] != true {
			continue
		}
		length := int(toFloat(m["len"]))
		complete, _ := m["complete"].(bool)
		if length > 0 && length == lastLen {
			stable++
		} else {
			stable = 0
		}
		lastLen = length
		// Two consecutive polls with no growth: done. When Google marks
		// the turn complete a single stable poll is enough.
		if (complete && stable >= 1) || stable >= 2 {
			return nil
		}
	}
	if lastLen == 0 {
		return fmt.Errorf("answer never rendered (timeout %s)", timeout)
	}
	return nil // best effort on deadline
}

// buildAnswer normalizes the raw JS extraction: strips the prompt echo and
// the disclaimer tail, resolves encrypted /goto links to real URLs.
// baseline is the aimc text captured before a follow-up was submitted and is
// used to cut the old conversation off when the DOM has no turn markers.
func (e *Engine) buildAnswer(raw interface{}, pageURL, mode, prompt, baseline string, start time.Time) (*Answer, error) {
	m, ok := raw.(map[string]interface{})
	if !ok || m == nil {
		return nil, fmt.Errorf("extraction returned no data")
	}
	answer := strings.TrimSpace(toString(m["answer"]))
	turns := int(toFloat(m["turns"]))
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
			s, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			src := Source{
				Site:    toString(s["site"]),
				Title:   toString(s["title"]),
				Snippet: toString(s["snippet"]),
			}
			href := toString(s["href"])
			if strings.HasPrefix(href, "/goto") {
				src.URL = e.resolveGoto(href)
			} else {
				src.URL = href
			}
			if src.Title == "" && src.Site == "" {
				src.Title = src.URL
			}
			sources = append(sources, src)
		}
	}
	if sources == nil {
		sources = []Source{}
	}
	return &Answer{
		Answer:  answer,
		Sources: sources,
		URL:     pageURL,
		Mode:    mode,
	}, nil
}

// resolveGoto follows Google's encrypted redirect link and returns the real
// target URL. The API request context shares browser cookies and follows
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

func countTurns(page pw.Page) int {
	v, err := page.Evaluate(turnCountJS)
	if err != nil {
		return 0
	}
	return int(toFloat(v))
}

// waitTurnGrowth polls until the conversation gains a new answer (turn
// markers, prompt echo, or aimc text growth — whichever the DOM variant
// provides) or the deadline passes.
func waitTurnGrowth(page pw.Page, before int, wait time.Duration, prompt, baseline string) bool {
	signal := func() bool {
		if countTurns(page) > before {
			return true
		}
		cur := aimcText(page)
		// The newest aimc pane resets on submit and then streams in, so
		// any sufficiently long new text counts as progress.
		return (cur != baseline && len(cur) > 50) ||
			(prompt != "" && strings.Contains(cur, prompt))
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		if signal() {
			return true
		}
	}
	return false
}

// aimcText returns the innerText of the AI answer pane (empty on failure).
func aimcText(page pw.Page) string {
	v, err := page.Evaluate(aimcTextJS)
	if err != nil {
		return ""
	}
	return toString(v)
}

// commonPrefixLen returns the length of the longest common prefix of two
// strings.
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
	default:
		return 0
	}
}

// The interactive chat TUI: colorful, streamed, markdown-lite rendering
// over the /v1/say NDJSON stream. Stdlib only — plain ANSI escapes.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Palette (design accent #105c5c on neutral dark greys; never purple).
const (
	cAcc    = "\033[38;2;16;92;92m"  // teal — brand, user, headers
	cAccB   = "\033[1;38;2;16;92;92m"
	cDim    = "\033[2m"              // meta, dividers, urls' brackets
	cURL    = "\033[4;38;2;80;140;140m"
	cYou    = "\033[1;38;2;120;180;180m"
	cErr    = "\033[1;31m"
	cWarn   = "\033[33m"
	cOK     = "\033[32m"
	cBold   = "\033[1m"
	cReset  = "\033[0m"
	spinSet = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
)

type chatTUI struct {
	addr     string
	driver   string
	session  string
	termWidth int
	noColor  bool
	showSrc  bool
	first    bool // next send is a fresh ask (session has no turns)
}

func (t *chatTUI) col(s, c string) string {
	if t.noColor {
		return s
	}
	return c + s + cReset
}

func (t *chatTUI) width() int {
	if t.termWidth > 40 {
		return t.termWidth
	}
	return 100
}

// banner prints the session header box.
func (t *chatTUI) banner() {
	w := t.width()
	line := strings.Repeat("─", w)
	fmt.Println(t.col("┌"+line+"┐", cDim))
	tag := fmt.Sprintf(" WrAPI ▸ %s ▸ session %s ", t.driver, t.session)
	pad := strings.Repeat(" ", max(0, w+1-runeLen(tag)))
	fmt.Println(t.col("│", cDim) + t.col(tag, cAccB) + t.col(pad+"│", cDim))
	fmt.Println(t.col("└"+line+"┘", cDim))
	fmt.Printf("%s /help for commands · empty line quits%s\n\n", t.col("hint:", cDim), t.col("", cReset))
}

// renderAnswer prints the assistant text with light markdown formatting.
func (t *chatTUI) renderAnswer(text string) {
	inCode := false
	for _, raw := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		line := strings.TrimRight(raw, " \t")
		switch {
		case strings.HasPrefix(line, "```"):
			inCode = !inCode
			fmt.Println(t.col(line, cDim))
		case inCode:
			fmt.Println(t.col("  │ "+line, cAcc)) // code in accent, keep simple
		case line == "":
			fmt.Println()
		case strings.HasPrefix(line, "### "), strings.HasPrefix(line, "## "), strings.HasPrefix(line, "# "):
			fmt.Println(t.col("  "+strings.TrimLeft(line, "# "), cAccB))
		case strings.HasPrefix(line, "- "), strings.HasPrefix(line, "* "), strings.HasPrefix(line, "• "):
			fmt.Printf("  %s %s\n", t.col("•", cAcc), inline(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(line, "- "), "* "), "• ")))
		case isNumItem(line):
			fmt.Println("  " + inline(line))
		case strings.HasPrefix(line, "|"):
			fmt.Println(t.col("  "+line, cDim))
		case strings.HasPrefix(line, "> "):
			fmt.Printf("  %s %s\n", t.col("▎", cDim), t.col(line[2:], cDim))
		default:
			fmt.Println("  " + inline(line))
		}
	}
}

// inline applies inline emphasis: **bold** and bare URLs.
func inline(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		switch {
		case strings.HasPrefix(s[i:], "**"):
			j := strings.Index(s[i+2:], "**")
			if j >= 0 {
				b.WriteString(cBold + s[i+2:i+2+j] + cReset)
				i += 2 + j + 2
				continue
			}
		case s[i] == 'h' && strings.HasPrefix(s[i:], "http"):
			j := i
			for j < len(s) && !strings.ContainsRune(" \t)]}", rune(s[j])) {
				j++
			}
			b.WriteString(cURL + s[i:j] + cReset)
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func isNumItem(line string) bool {
	if len(line) < 3 {
		return false
	}
	d := 0
	for d < len(line) && line[d] >= '0' && line[d] <= '9' {
		d++
	}
	return d > 0 && d+1 < len(line) && line[d] == '.' && line[d+1] == ' '
}

// renderSources prints the citations block.
func (t *chatTUI) renderSources(sources []chatSource) {
	if len(sources) == 0 {
		return
	}
	fmt.Println(t.col("  ── sources "+"──────────────────────────────────────────", cDim))
	for i, s := range sources {
		name := s.Site
		if name == "" {
			name = hostOf(s.URL)
		}
		if s.Title != "" && s.Title != name {
			fmt.Printf("  %s %s %s\n", t.col(fmt.Sprintf("%2d.", i+1), cAcc), name, t.col("· "+s.Title, cDim))
		} else {
			fmt.Printf("  %s %s\n", t.col(fmt.Sprintf("%2d.", i+1), cAcc), name)
		}
		fmt.Printf("     %s\n", t.col(s.URL, cURL))
	}
}

// spinner renders a waiting animation until stop is closed; returns elapsed.
func (t *chatTUI) spin(stop <-chan struct{}) {
	if t.noColor {
		return
	}
	start := time.Now()
	i := 0
	for {
		select {
		case <-stop:
			fmt.Print("\r" + strings.Repeat(" ", 40) + "\r")
			return
		case <-time.After(90 * time.Millisecond):
			fmt.Printf("\r  %s thinking… %s",
				t.col(string(spinSet[i%len(spinSet)]), cAcc),
				t.col(fmt.Sprintf("%.0fs", time.Since(start).Seconds()), cDim))
			i++
		}
	}
}

// chatSource mirrors the API source object (avoids importing engine here).
type chatSource struct {
	Site    string `json:"site,omitempty"`
	Title   string `json:"title,omitempty"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

type apiFinal struct {
	Answer   string         `json:"answer"`
	Sources  []chatSource `json:"sources"`
	Session  string         `json:"session"`
	Mode     string         `json:"mode"`
	Error    string         `json:"error"`
	Elapsed  int64          `json:"elapsed_ms"`
}

// send posts the prompt to /v1/say with stream:true and renders live.
func (t *chatTUI) send(prompt string) {
	start := time.Now()
	body, _ := json.Marshal(map[string]any{"prompt": prompt, "session": t.session, "driver": t.driver, "stream": true})
	resp, err := http.Post("http://"+t.addr+"/v1/say", "application/json", strings.NewReader(string(body)))
	if err != nil {
		fmt.Println(t.col("  ✗ "+err.Error(), cErr))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		var e struct{ Error string `json:"error"` }
		_ = json.Unmarshal(raw, &e)
		fmt.Println(t.col("  ✗ "+e.Error, cErr))
		return
	}

	stop := make(chan struct{})
	var done chan struct{}
	if !t.noColor {
		done = make(chan struct{})
		go func() { t.spin(stop); close(done) }()
	}

	fmt.Printf("%s\n", t.col("  ╭─ AI", cAcc))
	var final apiFinal
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var chunk map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &chunk) != nil {
			continue
		}
		// Deltas are consumed silently: the answer is rendered once, fully
		// formatted, when the final object arrives (avoids double-printing
		// raw vs cleaned text).
		if d, ok := chunk["delta"]; ok {
			var ds string
			_ = json.Unmarshal(d, &ds)
			continue
		}
		_ = json.Unmarshal([]byte(line), &final)
	}
	closeOnce(&stop)
	if done != nil {
		<-done
	}
	fmt.Println()
	if final.Error != "" {
		fmt.Println(t.col("  ✗ "+final.Error, cErr))
		return
	}
	if final.Answer != "" {
		t.renderAnswer(final.Answer)
		fmt.Println(t.col("  ╰───────────", cAcc))
	}
	if t.showSrc {
		t.renderSources(final.Sources)
	}
	fmt.Printf("  %s\n\n", t.col(fmt.Sprintf("▸ %s · %s · %.1fs", final.Session, final.Mode, time.Since(start).Seconds()), cDim))
	if final.Session != "" {
		t.session = final.Session
	}
	t.first = false
}

func closeOnce(c *chan struct{}) {
	select {
	case <-*c:
	default:
		close(*c)
	}
}

// runChat is the REPL loop.
func runChat(addr, driver, session string, noColor bool) {
	t := &chatTUI{addr: addr, driver: driver, session: session, noColor: noColor || os.Getenv("NO_COLOR") != "", first: true, showSrc: true}
	if v := os.Getenv("COLUMNS"); v != "" {
		fmt.Sscanf(v, "%d", &t.termWidth)
	}
	t.banner()

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for {
		fmt.Print(t.col("❯ ", cAccB))
		if !sc.Scan() {
			break
		}
		prompt := strings.TrimSpace(sc.Text())
		if prompt == "" {
			break
		}
		if strings.HasPrefix(prompt, "/") {
			if t.command(prompt) {
				return
			}
			continue
		}
		fmt.Printf("%s\n\n", t.col("  ╭─ you", cYou))
		t.send(prompt)
	}
	fmt.Println(t.col("  bye 👋", cDim))
}

// command handles slash commands; returns true when the REPL should exit.
func (t *chatTUI) command(cmd string) bool {
	fields := strings.Fields(cmd)
	switch fields[0] {
	case "/help", "/?":
		fmt.Printf("  %s\n", t.col("commands:", cAccB))
		for _, h := range [][2]string{
			{"/new", "start a fresh session"},
			{"/clear", "reset the current conversation"},
			{"/driver ID", "switch AI driver (starts a new session)"},
			{"/sources", "toggle source display"},
			{"/session", "show session info"},
			{"/exit", "quit"},
		} {
			fmt.Printf("    %s %s\n", t.col(fmt.Sprintf("%-12s", h[0]), cAcc), t.col(h[1], cDim))
		}
	case "/exit", "/quit":
		return true
	case "/new":
		var out struct{ Session string `json:"session"` }
		if err := apiPost(t.addr, "/v1/sessions", map[string]any{"driver": t.driver}, &out); err == nil && out.Session != "" {
			t.session = out.Session
		} else {
			t.session = fmt.Sprintf("chat-%d", time.Now().Unix())
		}
		t.first = true
		saveCurrentSession(t.session)
		fmt.Printf("  %s %s\n\n", t.col("new session:", cOK), t.col(t.session, cAcc))
	case "/clear":
		if err := apiPostVoid(t.addr, "/v1/reset?session="+urlQueryEscape(t.session), nil); err != nil {
			fmt.Println(t.col("  ✗ "+err.Error(), cErr))
		} else {
			t.first = true
			fmt.Printf("  %s\n\n", t.col("conversation cleared", cOK))
		}
	case "/driver":
		if len(fields) < 2 {
			fmt.Println(t.col("  usage: /driver ID  (see wrapi drivers)", cWarn))
			break
		}
		var out struct{ Session string `json:"session"` }
		if err := apiPost(t.addr, "/v1/sessions", map[string]any{"driver": fields[1]}, &out); err != nil {
			fmt.Println(t.col("  ✗ "+err.Error(), cErr))
			break
		}
		t.driver = fields[1]
		t.session = out.Session
		t.first = true
		saveCurrentSession(t.session)
		fmt.Printf("  %s %s\n\n", t.col("switched to:", cOK), t.col(t.driver, cAccB))
	case "/sources":
		t.showSrc = !t.showSrc
		fmt.Printf("  %s %s\n\n", t.col("sources:", cOK), t.col(fmt.Sprintf("%v", t.showSrc), cAcc))
	case "/session":
		fmt.Printf("  driver %s · session %s\n\n", t.col(t.driver, cAccB), t.col(t.session, cAcc))
	default:
		fmt.Println(t.col("  unknown command — /help", cWarn))
	}
	return false
}

func hostOf(u string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.Index(s, "/"); i > 0 {
		s = s[:i]
	}
	return s
}

func runeLen(s string) int { return len([]rune(s)) }

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

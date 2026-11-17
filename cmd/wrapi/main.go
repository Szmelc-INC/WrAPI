// Command wrapi serves a localhost API that queries Google AI Mode
// (udm=50) through a real Chromium driven by Playwright, and doubles as a
// client for that API plus a systemd service manager.
//
// Subcommands:
//
//	wrapi serve   start the HTTP server (default when run without args)
//	wrapi say     ask in the current session, printing the answer
//	wrapi ask     one-shot fresh query, prints the answer
//	wrapi chat    interactive REPL over one conversation session
//	wrapi pipe    JSONL stdin→stdout bridge for scripting
//	wrapi new|clear|sessions|rm   manage conversations
//	wrapi window  inspect / re-place the browser window
//	wrapi start|stop|restart|enable|disable|status|logs   systemd control
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"wrapi/internal/engine"
	"wrapi/internal/server"
)

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := parseBool(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: expected bool, got %q\n", key, v)
		os.Exit(1)
	}
	return b
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	var b bool
	err := json.Unmarshal([]byte(s), &b)
	return b, err
}

func main() {
	if len(os.Args) < 2 {
		serveCmd(os.Args[1:])
		return
	}
	switch os.Args[1] {
	case "serve":
		serveCmd(os.Args[2:])
	case "ask", "say":
		askCmd(os.Args[2:])
	case "chat":
		chatCmd(os.Args[2:])
	case "pipe":
		pipeCmd(os.Args[2:])
	case "new":
		newCmd(os.Args[2:])
	case "clear", "reset":
		clearCmd(os.Args[2:])
	case "sessions", "ls":
		sessionsCmd(os.Args[2:])
	case "rm":
		rmCmd(os.Args[2:])
	case "window":
		windowCmd(os.Args[2:])
	case "drivers":
		driversCmd(os.Args[2:])
	case "login":
		loginCmd(os.Args[2:])
	case "status":
		statusCmd(os.Args[2:])
	case "logs":
		logsCmd(os.Args[2:])
	case "start", "stop", "restart", "enable", "disable", "is-active":
		systemdCmd(os.Args[1], os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: wrapi <command> [flags]

talking to it:
  say PROMPT     one-shot query in the current session; prints the answer
  ask PROMPT     same, but always a fresh session (no context)
  chat           interactive REPL over one conversation session
  pipe           JSONL bridge: {"prompt"} lines in, result objects out

managing conversations:
  new            start a fresh session, print its id (sets WRAPI_SESSION hint)
  clear          reset the session's conversation (keeps the session)
  sessions       list sessions (turns, created)
  rm ID          delete a session

service control (systemd --user):
  start          start wrapi.service          stop     stop it
  restart        restart it                   enable   start on login
  disable        do not start on login        is-active  check status
  status         service + engine + window overview
  logs [-n N]    follow the service journal (default 50 lines)

window control:
  window         show where the browser window lives now
  window MODE    re-place it: workspace | mini | visible

drivers:
  drivers        list installed site drivers (default marked)
  login DRIVER   open the driver's login page (e.g. meta-ai QR scan)
  (-driver ID on say/ask/chat/pipe/new selects the site)

serve flags:
  -mode workspace|mini|visible|headless   browser window mode (default workspace)
  -workspace N     workspace number for workspace/visible modes (default 8)
  -addr HOST:PORT  listen address (default 127.0.0.1:8787)
  -profile DIR     persistent browser profile
  -timeout DUR     default per-request timeout (default 90s)
  -hl LANG         Google UI language

client flags (say/ask/chat/pipe/clear):
  -addr HOST:PORT  server address (default 127.0.0.1:8787)
  -session NAME    session id (say defaults to the last one used; see -c)
  -continue        say: send as a follow-up to the current conversation
  -sources         also print source URLs
`)
}

// --- serve ---

func serveCmd(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	mode := fs.String("mode", envStr("WRAPI_MODE", engine.ModeWorkspace), "window mode: workspace|mini|visible|headless")
	workspace := fs.Int("workspace", envInt("WRAPI_WORKSPACE", 8), "workspace number for workspace/visible modes")
	addr := fs.String("addr", envStr("WRAPI_ADDR", "127.0.0.1:8787"), "listen address")
	profile := fs.String("profile", envStr("WRAPI_PROFILE_DIR", "data/profile"), "browser profile dir")
	miniW := fs.Int("mini-width", envInt("WRAPI_MINI_WIDTH", 500), "mini window width")
	miniH := fs.Int("mini-height", envInt("WRAPI_MINI_HEIGHT", 120), "mini window height")
	hl := fs.String("hl", envStr("WRAPI_HL", "en"), "Google UI language")
	timeout := fs.Duration("timeout", envDur("WRAPI_TIMEOUT", 90*time.Second), "default request timeout")
	driversDir := fs.String("drivers", envStr("WRAPI_DRIVERS_DIR", "drivers"), "driver specs directory")
	defaultDriver := fs.String("driver", envStr("WRAPI_DRIVER", ""), "default driver id (first alphabetically when empty)")
	_ = fs.Parse(args)

	switch *mode {
	case engine.ModeWorkspace, engine.ModeMini, engine.ModeVisible, engine.ModeHeadless:
	default:
		fmt.Fprintf(os.Stderr, "invalid -mode %q (want workspace|mini|visible|headless)\n", *mode)
		os.Exit(2)
	}
	cfg := engine.Config{
		Addr: *addr, ProfileDir: *profile, WindowMode: *mode, Workspace: *workspace,
		MiniWidth: *miniW, MiniHeight: *miniH, HL: *hl, Timeout: *timeout,
		DriversDir: *driversDir, DefaultDriver: *defaultDriver,
	}
	fmt.Fprintf(os.Stderr, "wrapi: serving on %s (window=%s, profile=%s)\n", cfg.Addr, cfg.WindowMode, cfg.ProfileDir)
	if cfg.WindowMode == engine.ModeHeadless {
		fmt.Fprintln(os.Stderr, "wrapi: WARNING headless mode is frequently bot-walled by Google")
	}
	e, err := engine.Start(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "engine start: %v\n", err)
		os.Exit(1)
	}
	defer e.Close()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           server.New(e, cfg.Timeout),
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Fprintln(os.Stderr, srv.ListenAndServe())
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func envDur(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// --- shared client helpers ---

// clientFlags registers the shared -addr, -session and -driver flags on fs.
func clientFlags(fs *flag.FlagSet, defSession string) (addr, session, drv *string) {
	addr = fs.String("addr", envStr("WRAPI_ADDR", "127.0.0.1:8787"), "server address")
	session = fs.String("session", defSession, "session id")
	drv = fs.String("driver", envStr("WRAPI_DRIVER", ""), "driver id (site) to use")
	return addr, session, drv
}

// apiPost POSTs JSON and returns the decoded response.
func apiPost[T any](addr, path string, body any, out *T) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := http.Post("http://"+addr+path, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return json.Unmarshal(raw, out)
}

type apiAnswer struct {
	Answer  string          `json:"answer"`
	Sources []engine.Source `json:"sources"`
	Session string          `json:"session"`
	Mode    string          `json:"mode"`
	Error   string          `json:"error"`
}

// stateFilePath is where `wrapi say` remembers the current session id.
func stateFilePath() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return v + "/wrapi/session"
	}
	home, _ := os.UserHomeDir()
	return home + "/.local/state/wrapi/session"
}

func loadCurrentSession() string {
	b, err := os.ReadFile(stateFilePath())
	if err != nil {
		return "default"
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "default"
	}
	return s
}

func saveCurrentSession(id string) {
	_ = os.MkdirAll(filepath.Dir(stateFilePath()), 0o700)
	_ = os.WriteFile(stateFilePath(), []byte(id), 0o600)
}

// --- ask / say ---

// askCmd implements both: `wrapi ask` (always fresh session context) and
// `wrapi say` (continues the remembered session via /v1/say).
func askCmd(args []string) {
	var sources bool
	sayMode := false
	// Peek the verb before flag parsing: it decides default session
	// handling. os.Args[1] is reliable here because askCmd is only routed
	// from main().
	sayMode = len(os.Args) > 1 && os.Args[1] == "say"

	fs := flag.NewFlagSet("say", flag.ExitOnError)
	addr, session, driverID := clientFlags(fs, "")
	fs.BoolVar(&sources, "sources", false, "print sources too")
	fs.Parse(args)
	prompt := strings.Join(fs.Args(), " ")
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "usage: wrapi say \"prompt\"   (or: wrapi ask \"prompt\")")
		os.Exit(2)
	}

	sess := *session
	if sess == "" {
		if sayMode {
			sess = loadCurrentSession()
		} else {
			sess = fmt.Sprintf("ask-%d", time.Now().Unix())
		}
	}
	path := "/v1/ask"
	if sayMode {
		path = "/v1/say"
	}
	var out apiAnswer
	if err := apiPost(*addr, path, map[string]any{"prompt": prompt, "session": sess, "driver": *driverID}, &out); err != nil {
		fmt.Fprintf(os.Stderr, "wrapi: %v\n", err)
		os.Exit(1)
	}
	if sayMode && *session == "" {
		saveCurrentSession(out.Session)
	}
	fmt.Println(out.Answer)
	if sources {
		for _, s := range out.Sources {
			fmt.Printf("  [%s] %s\n", s.Site, s.URL)
		}
	}
}

// --- conversation management ---

func newCmd(args []string) {
	fs := flag.NewFlagSet("new", flag.ExitOnError)
	addr, _, driverID := clientFlags(fs, "")
	fs.Parse(args)
	sess, err := timeBasedSession()
	if err != nil {
		fmt.Fprintf(os.Stderr, "new: %v\n", err)
		os.Exit(1)
	}
	var out struct {
		Session string `json:"session"`
	}
	if err := apiPost(*addr, "/v1/sessions", map[string]any{"id": sess, "driver": *driverID}, &out); err != nil {
		fmt.Fprintf(os.Stderr, "new: %v\n", err)
		os.Exit(1)
	}
	saveCurrentSession(out.Session)
	fmt.Println(out.Session)
}

func timeBasedSession() (string, error) {
	return fmt.Sprintf("s%d", time.Now().Unix()), nil
}

func clearCmd(args []string) {
	fs := flag.NewFlagSet("clear", flag.ExitOnError)
	addr, session, _ := clientFlags(fs, "")
	fs.Parse(args)
	sess := *session
	if sess == "" {
		sess = loadCurrentSession()
	}
	if err := apiPostVoid(*addr, "/v1/reset?session="+urlQueryEscape(sess), nil); err != nil {
		fmt.Fprintf(os.Stderr, "clear: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "cleared session %s\n", sess)
}

func sessionsCmd(args []string) {
	fs := flag.NewFlagSet("sessions", flag.ExitOnError)
	addr, _, _ := clientFlags(fs, "")
	fs.Parse(args)
	var out struct {
		Sessions []struct {
			ID      string `json:"id"`
			Turns   int    `json:"turns"`
			URL     string `json:"url"`
			Created string `json:"created"`
		} `json:"sessions"`
	}
	if err := apiGet(*addr, "/v1/sessions", &out); err != nil {
		fmt.Fprintf(os.Stderr, "sessions: %v\n", err)
		os.Exit(1)
	}
	cur := loadCurrentSession()
	for _, s := range out.Sessions {
		mark := " "
		if s.ID == cur {
			mark = "*"
		}
		fmt.Printf("%s %-24s %3d turns  %s\n", mark, s.ID, s.Turns, s.Created)
	}
}

func rmCmd(args []string) {
	fs := flag.NewFlagSet("rm", flag.ExitOnError)
	addr, _, _ := clientFlags(fs, "")
	fs.Parse(args)
	id := fs.Arg(0)
	if id == "" {
		fmt.Fprintln(os.Stderr, "usage: wrapi rm SESSION_ID")
		os.Exit(2)
	}
	if err := apiDelete(*addr, "/v1/sessions?id="+urlQueryEscape(id)); err != nil {
		fmt.Fprintf(os.Stderr, "rm: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "deleted session %s\n", id)
}

// --- drivers / login ---

func driversCmd(args []string) {
	fs := flag.NewFlagSet("drivers", flag.ExitOnError)
	addr, _, _ := clientFlags(fs, "")
	fs.Parse(args)
	var out struct {
		Data []struct {
			ID    string         `json:"id"`
			Wrapi map[string]any `json:"wrapi"`
		} `json:"data"`
	}
	if err := apiGet(*addr, "/v1/models", &out); err != nil {
		fmt.Fprintf(os.Stderr, "drivers: %v\n", err)
		os.Exit(1)
	}
	for _, m := range out.Data {
		def, _ := m.Wrapi["default"].(bool)
		label, _ := m.Wrapi["label"].(string)
		mark := " "
		if def {
			mark = "*"
		}
		fmt.Printf("%s %-14s %s\n", mark, m.ID, label)
	}
}

func loginCmd(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	addr, _, driverID := clientFlags(fs, "")
	fs.Parse(args)
	drv := fs.Arg(0)
	if drv == "" {
		drv = *driverID
	}
	if drv == "" {
		fmt.Fprintln(os.Stderr, "usage: wrapi login DRIVER   (see wrapi drivers)")
		os.Exit(2)
	}
	var out struct {
		Status string `json:"status"`
		Hint   string `json:"hint"`
	}
	if err := apiPost(*addr, "/v1/login", map[string]any{"driver": drv}, &out); err != nil {
		fmt.Fprintf(os.Stderr, "login: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(out.Status)
	fmt.Println(out.Hint)
}

// --- window / status / logs ---

func windowCmd(args []string) {
	fs := flag.NewFlagSet("window", flag.ExitOnError)
	addr, _, _ := clientFlags(fs, "")
	fs.Parse(args)
	mode := fs.Arg(0)
	var out map[string]any
	if mode == "" {
		if err := apiGet(*addr, "/v1/window", &out); err != nil {
			fmt.Fprintf(os.Stderr, "window: %v\n", err)
			os.Exit(1)
		}
	} else {
		if err := apiPost(*addr, "/v1/window", map[string]any{"mode": mode}, &out); err != nil {
			fmt.Fprintf(os.Stderr, "window: %v\n", err)
			os.Exit(1)
		}
	}
	printWindowState(out)
}

func printWindowState(out map[string]any) {
	if v, _ := out["present"].(bool); !v {
		reason, _ := out["error"].(string)
		fmt.Printf("window: not found (%s)\n", reason)
		return
	}
	ws, _ := out["workspace"].(float64)
	fl, _ := out["floating"].(bool)
	at, _ := out["at"].([]any)
	size, _ := out["size"].([]any)
	mode, _ := out["mode"].(string)
	pos := "-"
	if len(at) == 2 {
		pos = fmt.Sprintf("%v,%v", at[0], at[1])
	}
	dim := "-"
	if len(size) == 2 {
		dim = fmt.Sprintf("%vx%v", size[0], size[1])
	}
	fmt.Printf("mode=%s workspace=%d floating=%v at=%s size=%s\n", mode, int(ws), fl, pos, dim)
}

func statusCmd(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	addr, _, _ := clientFlags(fs, "")
	fs.Parse(args)

	unit, err := execSilent("systemctl", "--user", "is-active", "wrapi.service")
	fmt.Printf("service:  %s (%v)\n", unit, err == nil)
	var health map[string]any
	if err := apiGet(*addr, "/health", &health); err != nil {
		fmt.Printf("engine:   unreachable: %v\n", err)
		return
	}
	fmt.Printf("engine:   %s (model %s)\n", health["status"], health["model"])
	var win map[string]any
	if err := apiGet(*addr, "/v1/window", &win); err == nil {
		printWindowState(win)
	}
	var sess struct {
		Sessions []struct {
			ID    string `json:"id"`
			Turns int    `json:"turns"`
		} `json:"sessions"`
	}
	if err := apiGet(*addr, "/v1/sessions", &sess); err == nil {
		fmt.Printf("sessions: %d (current: %s)\n", len(sess.Sessions), loadCurrentSession())
	}
}

func logsCmd(args []string) {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	n := fs.Int("n", 50, "number of lines")
	fs.Parse(args)
	cmd := exec.Command("journalctl", "--user", "-u", "wrapi.service", "-n", fmt.Sprintf("%d", *n), "--no-pager")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

// --- systemd control ---

func systemdCmd(verb string, args []string) {
	fs := flag.NewFlagSet(verb, flag.ExitOnError)
	fs.Parse(args)
	cmd := exec.Command("systemctl", "--user", verb, "wrapi.service")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "systemctl --user %s wrapi.service: %v\n", verb, err)
		os.Exit(1)
	}
}

// --- small client helpers ---

func apiGet[T any](addr, path string, out *T) error {
	resp, err := http.Get("http://" + addr + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return json.Unmarshal(raw, out)
}

func apiPostVoid(addr, path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := http.Post("http://"+addr+path, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

func apiDelete(addr, path string) error {
	req, err := http.NewRequest(http.MethodDelete, "http://"+addr+path, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

func urlQueryEscape(s string) string {
	return strings.ReplaceAll(s, " ", "%20")
}

func execSilent(name string, arg ...string) (string, error) {
	out, err := exec.Command(name, arg...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// --- chat (REPL) --—

func chatCmd(args []string) {
	fs := flag.NewFlagSet("chat", flag.ExitOnError)
	addr, session, driverID := clientFlags(fs, "wrapi-repl")
	newSess := fs.Bool("new", false, "start a fresh session")
	fs.Parse(args)
	if *newSess {
		*session = fmt.Sprintf("wrapi-%d", time.Now().Unix())
	}
	fmt.Fprintf(os.Stderr, "wrapi chat — session %s on %s (empty line or Ctrl-D exits)\n", *session, *addr)

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	first := true
	for {
		fmt.Fprint(os.Stderr, "> ")
		if !sc.Scan() {
			break
		}
		prompt := strings.TrimSpace(sc.Text())
		if prompt == "" {
			break
		}
		path := "/v1/followup"
		if first {
			path = "/v1/ask"
			first = false
		}
		var out apiAnswer
		if err := apiPost(*addr, path, map[string]any{"prompt": prompt, "session": *session, "driver": *driverID}, &out); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			continue
		}
		fmt.Println(out.Answer)
		fmt.Println()
	}
}

// --- pipe (JSONL bridge) --—

func pipeCmd(args []string) {
	fs := flag.NewFlagSet("pipe", flag.ExitOnError)
	addr, _, driverID := clientFlags(fs, "")
	fs.Parse(args)

	enc := json.NewEncoder(os.Stdout)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req struct {
			Prompt   string `json:"prompt"`
			Session  string `json:"session"`
			Driver   string `json:"driver"`
			Fresh    *bool  `json:"fresh"`
			Followup bool   `json:"followup"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			_ = enc.Encode(map[string]any{"error": "invalid JSONL: " + err.Error()})
			continue
		}
		if req.Prompt == "" {
			_ = enc.Encode(map[string]any{"error": `field "prompt" required`})
			continue
		}
		path := "/v1/ask"
		if req.Followup || (req.Fresh != nil && !*req.Fresh) {
			path = "/v1/followup"
		}
		var out apiAnswer
		drv := req.Driver
		if drv == "" {
			drv = *driverID
		}
		if err := apiPost(*addr, path, map[string]any{
			"prompt": req.Prompt, "session": req.Session, "driver": drv,
		}, &out); err != nil {
			_ = enc.Encode(map[string]any{"error": err.Error()})
			continue
		}
		_ = enc.Encode(out)
	}
}

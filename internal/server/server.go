// Package server exposes the engine as a localhost JSON HTTP API: the
// native WRAPI endpoints plus an OpenAI-compatible /v1/chat/completions
// (with SSE streaming), so stock LLM tooling can talk to Google AI Mode.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"wrapi/internal/engine"
)

// ModelID is what we advertise to OpenAI-compatible clients.
const ModelID = "wrapi"

// Server wires engine calls to HTTP handlers.
type Server struct {
	eng        *engine.Engine
	defTimeout time.Duration
	mux        *http.ServeMux
}

// New builds the handler set. All endpoints speak JSON (except the debug
// screenshot/HTML ones, which return raw bytes).
func New(e *engine.Engine, defTimeout time.Duration) *Server {
	s := &Server{eng: e, defTimeout: defTimeout, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /health", s.handleHealth)
	// OpenAI-compatible surface.
	s.mux.HandleFunc("GET /v1/models", s.handleModels)
	s.mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	// Native surface.
	s.mux.HandleFunc("POST /v1/ask", s.handleAsk)
	s.mux.HandleFunc("POST /v1/say", s.handleSay)
	s.mux.HandleFunc("POST /v1/followup", s.handleFollowUp)
	s.mux.HandleFunc("GET /v1/sessions", s.handleSessions)
	s.mux.HandleFunc("POST /v1/sessions", s.handleSessionCreate)
	s.mux.HandleFunc("POST /v1/reset", s.handleReset)
	s.mux.HandleFunc("GET /v1/window", s.handleWindowGet)
	s.mux.HandleFunc("POST /v1/window", s.handleWindowPlace)
	s.mux.HandleFunc("POST /v1/login", s.handleLogin)
	s.mux.HandleFunc("DELETE /v1/sessions", s.handleSessionDelete)
	s.mux.HandleFunc("GET /v1/screenshot", s.handleScreenshot)
	s.mux.HandleFunc("GET /v1/page.html", s.handlePageHTML)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// --- OpenAI-compatible ---

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	Session  string        `json:"session"` // WRAPI extension: conversation to use
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	drivers := s.eng.Drivers().List()
	data := make([]map[string]any, 0, len(drivers))
	for _, d := range drivers {
		data = append(data, map[string]any{
			"id": d["id"], "object": "model",
			"created": 1730000000, "owned_by": "wrapi",
			"wrapi": d,
		})
	}
	respondJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 {
		http.Error(w, `field "messages" is required`, http.StatusBadRequest)
		return
	}
	var lastUser string
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			lastUser = req.Messages[i].Content
			break
		}
	}
	if strings.TrimSpace(lastUser) == "" {
		http.Error(w, "no user message found", http.StatusBadRequest)
		return
	}
	timeout := s.defTimeout

	// Session selection: an explicit session keeps site-side context via
	// follow-ups; without one the transcript is flattened into a fresh ask.
	// The model field doubles as the driver selector ("wrapi/<driver-id>",
	// "wrapi", or a bare driver id).
	driverID := req.Model
	if i := strings.Index(driverID, "/"); i >= 0 {
		driverID = driverID[i+1:]
	}
	sessID := req.Session
	if sessID == "" {
		sessID = "openai-" + fmt.Sprintf("%d", time.Now().UnixNano()%1e6)
	}
	sess, err := s.eng.Session(sessID)
	if err != nil {
		sess, err = s.eng.NewSession(sessID, driverID)
		if err != nil {
			respondErr(w, err)
			return
		}
	}
	fresh := sess.Turns == 0
	prompt := lastUser
	if fresh && len(req.Messages) > 1 {
		prompt = flattenTranscript(req.Messages)
	}

	id := "chatcmpl-" + sessID + "-" + fmt.Sprintf("%d", time.Now().Unix())
	created := time.Now().Unix()

	if req.Stream {
		s.streamChat(w, sess, s.eng, fresh, prompt, id, created, timeout)
		return
	}

	var a *engine.Answer
	if fresh {
		a, err = sess.Ask(s.eng, prompt, timeout, nil)
	} else {
		a, err = sess.FollowUp(s.eng, prompt, timeout, nil)
	}
	if err != nil {
		respondErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"id": id, "object": "chat.completion", "created": created, "model": "wrapi/" + sess.Driver,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": a.Answer},
			"finish_reason": "stop",
		}},
		"usage": roughUsage(prompt, a.Answer),
		"wrapi": map[string]any{"sources": a.Sources, "url": a.URL, "session": sess.ID, "mode": a.Mode},
	})
}

// streamChat emits an OpenAI-style SSE stream: a role chunk, content delta
// chunks as the answer streams in, a stop chunk, then [DONE].
func (s *Server) streamChat(w http.ResponseWriter, sess *engine.Session, eng *engine.Engine, fresh bool, prompt, id string, created int64, timeout time.Duration) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	send := func(payload map[string]any) {
		b, _ := json.Marshal(payload)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	chunk := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": "wrapi/" + sess.Driver,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
		}
	}
	send(chunk(map[string]any{"role": "assistant"}, nil))

	var final *engine.Answer
	var err error
	onDelta := func(d string) { send(chunk(map[string]any{"content": d}, nil)) }
	if fresh {
		final, err = sess.Ask(eng, prompt, timeout, onDelta)
	} else {
		final, err = sess.FollowUp(eng, prompt, timeout, onDelta)
	}
	if err != nil {
		// Surface the failure inside the stream, then stop.
		send(chunk(map[string]any{"content": "\n[wrapi error] " + err.Error()}, "stop"))
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}
	// Final chunk carries stop plus WRAPI extras (sources etc.) as a
	// non-standard field that OpenAI clients simply ignore.
	send(map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": created, "model": "wrapi/" + sess.Driver,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		"wrapi":   map[string]any{"sources": final.Sources, "url": final.URL, "session": sess.ID, "mode": final.Mode},
	})
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// flattenTranscript renders a multi-message transcript as a single prompt
// for a fresh (context-free) AI Mode query.
func flattenTranscript(msgs []chatMessage) string {
	var b strings.Builder
	for i, m := range msgs {
		switch m.Role {
		case "system":
			fmt.Fprintf(&b, "[Instructions] %s\n", m.Content)
		case "assistant":
			fmt.Fprintf(&b, "Assistant: %s\n", m.Content)
		default:
			fmt.Fprintf(&b, "User: %s\n", m.Content)
		}
		_ = i
	}
	b.WriteString("\nUsing the conversation above as context, answer the last user message.")
	return b.String()
}

func roughUsage(prompt, completion string) map[string]any {
	pt, ct := len(prompt)/4, len(completion)/4
	return map[string]any{
		"prompt_tokens": pt, "completion_tokens": ct,
		"total_tokens": pt + ct,
	}
}

// --- native WRAPI endpoints ---

type askRequest struct {
	Prompt   string `json:"prompt"`
	Fresh    *bool  `json:"fresh,omitempty"`
	Stream   bool   `json:"stream,omitempty"`
	Session  string `json:"session,omitempty"`
	Driver   string `json:"driver,omitempty"`
	TimeoutS int    `json:"timeout_s,omitempty"`
}

func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	var req askRequest
	if err := decodeAsk(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sess, err := s.sessionFor(req.Session, req.Driver)
	if err != nil {
		respondErr(w, err)
		return
	}
	timeout := s.timeout(req.TimeoutS)
	start := time.Now()

	if req.Stream {
		s.streamNative(w, sess, true, req.Prompt, timeout, start)
		return
	}
	a, err := sess.Ask(s.eng, req.Prompt, timeout, nil)
	if err != nil {
		respondErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"answer": a.Answer, "sources": a.Sources, "url": a.URL, "mode": a.Mode,
		"session": sess.ID, "elapsed_ms": time.Since(start).Milliseconds(),
	})
}

// handleSay routes smartly: a session with no turns gets a fresh ask, an
// existing conversation gets a follow-up. This is what the `wrapi say`
// CLI command uses, so terminal usage feels like one continuous chat.
func (s *Server) handleSay(w http.ResponseWriter, r *http.Request) {
	var req askRequest
	if err := decodeAsk(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sess, err := s.sessionFor(req.Session, req.Driver)
	if err != nil {
		respondErr(w, err)
		return
	}
	timeout := s.timeout(req.TimeoutS)
	start := time.Now()

	if req.Stream {
		s.streamNative(w, sess, sess.Turns == 0, req.Prompt, timeout, start)
		return
	}
	var a *engine.Answer
	if sess.Turns == 0 {
		a, err = sess.Ask(s.eng, req.Prompt, timeout, nil)
	} else {
		a, err = sess.FollowUp(s.eng, req.Prompt, timeout, nil)
	}
	if err != nil {
		respondErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"answer": a.Answer, "sources": a.Sources, "url": a.URL, "mode": a.Mode,
		"session": sess.ID, "elapsed_ms": time.Since(start).Milliseconds(),
	})
}

func (s *Server) handleFollowUp(w http.ResponseWriter, r *http.Request) {
	var req askRequest
	if err := decodeAsk(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sess, err := s.sessionFor(req.Session, req.Driver)
	if err != nil {
		respondErr(w, err)
		return
	}
	timeout := s.timeout(req.TimeoutS)
	start := time.Now()

	if req.Stream {
		s.streamNative(w, sess, false, req.Prompt, timeout, start)
		return
	}
	a, err := sess.FollowUp(s.eng, req.Prompt, timeout, nil)
	if err != nil {
		respondErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"answer": a.Answer, "sources": a.Sources, "url": a.URL, "mode": a.Mode,
		"session": sess.ID, "elapsed_ms": time.Since(start).Milliseconds(),
	})
}

// streamNative streams the native API as JSONL: one delta object per line,
// terminated by a final object carrying the complete result.
func (s *Server) streamNative(w http.ResponseWriter, sess *engine.Session, fresh bool, prompt string, timeout time.Duration, start time.Time) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	enc := json.NewEncoder(w)
	send := func(v any) { _ = enc.Encode(v); flusher.Flush() }

	onDelta := func(d string) { send(map[string]any{"delta": d}) }
	var a *engine.Answer
	var err error
	if fresh {
		a, err = sess.Ask(s.eng, prompt, timeout, onDelta)
	} else {
		a, err = sess.FollowUp(s.eng, prompt, timeout, onDelta)
	}
	if err != nil {
		send(map[string]any{"error": err.Error()})
		return
	}
	send(map[string]any{
		"answer": a.Answer, "sources": a.Sources, "url": a.URL, "mode": a.Mode,
		"session": sess.ID, "elapsed_ms": time.Since(start).Milliseconds(),
	})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{"sessions": s.eng.Sessions()})
}

func (s *Server) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     string `json:"id"`
		Driver string `json:"driver"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	sess, err := s.eng.NewSession(req.ID, req.Driver)
	if err != nil {
		respondErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"session": sess.ID, "driver": sess.Driver})
}

// handleLogin opens the driver's login page in the shared browser so a
// human can authenticate once (QR scan etc.); the persistent profile keeps
// the session.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Driver string `json:"driver"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	hint, err := s.eng.OpenLogin(req.Driver)
	if err != nil {
		respondErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"status": "login page opened", "hint": hint})
}

func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "query param id is required", http.StatusBadRequest)
		return
	}
	if err := s.eng.DeleteSession(id); err != nil {
		respondErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"deleted": id})
}

func (s *Server) handleWindowGet(w http.ResponseWriter, r *http.Request) {
	state, err := s.eng.WindowState()
	if err != nil {
		respondErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, state)
}

func (s *Server) handleWindowPlace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	state, err := s.eng.Place(req.Mode)
	if err != nil {
		respondErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, state)
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	sess, err := s.sessionFor(r.URL.Query().Get("session"), r.URL.Query().Get("driver"))
	if err != nil {
		respondErr(w, err)
		return
	}
	if err := sess.Reset(); err != nil {
		respondErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "reset"})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"engine": "google-ai-mode-udm50",
		"model":  "wrapi (" + s.eng.Drivers().Default().ID + " default)",
	})
}

func (s *Server) handleScreenshot(w http.ResponseWriter, r *http.Request) {
	sess, err := s.sessionFor(r.URL.Query().Get("session"), r.URL.Query().Get("driver"))
	if err != nil {
		respondErr(w, err)
		return
	}
	png, err := sess.ScreenshotPNG()
	if err != nil {
		respondErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(png)
}

func (s *Server) handlePageHTML(w http.ResponseWriter, r *http.Request) {
	sess, err := s.sessionFor(r.URL.Query().Get("session"), r.URL.Query().Get("driver"))
	if err != nil {
		respondErr(w, err)
		return
	}
	html, err := sess.PageHTML()
	if err != nil {
		respondErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(html))
}

// --- helpers ---

func decodeAsk(r *http.Request, req *askRequest) error {
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		return errors.New("invalid JSON body: " + err.Error())
	}
	if req.Prompt == "" {
		return errors.New(`field "prompt" is required`)
	}
	return nil
}

// sessionFor resolves or creates the requested session ("default" when
// empty), binding it to the requested driver.
func (s *Server) sessionFor(id, driverID string) (*engine.Session, error) {
	if id == "" {
		id = "default"
	}
	if sess, err := s.eng.Session(id); err == nil {
		return sess, nil
	}
	return s.eng.NewSession(id, driverID)
}

func (s *Server) timeout(reqS int) time.Duration {
	if reqS > 0 {
		return time.Duration(reqS) * time.Second
	}
	return s.defTimeout
}

func respondErr(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, engine.ErrBlocked) {
		status = http.StatusForbidden
	}
	if status == http.StatusInternalServerError {
		log.Printf("request failed: %v", err)
	}
	respondJSON(w, status, map[string]string{"error": err.Error()})
}

func respondJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

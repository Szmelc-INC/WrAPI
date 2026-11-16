// Package server exposes the engine as a localhost JSON HTTP API.
package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"wrapi/internal/engine"
)

// Server wires engine calls to HTTP handlers.
type Server struct {
	engine     *engine.Engine
	defTimeout time.Duration
	mux        *http.ServeMux
}

// New builds the handler set. All endpoints speak JSON (except the debug
// screenshot/HTML ones, which return raw bytes).
func New(e *engine.Engine, defTimeout time.Duration) *Server {
	s := &Server{engine: e, defTimeout: defTimeout, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("POST /v1/ask", s.handleAsk)
	s.mux.HandleFunc("POST /v1/followup", s.handleFollowUp)
	s.mux.HandleFunc("POST /v1/reset", s.handleReset)
	s.mux.HandleFunc("GET /v1/screenshot", s.handleScreenshot)
	s.mux.HandleFunc("GET /v1/page.html", s.handlePageHTML)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

type askRequest struct {
	Prompt   string `json:"prompt"`
	Fresh    *bool  `json:"fresh,omitempty"`
	TimeoutS int    `json:"timeout_s,omitempty"`
}

func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	var req askRequest
	if err := decode(w, r, &req); err != nil {
		return
	}
	fresh := true
	if req.Fresh != nil {
		fresh = *req.Fresh
	}
	timeout := s.timeout(req.TimeoutS)

	start := time.Now()
	a, err := s.engine.Ask(req.Prompt, fresh, timeout)
	if err != nil {
		respondErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"answer":     a.Answer,
		"sources":    a.Sources,
		"url":        a.URL,
		"mode":       a.Mode,
		"elapsed_ms": time.Since(start).Milliseconds(),
	})
}

func (s *Server) handleFollowUp(w http.ResponseWriter, r *http.Request) {
	var req askRequest
	if err := decode(w, r, &req); err != nil {
		return
	}
	timeout := s.timeout(req.TimeoutS)
	start := time.Now()
	a, err := s.engine.FollowUp(req.Prompt, timeout)
	if err != nil {
		respondErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"answer":     a.Answer,
		"sources":    a.Sources,
		"url":        a.URL,
		"mode":       a.Mode,
		"elapsed_ms": time.Since(start).Milliseconds(),
	})
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if err := s.engine.Reset(); err != nil {
		respondErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "reset"})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"engine": "google-ai-mode-udm50",
	})
}

func (s *Server) handleScreenshot(w http.ResponseWriter, r *http.Request) {
	png, err := s.engine.ScreenshotPNG()
	if err != nil {
		respondErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(png)
}

func (s *Server) handlePageHTML(w http.ResponseWriter, r *http.Request) {
	html, err := s.engine.PageHTML()
	if err != nil {
		respondErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(html))
}

func decode(w http.ResponseWriter, r *http.Request, v interface{}) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return err
	}
	req := v.(*askRequest)
	if req.Prompt == "" {
		http.Error(w, `field "prompt" is required`, http.StatusBadRequest)
		return errors.New("empty prompt")
	}
	return nil
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

func respondJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

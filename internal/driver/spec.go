// Package driver defines the declarative site specs that teach the engine
// how to talk to an AI on a real web page: where to type, how to submit,
// how to read the streaming answer back, and how to harvest sources.
//
// A driver is a JSON file in the drivers/ directory. Everything the engine
// does is driven by these specs — adding a new site means dropping in a
// new JSON file, no recompilation.
package driver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// StartSpec describes how a fresh conversation begins.
type StartSpec struct {
	// Strategy: "url" puts the prompt in the start URL (search-style sites);
	// "input" loads the page, optionally clicks OpenButton, then types the
	// prompt into the composer.
	Strategy string `json:"strategy"` // url | input
	// URL is the start page; {prompt} and {hl} are substituted. For the
	// "input" strategy it is just the page to open.
	URL string `json:"url"`
	// OpenSelector (input strategy) is clicked after load before typing,
	// e.g. the button that opens the AI chat.
	OpenSelector string `json:"open_selector,omitempty"`
	// WaitSelector must become visible before the engine proceeds.
	WaitSelector string `json:"wait_selector"`
}

// FollowupSpec describes the conversation composer.
type FollowupSpec struct {
	InputSelector string `json:"input_selector"`
	// InputMode: "fill" uses Playwright fill (inputs + contenteditable),
	// "type" focuses and types (some custom editors need it).
	InputMode string `json:"input_mode,omitempty"` // fill | type
	// SendButton is the fallback submit button when Enter does not submit.
	SendButton string `json:"send_button,omitempty"`
	// FocusClick clicks the composer before typing (mirrors a human).
	FocusClick bool `json:"focus_click"`
}

// CleanupSpec strips boilerplate from the extracted answer.
type CleanupSpec struct {
	// StripPrefix removes a leading echo of the prompt
	// ("{prompt}" placeholder substituted).
	StripPrefix string `json:"strip_prefix,omitempty"`
	// StripSuffixContains truncates everything from this marker onwards.
	StripSuffixContains string `json:"strip_suffix_contains,omitempty"`
}

// Spec is one complete site driver.
type Spec struct {
	ID    string `json:"id"`
	Label string `json:"label"`

	Start         StartSpec    `json:"start"`
	Followup      FollowupSpec `json:"followup"`
	BlockedOnURL  string       `json:"blocked_on_url,omitempty"` // /sorry/ style wall marker
	LoginURL      string       `json:"login_url,omitempty"`      // where a human logs in once
	LoginHint     string       `json:"login_hint,omitempty"`     // shown by `wrapi login`
	ConsentJS     string       `json:"consent_js,omitempty"`     // click-through popups/consent
	StateJS       string       `json:"state_js"`                 // () => {ready, text, complete}
	TurnCountJS   string       `json:"turn_count_js,omitempty"`  // () => number
	PaneTextJS    string       `json:"pane_text_js,omitempty"`   // () => string (submit signal)
	ExtractJS     string       `json:"extract_js"`               // () => {answer, sources[], turns}
	Cleanup       CleanupSpec  `json:"cleanup"`
	ResolveGoto   bool         `json:"resolve_goto_links,omitempty"` // decrypt /goto?url= links
	StreamMinText int          `json:"stream_min_text,omitempty"`    // min chars before stream starts (default 50)
}

// Registry holds loaded specs by id.
type Registry struct {
	byID      map[string]*Spec
	order     []string
	defaultID string
}

// Load reads every *.json in dir and returns a registry. Default driver is
// the first spec alphabetically when preferred is missing.
func Load(dir, preferred string) (*Registry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("drivers dir %s: %w", dir, err)
	}
	r := &Registry{byID: map[string]*Spec{}}
	for _, e := range entries {
		if e.IsDir() || (!strings.HasSuffix(e.Name(), ".json")) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var s Spec
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if s.ID == "" || s.Label == "" || s.Start.URL == "" || s.Start.WaitSelector == "" ||
			s.StateJS == "" || s.ExtractJS == "" {
			return nil, fmt.Errorf("%s: fields id, label, start.url, start.wait_selector, state_js, extract_js are required", e.Name())
		}
		if s.Start.Strategy == "" {
			s.Start.Strategy = "url"
		}
		if s.Followup.InputMode == "" {
			s.Followup.InputMode = "fill"
		}
		if s.StreamMinText == 0 {
			s.StreamMinText = 50
		}
		if _, dup := r.byID[s.ID]; dup {
			return nil, fmt.Errorf("%s: duplicate driver id %q", e.Name(), s.ID)
		}
		r.byID[s.ID] = &s
		r.order = append(r.order, s.ID)
	}
	if len(r.order) == 0 {
		return nil, fmt.Errorf("drivers dir %s: no *.json driver files", dir)
	}
	sort.Strings(r.order)
	if _, ok := r.byID[preferred]; !ok {
		preferred = r.order[0]
	}
	r.defaultID = preferred
	return r, nil
}

// Get returns the spec by id, or the default when id is empty.
func (r *Registry) Get(id string) (*Spec, error) {
	if id == "" {
		id = r.defaultID
	}
	s, ok := r.byID[id]
	if !ok {
		return nil, fmt.Errorf("unknown driver %q (available: %s)", id, strings.Join(r.order, ", "))
	}
	return s, nil
}

// List returns metadata for /v1/models and `wrapi drivers`.
func (r *Registry) List() []map[string]any {
	out := make([]map[string]any, 0, len(r.order))
	for _, id := range r.order {
		s := r.byID[id]
		def := id == r.defaultID
		out = append(out, map[string]any{
			"id": s.ID, "label": s.Label, "default": def,
			"login_url": s.LoginURL, "login_hint": s.LoginHint,
		})
	}
	return out
}

// Default returns the default spec.
func (r *Registry) Default() *Spec { return r.byID[r.defaultID] }

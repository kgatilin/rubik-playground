package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultLimit = 100 // face turns per game

// Session is the one cube the server owns: solved + Scramble + History.
// The page is a view over its event stream; page buttons and `run --ui`
// change it through the same endpoints.
type Session struct {
	id       string // "" is the sandbox cube; a game session has a short number
	name     string // the player the session was registered for
	view     string // observation mode of the registration
	mu       sync.Mutex
	stepMu   sync.Mutex // one built-in decision at a time; s.mu is not held while a model thinks
	jev      *Jev
	gemini   map[string]*Gemini // one conversation per model per game
	run      string
	scramble []string
	history  []string
	game     *Game   // the registered game being played, nil outside one
	log      []event // scramble, move and decision events of the current game, replayed to a page that connects
	subs     map[chan []byte]struct{}
}

type event struct {
	Type     string      `json:"type"` // sync | move | scramble | decision | game
	Time     time.Time   `json:"time"` // moves of one move call share it
	Move     string      `json:"move,omitempty"`
	By       string      `json:"by,omitempty"` // who made the move: jev, page, cli, or an agent name
	Moves    []string    `json:"moves,omitempty"`
	Scramble []string    `json:"scramble"`
	History  []string    `json:"history"`
	Record   *StepRecord `json:"record,omitempty"`
	Game     *Game       `json:"game,omitempty"` // game: opened (no outcome) or closed
	View     string      `json:"view,omitempty"` // /api/state only: the observation mode the session's game was registered with
	Log      []event     `json:"log,omitempty"`  // sync only: the game so far, for the page log
}

var (
	errSolved = errors.New("cube is already solved")
	errLimit  = fmt.Errorf("turn limit of %d reached", defaultLimit)
)

// players lists what the page can pick: jev plus gemini:<model> for GEMINI_MODELS.
func players() []string {
	models := os.Getenv("GEMINI_MODELS")
	if models == "" {
		models = "gemini-3.8-flash,gemini-3.1-pro-preview,gemini-3.5-flash"
	}
	out := []string{"jev"}
	for _, m := range strings.Split(models, ",") {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, "gemini:"+m)
		}
	}
	return out
}

// Hub holds the cubes of one serve: the sandbox and one session per registration,
// so games run side by side. Registration hands out the session's short id, and
// ?game=<id> picks it.
type Hub struct {
	mu       sync.Mutex
	jev      *Jev
	sessions map[string]*Session
	order    []string // session ids in registration order, sandbox first
}

func newHub(jev *Jev) *Hub {
	return &Hub{jev: jev, sessions: map[string]*Session{"": newSession(jev)}, order: []string{""}}
}

func (h *Hub) get(id string) (*Session, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.sessions[id]; s != nil {
		return s, nil
	}
	return nil, fmt.Errorf("no game %q: register with play --as <name> and use the game number it prints", id)
}

// Play registers the player in a new session and starts its game.
func (h *Hub) Play(in PlayRequest) (*Game, error) {
	in.Player = strings.TrimSpace(in.Player)
	if in.Player == "" {
		return nil, errors.New("a game needs a player name")
	}
	if err := validObservation(in.Observation); err != nil {
		return nil, err
	}
	h.mu.Lock()
	s := newSession(h.jev)
	s.id, s.name, s.view = strconv.Itoa(len(h.order)), in.Player, in.Observation
	h.sessions[s.id] = s
	h.order = append(h.order, s.id)
	h.mu.Unlock()
	return s.Play(in)
}

// GameInfo is one line of the page's game switcher.
type GameInfo struct {
	ID      string `json:"id"`
	Player  string `json:"player"`
	Turns   int    `json:"turns"`
	Open    bool   `json:"open"` // a registered game is being played
	Solved  bool   `json:"solved"`
	Matched int    `json:"matched"`
}

func (h *Hub) games() []GameInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]GameInfo, 0, len(h.order))
	for _, id := range h.order {
		s := h.sessions[id]
		s.mu.Lock()
		cube := NewCube()
		cube.ApplyAll(s.scramble)
		cube.ApplyAll(s.history)
		out = append(out, GameInfo{ID: id, Player: s.name, Turns: len(s.history), Open: s.game != nil, Solved: cube.Solved(), Matched: cube.Matched()})
		s.mu.Unlock()
	}
	return out
}

func newSession(jev *Jev) *Session {
	return &Session{jev: jev, run: newRunID("ui"), gemini: map[string]*Gemini{}, subs: map[chan []byte]struct{}{}}
}

// publish sends an event to every connected page; callers hold s.mu.
func (s *Session) publish(e event) {
	e.Scramble, e.History = slices.Clone(s.scramble), slices.Clone(s.history)
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	if e.Type == "sync" {
		s.log = nil
	} else {
		s.log = append(s.log, e)
	}
	data, _ := json.Marshal(e)
	for ch := range s.subs {
		select {
		case ch <- data:
		default: // a stalled page resyncs on reconnect
		}
	}
}

func (s *Session) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeGame("abandoned")
	s.scramble, s.history, s.run, s.gemini = nil, nil, newRunID("ui"), map[string]*Gemini{}
	s.publish(event{Type: "sync"})
}

// Scramble folds the moves made so far into the scramble and starts a new run.
func (s *Session) Scramble(moves []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range moves {
		if !ValidMove(m) {
			return fmt.Errorf("unknown move %q", m)
		}
	}
	s.closeGame("abandoned")
	s.scramble = append(append(s.scramble, s.history...), moves...)
	s.history, s.run, s.gemini, s.log = nil, newRunID("ui"), map[string]*Gemini{}, nil
	s.publish(event{Type: "scramble", Moves: moves})
	return nil
}

// Move applies the moves of one call in order; nothing is applied if one is unknown,
// and the turn limit cuts the list.
func (s *Session) Move(moves []string, by string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range moves {
		if !ValidMove(m) {
			return fmt.Errorf("unknown move %q", m)
		}
	}
	by = cmp.Or(by, s.name, "cli")
	at := time.Now().UTC()
	for _, m := range moves {
		if len(s.history) >= defaultLimit {
			return errLimit
		}
		s.record(m, by, at)
	}
	return nil
}

// decider returns the built-in player for the request; callers hold s.mu.
func (s *Session) decider(player string) (Decider, error) {
	model, isGemini := strings.CutPrefix(player, "gemini:")
	if !isGemini {
		if player != "jev" {
			return nil, fmt.Errorf("%s is not a built-in player: it moves through the CLI", player)
		}
		if s.jev == nil {
			return nil, errors.New("JEV_API_TOKEN is not set (env or .env)")
		}
		return s.jev, nil
	}
	if g := s.gemini[model]; g != nil {
		return g, nil
	}
	g, err := newGemini(model)
	if err != nil {
		return nil, err
	}
	s.gemini[model] = g
	return g, nil
}

// Step asks a built-in player for one decision on the current cube and applies it.
// Cancelling ctx (the page's Stop closes the request) abandons the model call.
func (s *Session) Step(ctx context.Context, req StepRequest) (*StepRecord, error) {
	s.stepMu.Lock()
	defer s.stepMu.Unlock()

	if req.Player == "" {
		req.Player = "jev"
	}
	s.mu.Lock()
	req.Run, req.Limit = s.run, defaultLimit
	req.Scramble, req.History = slices.Clone(s.scramble), slices.Clone(s.history)
	cube := NewCube()
	cube.ApplyAll(s.scramble)
	cube.ApplyAll(s.history)
	if g := s.game; g != nil && g.Player != req.Player {
		s.mu.Unlock()
		return nil, fmt.Errorf("the game in progress belongs to %s", g.Player)
	} else if g != nil { // the game's category is fixed at registration
		req.Observation, req.Lookahead = g.Observation, g.Lookahead
	}
	d, err := s.decider(req.Player)
	s.mu.Unlock()
	switch {
	case err != nil:
		return nil, err
	case cube.Solved():
		return nil, errSolved
	case len(req.History) >= defaultLimit:
		return nil, errLimit
	}

	rec, err := d.Decide(ctx, req)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run != req.Run || len(s.history) != len(req.History) {
		return nil, errors.New("the cube changed while the player was deciding; decision dropped")
	}
	s.publish(event{Type: "decision", Record: rec})
	if s.game != nil {
		s.game.Decisions++
	}
	for _, m := range rec.Moves {
		s.record(m, rec.Request.Player, time.Now().UTC())
	}
	return rec, nil
}

func (s *Session) events(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch := make(chan []byte, 256)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	first, _ := json.Marshal(event{Type: "sync", Scramble: s.scramble, History: s.history, Log: s.log})
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()
	fmt.Fprintf(w, "data: %s\n\n", first)
	w.(http.Flusher).Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case data := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()
		}
	}
}

// post wraps a JSON command handler: decode the body, run it, encode the result.
func post[T any](fn func(*http.Request, T) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in T
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out, err := fn(r, in)
		if err != nil {
			code := http.StatusBadGateway
			if errors.Is(err, errSolved) || errors.Is(err, errLimit) {
				code = http.StatusConflict
			}
			log.Printf("%s: %v", r.URL.Path, err)
			http.Error(w, err.Error(), code)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}

func (h *Hub) routes(mux *http.ServeMux) {
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	session := func(w http.ResponseWriter, r *http.Request) *Session {
		s, err := h.get(r.URL.Query().Get("game"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
		}
		return s
	}
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		if s := session(w, r); s != nil {
			s.events(w, r)
		}
	})
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		if s := session(w, r); s != nil {
			s.mu.Lock()
			defer s.mu.Unlock()
			writeJSON(w, event{Type: "sync", Scramble: s.scramble, History: s.history, View: s.view})
		}
	})
	mux.HandleFunc("/api/players", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, players()) })
	mux.HandleFunc("/api/games", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, h.games()) })
	mux.HandleFunc("/api/leaderboard", func(w http.ResponseWriter, r *http.Request) {
		rows, err := leaderboard()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, rows)
	})
	mux.HandleFunc("/api/play", post(func(_ *http.Request, in PlayRequest) (any, error) { return h.Play(in) }))
	mux.HandleFunc("/api/reset", post(func(r *http.Request, _ struct{}) (any, error) {
		s, err := h.get(r.URL.Query().Get("game"))
		if err != nil {
			return nil, err
		}
		s.Reset()
		return struct{}{}, nil
	}))
	mux.HandleFunc("/api/move", post(func(r *http.Request, in struct {
		Move  string // one move, or
		Moves []string
		By    string
	}) (any, error) {
		s, err := h.get(r.URL.Query().Get("game"))
		if err != nil {
			return nil, err
		}
		if in.Move != "" {
			in.Moves = append(in.Moves, in.Move)
		}
		return struct{}{}, s.Move(in.Moves, in.By)
	}))
	mux.HandleFunc("/api/scramble", post(func(r *http.Request, in struct {
		Moves []string
		Len   int
	}) (any, error) {
		s, err := h.get(r.URL.Query().Get("game"))
		if err != nil {
			return nil, err
		}
		if len(in.Moves) == 0 {
			in.Moves = randomScramble(max(1, min(in.Len, 60)))
		}
		return in.Moves, s.Scramble(in.Moves)
	}))
	mux.HandleFunc("/api/step", post(func(r *http.Request, req StepRequest) (any, error) {
		s, err := h.get(r.URL.Query().Get("game"))
		if err != nil {
			return nil, err
		}
		return s.Step(r.Context(), req)
	}))
}

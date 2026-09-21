package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
)

// Session is the one cube the server owns: solved + Scramble + History.
// The page is a view over its event stream; page buttons and `run --ui`
// change it through the same endpoints.
type Session struct {
	mu       sync.Mutex
	jev      *Jev
	run      string
	scramble []string
	history  []string
	subs     map[chan []byte]struct{}
}

type event struct {
	Type     string      `json:"type"` // sync | move | scramble | decision
	Move     string      `json:"move,omitempty"`
	Moves    []string    `json:"moves,omitempty"`
	Scramble []string    `json:"scramble"`
	History  []string    `json:"history"`
	Record   *StepRecord `json:"record,omitempty"`
}

var errSolved = errors.New("cube is already solved")

func newSession(jev *Jev) *Session {
	return &Session{jev: jev, run: newRunID("ui"), subs: map[chan []byte]struct{}{}}
}

// publish sends an event to every connected page; callers hold s.mu.
func (s *Session) publish(e event) {
	e.Scramble, e.History = s.scramble, s.history
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
	s.scramble, s.history, s.run = nil, nil, newRunID("ui")
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
	s.scramble = append(append(s.scramble, s.history...), moves...)
	s.history, s.run = nil, newRunID("ui")
	s.publish(event{Type: "scramble", Moves: moves})
	return nil
}

func (s *Session) Move(m string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidMove(m) {
		return fmt.Errorf("unknown move %q", m)
	}
	s.history = append(s.history, m)
	s.publish(event{Type: "move", Move: m})
	return nil
}

// Step asks Jev for one move on the current cube and applies it.
func (s *Session) Step(req StepRequest) (*StepRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req.Run, req.Scramble, req.History = s.run, s.scramble, s.history
	cube := NewCube()
	cube.ApplyAll(s.scramble)
	cube.ApplyAll(s.history)
	if cube.Solved() {
		return nil, errSolved
	}
	rec, err := s.jev.Decide(req)
	if err != nil {
		return nil, err
	}
	s.history = append(s.history, rec.Move)
	s.publish(event{Type: "decision", Record: rec})
	s.publish(event{Type: "move", Move: rec.Move})
	return rec, nil
}

func (s *Session) events(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch := make(chan []byte, 256)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	first, _ := json.Marshal(event{Type: "sync", Scramble: s.scramble, History: s.history})
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
func post[T any](fn func(T) (any, error)) http.HandlerFunc {
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
		out, err := fn(in)
		if err != nil {
			code := http.StatusBadGateway
			if errors.Is(err, errSolved) {
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

func (s *Session) routes(mux *http.ServeMux) {
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})
	mux.HandleFunc("/api/events", s.events)
	mux.HandleFunc("/api/reset", post(func(struct{}) (any, error) { s.Reset(); return struct{}{}, nil }))
	mux.HandleFunc("/api/move", post(func(in struct{ Move string }) (any, error) { return struct{}{}, s.Move(in.Move) }))
	mux.HandleFunc("/api/scramble", post(func(in struct {
		Moves []string
		Len   int
	}) (any, error) {
		if len(in.Moves) == 0 {
			in.Moves = randomScramble(max(1, min(in.Len, 60)))
		}
		return in.Moves, s.Scramble(in.Moves)
	}))
	mux.HandleFunc("/api/step", post(func(req StepRequest) (any, error) { return s.Step(req) }))
}

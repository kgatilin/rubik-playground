package main

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	gamesFile    = "games"
	gameScramble = 20 // face turns of a leaderboard scramble
)

// Game is one registered attempt: a player, a random scramble, and the moves
// made until the cube was solved, the turn limit was reached or the game was
// dropped. Closed games are the lines of runs/games.jsonl; the leaderboard is
// computed from that file.
type Game struct {
	ID          string    `json:"id"`      // the run id, so decisions are in runs/<id>.jsonl
	Session     string    `json:"session"` // short number of the game on its serve: what state and move are pointed at
	Player      string    `json:"player"`
	Observation string    `json:"observation"`
	Goal        string    `json:"goal,omitempty"` // "" = fewest face turns, goalTime = fastest solve
	Lookahead   bool      `json:"lookahead,omitempty"`
	Scramble    []string  `json:"scramble"`
	Moves       []string  `json:"moves"`
	Decisions   int       `json:"decisions,omitempty"` // built-in players only
	Started     time.Time `json:"started"`
	Ended       time.Time `json:"ended,omitzero"`
	Outcome     string    `json:"outcome,omitempty"` // solved | dnf | abandoned
}

// The goal of a game: what the leaderboard ranks solved games by.
const (
	goalTurns = "turns" // fewest face turns (default)
	goalTime  = "time"  // shortest time from registration to the last move; face turns do not count
)

func validGoal(goal string) error {
	if goal != "" && goal != goalTurns && goal != goalTime {
		return fmt.Errorf("unknown goal %q: use %s or %s", goal, goalTurns, goalTime)
	}
	return nil
}

// category separates results that are not comparable.
func (g *Game) category() string {
	c := cmp.Or(g.Observation, obsText)
	if g.Lookahead {
		c += "+lookahead"
	}
	if g.Goal == goalTime {
		c += "+time"
	}
	return c
}

// timed reports whether the game is ranked by time.
func (g *Game) timed() bool { return g.Goal == goalTime }

type PlayRequest struct {
	Player      string `json:"player"`
	Observation string `json:"observation"`
	Goal        string `json:"goal"`
	Lookahead   bool   `json:"lookahead"`
}

// Play registers a player on this session's cube: the open game is dropped, the
// cube gets a fresh random scramble and a new game starts.
func (s *Session) Play(in PlayRequest) (*Game, error) {
	in.Player = strings.TrimSpace(in.Player)
	in.Observation = cmp.Or(in.Observation, obsText)
	if in.Player == "" {
		return nil, errors.New("a game needs a player name")
	}
	if err := cmp.Or(validObservation(in.Observation), validGoal(in.Goal)); err != nil {
		return nil, err
	}
	if in.Goal == goalTurns {
		in.Goal = ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeGame("abandoned")
	s.scramble, s.history, s.run, s.gemini = nil, nil, newRunID("game"), map[string]*Gemini{}
	s.publish(event{Type: "sync"})
	s.scramble = randomScramble(gameScramble)
	s.game = &Game{ID: s.run, Session: s.id, Player: in.Player, Observation: in.Observation, Goal: in.Goal,
		Lookahead: in.Lookahead && in.Player == "jev", // only Jev is shown lookahead
		Scramble:  slices.Clone(s.scramble), Started: time.Now().UTC()}
	s.publish(event{Type: "scramble", Moves: s.scramble})
	s.publish(event{Type: "game", Game: s.game})
	return s.game, nil
}

// closeGame writes the open game to the games file; callers hold s.mu.
func (s *Session) closeGame(outcome string) {
	g := s.game
	if g == nil {
		return
	}
	s.game = nil
	g.Moves, g.Ended, g.Outcome = slices.Clone(s.history), time.Now().UTC(), outcome
	if err := appendJSONL(gamesFile, g); err != nil {
		log.Printf("game %s not recorded: %v", g.ID, err)
	}
	s.publish(event{Type: "game", Game: g})
}

// record appends one move to the cube and ends the game when it is over; callers hold s.mu.
func (s *Session) record(m, by string, at time.Time) {
	s.history = append(s.history, m)
	s.publish(event{Type: "move", Move: m, By: by, Time: at})
	if s.game == nil {
		return
	}
	cube := NewCube()
	cube.ApplyAll(s.scramble)
	cube.ApplyAll(s.history)
	if cube.Solved() {
		s.closeGame("solved")
	} else if len(s.history) >= defaultLimit {
		s.closeGame("dnf")
	}
}

// BoardRow is one leaderboard line: a player in one category. The best game is
// the one with the fewest face turns, or in a timed category the fastest one.
type BoardRow struct {
	Player    string  `json:"player"`
	Category  string  `json:"category"`
	Timed     bool    `json:"timed,omitempty"` // the category ranks by time
	Attempts  int     `json:"attempts"`
	Solved    int     `json:"solved"`
	DNF       int     `json:"dnf"`
	Abandoned int     `json:"abandoned"`
	Best      int     `json:"best,omitempty"`      // face turns of the best solved game
	Mean      float64 `json:"mean,omitempty"`      // face turns over solved games
	BestSecs  float64 `json:"best_secs,omitempty"` // duration of the best game, registration to last move
	MeanSecs  float64 `json:"mean_secs,omitempty"` // duration over solved games
}

// better reports whether a solved game of n turns in secs beats the row's best.
func (r *BoardRow) better(n int, secs float64) bool {
	if r.Best == 0 {
		return true
	}
	if r.Timed {
		return secs < r.BestSecs || secs == r.BestSecs && n < r.Best
	}
	return n < r.Best || n == r.Best && secs < r.BestSecs
}

// leaderboard aggregates the games file: solvers first, by category, then by the
// category's best result, then by its mean.
func leaderboard() ([]BoardRow, error) {
	f, err := os.Open(filepath.Join(runsDir, gamesFile+".jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return []BoardRow{}, nil
	} else if err != nil {
		return nil, err
	}
	defer f.Close()
	rows := map[[2]string]*BoardRow{}
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var g Game
		if err := json.Unmarshal(sc.Bytes(), &g); err != nil {
			return nil, err
		}
		k := [2]string{g.Player, g.category()}
		r := rows[k]
		if r == nil {
			r = &BoardRow{Player: k[0], Category: k[1], Timed: g.timed()}
			rows[k] = r
		}
		r.Attempts++
		switch g.Outcome {
		case "solved":
			n, secs := len(g.Moves), g.Ended.Sub(g.Started).Seconds()
			r.Mean = (r.Mean*float64(r.Solved) + float64(n)) / float64(r.Solved+1)
			r.MeanSecs = (r.MeanSecs*float64(r.Solved) + secs) / float64(r.Solved+1)
			r.Solved++
			if r.better(n, secs) {
				r.Best, r.BestSecs = n, secs
			}
		case "dnf":
			r.DNF++
		default:
			r.Abandoned++
		}
	}
	out := make([]BoardRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	slices.SortFunc(out, func(a, b BoardRow) int {
		byBest, byMean := cmp.Compare(a.Best, b.Best), cmp.Compare(a.Mean, b.Mean)
		if a.Timed {
			byBest, byMean = cmp.Compare(a.BestSecs, b.BestSecs), cmp.Compare(a.MeanSecs, b.MeanSecs)
		}
		return cmp.Or(cmp.Compare(min(b.Solved, 1), min(a.Solved, 1)), cmp.Compare(a.Category, b.Category),
			byBest, byMean, cmp.Compare(b.Attempts, a.Attempts), cmp.Compare(a.Player, b.Player))
	})
	return out, sc.Err()
}

func printBoard(rows []BoardRow) {
	fmt.Printf("%-40s %-16s %8s %6s %4s %9s %5s %9s %6s %9s\n", "player", "category", "attempts", "solved", "dnf", "abandoned", "best", "best time", "mean", "mean time")
	for _, r := range rows {
		best, mean, bestTime, meanTime := "-", "-", "-", "-"
		if r.Solved > 0 {
			best, mean = fmt.Sprint(r.Best), fmt.Sprintf("%.1f", r.Mean)
			bestTime = (time.Duration(r.BestSecs) * time.Second).String()
			meanTime = (time.Duration(r.MeanSecs) * time.Second).String()
		}
		fmt.Printf("%-40s %-16s %8d %6d %4d %9d %5s %9s %6s %9s\n", r.Player, r.Category, r.Attempts, r.Solved, r.DNF, r.Abandoned, best, bestTime, mean, meanTime)
	}
}

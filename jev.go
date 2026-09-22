package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"google.golang.org/genai"
)

const (
	jevURL   = "https://api.typesafe.ai/v1/systemone"
	jevModel = "jev-latest"
	runsDir  = "runs"
)

const defaultInstructions = "You are solving a 3x3 Rubik's cube. Pick the single next move that brings the cube closer to the solved state, where every face shows one colour. Do not undo the previous move and do not repeat a cycle from the move history."

var allMoves = strings.Fields("U U' U2 D D' D2 L L' L2 R R' R2 F F' F2 B B' B2")

// StepRequest is one decision: the cube is NewCube + Scramble + History.
// Both the web page and the CLI go through Decide with this.
type StepRequest struct {
	Player       string   `json:"player"` // "jev" (default) or "gemini:<model>[@<thinking level>]"
	Limit        int      `json:"limit"`  // face-turn limit of the game
	Run          string   `json:"run"`
	Scramble     []string `json:"scramble"`
	History      []string `json:"history"`
	Options      []string `json:"options"`
	Instructions string   `json:"instructions"`
	Sample       bool     `json:"sample"`                // draw the move from the probabilities instead of taking the top one
	NoUndo       bool     `json:"no_undo"`               // do not offer the inverse of the previous move
	Shuffle      bool     `json:"shuffle"`               // randomise the order in which moves are listed
	Lookahead    bool     `json:"lookahead"`             // describe each move by the sticker count it leads to
	Observation  string   `json:"observation,omitempty"` // how the faces are shown: "text" (default), "pieces" or "image"
	Goal         string   `json:"goal,omitempty"`        // "" fewest face turns, "time" fastest solve
}

// StepRecord is the outcome of one decision and one line of runs/<run>.jsonl.
type StepRecord struct {
	Time          time.Time          `json:"time"`
	Step          int                `json:"step"`
	Request       StepRequest        `json:"request"`
	Offered       []string           `json:"offered"` // in the order sent to Jev
	State         string             `json:"state"`
	Image         []byte             `json:"image,omitempty"` // PNG of the faces when the observation is an image
	MatchedBefore int                `json:"matched_before"`
	Choice        string             `json:"choice,omitempty"` // Jev's top option
	Moves         []string           `json:"moves"`            // moves taken by this decision (Jev: one; differs from Choice when sampling)
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Thoughts      string             `json:"thoughts,omitempty"` // thought summary, for players that return one
	Response      *ModelResponse     `json:"response,omitempty"` // the model's turn as returned, for players that call tools
	MatchedAfter  int                `json:"matched_after"`
	Solved        bool               `json:"solved"`
	Model         string             `json:"model"`
	InputTokens   int                `json:"input_tokens"`
	OutputTokens  int                `json:"output_tokens,omitempty"`
	Millis        int64              `json:"ms"`
}

// ModelResponse is the raw turn of a tool-calling player: text and function calls with their
// arguments, without thought text (StepRecord.Thoughts) and thought signatures.
type ModelResponse struct {
	Parts        []*genai.Part `json:"parts"`
	FinishReason string        `json:"finish_reason"`
}

type Jev struct {
	Token  string
	Client *http.Client
}

// orderedCriteria marshals as a JSON object that keeps the option order,
// because the order in which options are listed is part of the prompt.
type orderedCriteria [][2]string

func (o orderedCriteria) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(m[0])
		v, _ := json.Marshal(m[1])
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// Decider is a built-in player: one decision on the cube described by the request.
type Decider interface {
	Decide(context.Context, StepRequest) (*StepRecord, error)
}

// prepareStep rebuilds the cube and the list of moves offered for this decision.
func prepareStep(req StepRequest) (*Cube, []string, error) {
	cube := NewCube()
	if err := cube.ApplyAll(req.Scramble); err != nil {
		return nil, nil, err
	}
	if err := cube.ApplyAll(req.History); err != nil {
		return nil, nil, err
	}

	var offered []string
	for _, m := range req.Options {
		if !ValidMove(m) {
			return nil, nil, fmt.Errorf("unknown move %q", m)
		}
		if !slices.Contains(offered, m) {
			offered = append(offered, m)
		}
	}
	if n := len(req.History); req.NoUndo && n > 0 && len(offered) > 1 {
		undo := Inverse(req.History[n-1])
		offered = slices.DeleteFunc(offered, func(m string) bool { return m == undo })
	}
	if len(offered) == 0 {
		return nil, nil, fmt.Errorf("no moves offered")
	}
	if err := validObservation(req.Observation); err != nil {
		return nil, nil, err
	}
	if req.Shuffle {
		rand.Shuffle(len(offered), func(a, b int) { offered[a], offered[b] = offered[b], offered[a] })
	}
	return cube, offered, nil
}

// finishStep applies the decided moves (cut at the turn limit), fills the outcome
// and appends the record to the run log.
func finishStep(rec *StepRecord, cube *Cube) error {
	if left := rec.Request.Limit - len(rec.Request.History); rec.Request.Limit > 0 && len(rec.Moves) > left {
		rec.Moves = rec.Moves[:max(left, 0)]
	}
	for _, m := range rec.Moves {
		if !slices.Contains(rec.Offered, m) {
			return fmt.Errorf("%s chose a move that was not offered: %q", rec.Request.Player, m)
		}
		cube.Apply(m)
	}
	rec.MatchedAfter, rec.Solved = cube.Matched(), cube.Solved()
	return appendJSONL(rec.Request.Run, rec)
}

// Decide asks Jev for the next move, appends the record to the run log and returns it.
func (j *Jev) Decide(ctx context.Context, req StepRequest) (*StepRecord, error) {
	if req.Observation == obsImage {
		return nil, fmt.Errorf("jev takes a text state only: the image observation is for players that accept pictures")
	}
	cube, offered, err := prepareStep(req)
	if err != nil {
		return nil, err
	}
	rec := &StepRecord{Time: time.Now().UTC(), Step: len(req.History) + 1, Request: req, Offered: offered,
		State: cube.StateText(req.History, req.Limit, req.Observation), MatchedBefore: cube.Matched()}

	criteria := make(orderedCriteria, len(offered))
	for i, m := range offered {
		text := DescribeMove(m)
		if req.Lookahead {
			next := NewCube()
			next.ApplyAll(req.Scramble)
			next.ApplyAll(req.History)
			next.Apply(m)
			text += fmt.Sprintf(". Result: %d/54 stickers match their face centre (%+d)", next.Matched(), next.Matched()-rec.MatchedBefore)
		}
		criteria[i] = [2]string{m, text}
	}
	body, _ := json.Marshal(map[string]any{
		"model": jevModel,
		"state": rec.State,
		"questions": map[string]any{"next_move": map[string]any{
			"type": "choice", "instructions": req.Instructions, "criteria": criteria}},
	})
	hreq, _ := http.NewRequestWithContext(ctx, http.MethodPost, jevURL, bytes.NewReader(body))
	hreq.Header.Set("Authorization", "Bearer "+j.Token)
	hreq.Header.Set("Content-Type", "application/json")
	t0 := time.Now()
	resp, err := j.Client.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	rec.Millis = time.Since(t0).Milliseconds()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jev %d: %s", resp.StatusCode, raw)
	}

	var out struct {
		Model   string `json:"model"`
		Answers map[string]struct {
			Choice        string             `json:"choice"`
			Confidence    float64            `json:"confidence"`
			Probabilities map[string]float64 `json:"probabilities"`
		} `json:"answers"`
		Usage struct {
			InputTokens int `json:"input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("jev response: %w: %s", err, raw)
	}
	a := out.Answers["next_move"]
	rec.Choice, rec.Confidence, rec.Probabilities = a.Choice, a.Confidence, a.Probabilities
	move := a.Choice
	rec.Model, rec.InputTokens = out.Model, out.Usage.InputTokens

	if req.Sample {
		total := 0.0
		for _, m := range offered {
			total += a.Probabilities[m]
		}
		r := rand.Float64() * total
		for _, m := range offered {
			if r -= a.Probabilities[m]; r <= 0 {
				move = m
				break
			}
		}
	}
	rec.Moves = []string{move}
	return rec, finishStep(rec, cube)
}

// appendJSONL adds one line to runs/<name>.jsonl.
func appendJSONL(name string, v any) error {
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(runsDir, filepath.Base(name)+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, _ := json.Marshal(v)
	_, err = f.Write(append(line, '\n'))
	return err
}

func newRunID(prefix string) string {
	return prefix + "-" + time.Now().Format("20060102-150405")
}

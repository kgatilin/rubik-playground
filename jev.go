package main

import (
	"bytes"
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
	Run          string   `json:"run"`
	Scramble     []string `json:"scramble"`
	History      []string `json:"history"`
	Options      []string `json:"options"`
	Instructions string   `json:"instructions"`
	Sample       bool     `json:"sample"`       // draw the move from the probabilities instead of taking the top one
	NoUndo       bool     `json:"no_undo"`      // do not offer the inverse of the previous move
	Shuffle      bool     `json:"shuffle"`      // randomise the order in which moves are listed
	HideHistory  bool     `json:"hide_history"` // leave the move history out of the state text
	Lookahead    bool     `json:"lookahead"`    // describe each move by the sticker count it leads to
}

// StepRecord is the outcome of one decision and one line of runs/<run>.jsonl.
type StepRecord struct {
	Time          time.Time          `json:"time"`
	Step          int                `json:"step"`
	Request       StepRequest        `json:"request"`
	Offered       []string           `json:"offered"` // in the order sent to Jev
	State         string             `json:"state"`
	MatchedBefore int                `json:"matched_before"`
	Choice        string             `json:"choice"` // Jev's top option
	Move          string             `json:"move"`   // move actually taken (differs from Choice when sampling)
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
	MatchedAfter  int                `json:"matched_after"`
	Solved        bool               `json:"solved"`
	Model         string             `json:"model"`
	InputTokens   int                `json:"input_tokens"`
	Millis        int64              `json:"ms"`
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

// Decide asks Jev for the next move, appends the record to the run log and returns it.
func (j *Jev) Decide(req StepRequest) (*StepRecord, error) {
	cube := NewCube()
	if err := cube.ApplyAll(req.Scramble); err != nil {
		return nil, err
	}
	if err := cube.ApplyAll(req.History); err != nil {
		return nil, err
	}

	var offered []string
	for _, m := range req.Options {
		if !ValidMove(m) {
			return nil, fmt.Errorf("unknown move %q", m)
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
		return nil, fmt.Errorf("no moves offered")
	}
	if req.Shuffle {
		rand.Shuffle(len(offered), func(a, b int) { offered[a], offered[b] = offered[b], offered[a] })
	}

	shown := req.History
	if req.HideHistory {
		shown = nil
	}
	rec := &StepRecord{Time: time.Now().UTC(), Step: len(req.History) + 1, Request: req, Offered: offered,
		State: cube.StateText(shown), MatchedBefore: cube.Matched()}

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
	hreq, _ := http.NewRequest(http.MethodPost, jevURL, bytes.NewReader(body))
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
	rec.Choice, rec.Move, rec.Confidence, rec.Probabilities = a.Choice, a.Choice, a.Confidence, a.Probabilities
	rec.Model, rec.InputTokens = out.Model, out.Usage.InputTokens

	if req.Sample {
		total := 0.0
		for _, m := range offered {
			total += a.Probabilities[m]
		}
		r := rand.Float64() * total
		for _, m := range offered {
			if r -= a.Probabilities[m]; r <= 0 {
				rec.Move = m
				break
			}
		}
	}
	if !slices.Contains(offered, rec.Move) {
		return nil, fmt.Errorf("jev chose a move that was not offered: %q", rec.Move)
	}

	cube.Apply(rec.Move)
	rec.MatchedAfter, rec.Solved = cube.Matched(), cube.Solved()
	return rec, appendRunLog(rec)
}

func appendRunLog(rec *StepRecord) error {
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(runsDir, filepath.Base(rec.Request.Run)+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, _ := json.Marshal(rec)
	_, err = f.Write(append(line, '\n'))
	return err
}

func newRunID(prefix string) string {
	return prefix + "-" + time.Now().Format("20060102-150405")
}

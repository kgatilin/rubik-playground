package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/genai"
)

const geminiRules = `You are playing a Rubik's cube game. Goal: solve the 3x3 cube in as few face turns as possible; the game ends unsolved when the turn limit is reached.

You act only by calling the tool move(actions). Each action is one face turn: U D L R F B turn that face 90° clockwise as seen from outside the face, X' is counter-clockwise, X2 is 180°. There are no whole-cube rotations: centres never move, U is always the white centre and F the green one. Every tool response is the new observation of the cube. You may send several actions in one call; each costs one face turn. Think about where pieces go before moving, and keep your plan in mind between calls.`

// Gemini plays one game as one tool-calling conversation. The model's turns are
// appended to the conversation untouched, so thought signatures are sent back
// and the model keeps its plan between decisions.
type Gemini struct {
	client   *genai.Client
	model    string
	level    genai.ThinkingLevel // empty: the model's default
	contents []*genai.Content
	pending  *genai.FunctionCall // answered with the observation at the start of the next decision
}

// newGemini takes "<model>" or "<model>@<thinking level>" (minimal, low, medium, high).
func newGemini(spec string) (*Gemini, error) {
	model, lvl, _ := strings.Cut(spec, "@")
	level := genai.ThinkingLevel(strings.ToUpper(lvl))
	switch level {
	case "", genai.ThinkingLevelMinimal, genai.ThinkingLevelLow, genai.ThinkingLevelMedium, genai.ThinkingLevelHigh:
	default:
		return nil, fmt.Errorf("unknown thinking level %q: use minimal, low, medium or high", lvl)
	}
	project, location := os.Getenv("GOOGLE_CLOUD_PROJECT"), os.Getenv("GOOGLE_CLOUD_LOCATION")
	if project == "" || location == "" {
		return nil, fmt.Errorf("GOOGLE_CLOUD_PROJECT and GOOGLE_CLOUD_LOCATION are not set (env or .env)")
	}
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		Backend: genai.BackendVertexAI, Project: project, Location: location})
	if err != nil {
		return nil, err
	}
	return &Gemini{client: client, model: model, level: level}, nil
}

func (g *Gemini) config(options []string) *genai.GenerateContentConfig {
	return &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(geminiRules, genai.RoleUser),
		ThinkingConfig:    &genai.ThinkingConfig{IncludeThoughts: true, ThinkingLevel: g.level},
		ToolConfig: &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{
			Mode: genai.FunctionCallingConfigModeAny}},
		Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{
			Name:        "move",
			Description: "Turn faces of the cube, in order. Returns the new observation.",
			Parameters: &genai.Schema{Type: genai.TypeObject, Required: []string{"actions"},
				Properties: map[string]*genai.Schema{"actions": {
					Type: genai.TypeArray, MinItems: genai.Ptr[int64](1),
					Items: &genai.Schema{Type: genai.TypeString, Enum: options}}}},
		}}}},
	}
}

func (g *Gemini) Decide(req StepRequest) (*StepRecord, error) {
	cube, offered, err := prepareStep(req)
	if err != nil {
		return nil, err
	}
	rec := &StepRecord{Time: time.Now().UTC(), Step: len(req.History) + 1, Request: req, Offered: offered,
		State: cube.StateText(req.History, req.Limit), MatchedBefore: cube.Matched()}

	if g.pending == nil {
		g.contents = append(g.contents, genai.NewContentFromText(rec.State, genai.RoleUser))
	} else {
		part := genai.NewPartFromFunctionResponse(g.pending.Name, map[string]any{"observation": rec.State})
		part.FunctionResponse.ID = g.pending.ID
		g.contents = append(g.contents, genai.NewContentFromParts([]*genai.Part{part}, genai.RoleUser))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	t0 := time.Now()
	resp, err := g.client.Models.GenerateContent(ctx, g.model, g.contents, g.config(offered))
	rec.Millis = time.Since(t0).Milliseconds()
	if err != nil {
		g.contents = g.contents[:len(g.contents)-1] // the decision can be retried
		return nil, fmt.Errorf("gemini: %w", err)
	}
	calls := resp.FunctionCalls()
	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil || len(calls) == 0 {
		g.contents = g.contents[:len(g.contents)-1]
		reason := "no candidates"
		if len(resp.Candidates) > 0 {
			reason = fmt.Sprintf("%s %s", resp.Candidates[0].FinishReason, resp.Candidates[0].FinishMessage)
		}
		return nil, fmt.Errorf("gemini returned no move call (%s): %s", reason, resp.Text())
	}
	g.contents = append(g.contents, resp.Candidates[0].Content)
	g.pending = calls[0]

	var thoughts []string
	for _, p := range resp.Candidates[0].Content.Parts {
		if p.Thought && p.Text != "" {
			thoughts = append(thoughts, p.Text)
		}
	}
	rec.Thoughts = strings.Join(thoughts, "\n")
	if u := resp.UsageMetadata; u != nil {
		rec.InputTokens, rec.OutputTokens = int(u.PromptTokenCount), int(u.CandidatesTokenCount+u.ThoughtsTokenCount)
	}
	rec.Model = g.model

	actions, _ := calls[0].Args["actions"].([]any)
	for _, a := range actions {
		rec.Moves = append(rec.Moves, fmt.Sprint(a))
	}
	return rec, finishStep(rec, cube)
}

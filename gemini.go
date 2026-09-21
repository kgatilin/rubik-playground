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
	pending  []*genai.FunctionCall // answered at the start of the next decision, the last one with the observation
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

func (g *Gemini) Decide(ctx context.Context, req StepRequest) (*StepRecord, error) {
	cube, offered, err := prepareStep(req)
	if err != nil {
		return nil, err
	}
	rec := &StepRecord{Time: time.Now().UTC(), Step: len(req.History) + 1, Request: req, Offered: offered,
		State: cube.StateText(req.History, req.Limit, req.Observation), MatchedBefore: cube.Matched()}
	if req.Observation == obsImage {
		rec.Image = cube.StateImage()
	}

	parts := []*genai.Part{genai.NewPartFromText(rec.State)}
	if len(g.pending) > 0 {
		// every call of the previous turn gets a response; the observation goes into the last one
		parts = parts[:0]
		for i, call := range g.pending {
			part := genai.NewPartFromFunctionResponse(call.Name, map[string]any{"result": "applied"})
			if i == len(g.pending)-1 {
				var media []*genai.FunctionResponsePart
				if rec.Image != nil {
					media = append(media, genai.NewFunctionResponsePartFromBytes(rec.Image, "image/png"))
				}
				part = genai.NewPartFromFunctionResponseWithParts(call.Name, map[string]any{"observation": rec.State}, media)
			}
			part.FunctionResponse.ID = call.ID
			parts = append(parts, part)
		}
	} else if rec.Image != nil {
		parts = append(parts, genai.NewPartFromBytes(rec.Image, "image/png"))
	}
	g.contents = append(g.contents, genai.NewContentFromParts(parts, genai.RoleUser))

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
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
	g.pending = calls

	// the raw record: every part of the model's turn except thought text (in Thoughts) and thought signatures
	var thoughts []string
	var raw []*genai.Part
	for _, p := range resp.Candidates[0].Content.Parts {
		if p.Thought {
			if p.Text != "" {
				thoughts = append(thoughts, p.Text)
			}
			continue
		}
		q := *p
		q.ThoughtSignature = nil
		raw = append(raw, &q)
	}
	rec.Response = &ModelResponse{Parts: raw, FinishReason: string(resp.Candidates[0].FinishReason)}
	rec.Thoughts = strings.Join(thoughts, "\n")
	if u := resp.UsageMetadata; u != nil {
		rec.InputTokens, rec.OutputTokens = int(u.PromptTokenCount), int(u.CandidatesTokenCount+u.ThoughtsTokenCount)
	}
	rec.Model = g.model

	for _, call := range calls { // several move calls in one turn are applied in order
		actions, _ := call.Args["actions"].([]any)
		for _, a := range actions {
			rec.Moves = append(rec.Moves, fmt.Sprint(a))
		}
	}
	return rec, finishStep(rec, cube)
}

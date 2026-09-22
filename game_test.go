package main

import (
	"slices"
	"strconv"
	"testing"
	"time"
)

func TestGameLifecycleAndLeaderboard(t *testing.T) {
	t.Chdir(t.TempDir())
	s := newSession(nil)

	if _, err := s.Play(PlayRequest{Player: "model-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Play(PlayRequest{Player: "model-a"}); err != nil { // drops the first game
		t.Fatal(err)
	}
	undo := slices.Clone(s.scramble)
	slices.Reverse(undo)
	for i, m := range undo {
		undo[i] = Inverse(m)
	}
	if err := s.Move(undo, "model-a"); err != nil {
		t.Fatal(err)
	}
	if s.game != nil {
		t.Fatal("game still open after the cube was solved")
	}
	if _, err := s.Step(t.Context(), StepRequest{Player: "jev"}); err == nil {
		t.Fatal("step on a solved cube should fail")
	}

	rows, err := leaderboard()
	if err != nil {
		t.Fatal(err)
	}
	want := BoardRow{Player: "model-a", Category: "text", Attempts: 2, Solved: 1, Abandoned: 1, Best: gameScramble, Mean: gameScramble}
	if len(rows) == 1 {
		if rows[0].BestSecs <= 0 || rows[0].MeanSecs != rows[0].BestSecs {
			t.Fatalf("times not recorded: %+v", rows[0])
		}
		rows[0].BestSecs, rows[0].MeanSecs = 0, 0
	}
	if len(rows) != 1 || rows[0] != want {
		t.Fatalf("leaderboard = %+v, want %+v", rows, want)
	}
}

func TestTimedCategoryRanksByTime(t *testing.T) {
	t.Chdir(t.TempDir())
	base := time.Now().UTC()
	for _, g := range []Game{
		{Player: "slow-short", Goal: goalTime, Moves: []string{"R"}, Started: base, Ended: base.Add(30 * time.Second), Outcome: "solved"},
		{Player: "fast-long", Goal: goalTime, Moves: []string{"R", "U", "F"}, Started: base, Ended: base.Add(10 * time.Second), Outcome: "solved"},
		{Player: "fast-long", Goal: goalTime, Moves: []string{"R", "U"}, Started: base, Ended: base.Add(50 * time.Second), Outcome: "solved"},
		{Player: "fast-long", Moves: []string{"R", "U"}, Started: base, Ended: base.Add(50 * time.Second), Outcome: "solved"},
	} {
		if err := appendJSONL(gamesFile, g); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := leaderboard()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Category != "text" || rows[1].Category != "text+time" || rows[1].Player != "fast-long" || rows[2].Player != "slow-short" {
		t.Fatalf("rows = %+v", rows)
	}
	if r := rows[1]; !r.Timed || r.BestSecs != 10 || r.Best != 3 || r.MeanSecs != 30 {
		t.Fatalf("timed row = %+v", r)
	}
}

func TestHubRunsGamesSideBySide(t *testing.T) {
	t.Chdir(t.TempDir())
	h := newHub(nil)
	for i, p := range []string{"model-a", "model-a"} { // one name can play several games at once
		if g, err := h.Play(PlayRequest{Player: p}); err != nil || g.Session != strconv.Itoa(i+1) {
			t.Fatal(g, err)
		}
	}
	a, _ := h.get("1")
	b, _ := h.get("2")
	if err := a.Move([]string{"R", "U"}, ""); err != nil {
		t.Fatal(err)
	}
	if len(a.history) != 2 || len(b.history) != 0 || a.game == nil || b.game == nil {
		t.Fatalf("games are not independent: a=%v b=%v", a.history, b.history)
	}
	if _, err := h.get("3"); err == nil {
		t.Fatal("unregistered player got a session")
	}
	if got := h.games(); len(got) != 3 || got[0].ID != "" || !got[1].Open || got[1].Turns != 2 {
		t.Fatalf("games = %+v", got)
	}
}

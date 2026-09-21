package main

import (
	"slices"
	"testing"
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
	for _, m := range undo {
		if err := s.Move(Inverse(m), "model-a"); err != nil {
			t.Fatal(err)
		}
	}
	if s.game != nil {
		t.Fatal("game still open after the cube was solved")
	}
	if _, err := s.Step(StepRequest{Player: "jev"}); err == nil {
		t.Fatal("step on a solved cube should fail")
	}

	rows, err := leaderboard()
	if err != nil {
		t.Fatal(err)
	}
	want := BoardRow{Player: "model-a", Category: "text", Attempts: 2, Solved: 1, Abandoned: 1, Best: gameScramble, Mean: gameScramble}
	if len(rows) != 1 || rows[0] != want {
		t.Fatalf("leaderboard = %+v, want %+v", rows, want)
	}
}

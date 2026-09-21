package main

import (
	"strings"
	"testing"
)

func rows(c *Cube, f string) string {
	g := c.Facelets()[f]
	return strings.Join([]string{strings.Join(g[0][:], ""), strings.Join(g[1][:], ""), strings.Join(g[2][:], "")}, "/")
}

func TestMovesFollowStandardNotation(t *testing.T) {
	for _, tc := range []struct{ move, face, want string }{
		{"R", "U", "WWG/WWG/WWG"}, {"R", "B", "WBB/WBB/WBB"},
		{"U", "F", "RRR/GGG/GGG"}, {"U", "L", "GGG/OOO/OOO"},
		{"F", "U", "WWW/WWW/OOO"}, {"F", "R", "WRR/WRR/WRR"},
	} {
		c := NewCube()
		c.Apply(tc.move)
		if got := rows(c, tc.face); got != tc.want {
			t.Errorf("%s: face %s = %s, want %s", tc.move, tc.face, got, tc.want)
		}
	}
}

func TestSequencesReturnToSolved(t *testing.T) {
	c := NewCube()
	for range 6 {
		c.ApplyAll(strings.Fields("R U R' U'"))
	}
	if !c.Solved() {
		t.Error("(R U R' U') x6 should be identity")
	}
	seq := strings.Fields("R U2 F' L D B2")
	c.ApplyAll(seq)
	for i := len(seq) - 1; i >= 0; i-- {
		c.Apply(Inverse(seq[i]))
	}
	if !c.Solved() {
		t.Error("sequence followed by its inverse should be identity")
	}
}

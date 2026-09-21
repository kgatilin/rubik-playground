package main

import (
	"encoding/json"
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

func TestProgress(t *testing.T) {
	c := NewCube()
	if got, want := c.Progress(), (Progress{4, 4, 4, 4, 4, 4, 4}); got != want {
		t.Errorf("solved cube: %+v", got)
	}
	c.Apply("U") // U pieces keep white up and leave their places; lower layers untouched
	if got, want := c.Progress(), (Progress{4, 4, 4, 4, 0, 0, 0}); got != want {
		t.Errorf("after U: %+v", got)
	}
	c = NewCube()
	c.Apply("F") // takes one edge and two corners out of D and U, two middle edges
	if got, want := c.Progress(), (Progress{3, 2, 2, 3, 3, 2, 2}); got != want {
		t.Errorf("after F: %+v", got)
	}
}

func TestPiecesText(t *testing.T) {
	c := NewCube()
	if got := c.piecesText(); strings.Count(got, "(solved)") != 20 {
		t.Fatalf("solved cube:\n%s", got)
	}
	c.Apply("R") // the DFR corner comes up to UFR: green on top, yellow in front
	got := c.piecesText()
	for _, want := range []string{"UFR: U=G F=Y R=R (belongs at DFR)\n", "UR: U=G R=R (belongs at FR)\n", "UFL: U=W F=G L=O (solved)\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestNetAndJSONViews(t *testing.T) {
	c := NewCube()
	c.Apply("F")
	net := "        W W W\n        W W W\n        O O O\n" +
		"O O Y   G G G   W R R   B B B\nO O Y   G G G   W R R   B B B\nO O Y   G G G   W R R   B B B\n" +
		"        R R R\n        Y Y Y\n        Y Y Y\n"
	if got := c.netText(); got != net {
		t.Errorf("net:\n%s", got)
	}
	var faces map[string][3][3]string
	if err := json.Unmarshal([]byte(c.jsonText()), &faces); err != nil {
		t.Fatal(err)
	}
	if faces["up"][2] != [3]string{"orange", "orange", "orange"} || faces["right"][0] != [3]string{"white", "red", "red"} || len(faces) != 6 {
		t.Errorf("json: %v", faces)
	}
}

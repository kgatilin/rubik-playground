package main

import (
	"fmt"
	"regexp"
	"strings"
)

// Axes: x → R, y → U, z → F. A cubie keeps its position p and orientation m
// (world = m · local); home is where it sits on a solved cube.
type vec [3]int
type mat [3]vec

type cubie struct {
	p, home vec
	m       mat
}

type Cube struct{ cubies []cubie }

var (
	normals  = [6]vec{{0, 1, 0}, {0, -1, 0}, {1, 0, 0}, {-1, 0, 0}, {0, 0, 1}, {0, 0, -1}}
	faces    = [6]string{"U", "D", "R", "L", "F", "B"} // face whose outward normal is normals[i]
	letters  = [6]string{"W", "Y", "R", "O", "G", "B"} // home colour of that face
	faceName = map[byte]string{'U': "Up", 'D': "Down", 'R': "Right", 'L': "Left", 'F': "Front", 'B': "Back"}
	faceAxis = map[byte][2]int{'U': {1, 1}, 'D': {1, -1}, 'R': {0, 1}, 'L': {0, -1}, 'F': {2, 1}, 'B': {2, -1}}
	moveRE   = regexp.MustCompile(`^[UDLRFB]['2]?$`)
)

func dot(a, b vec) int { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }

func (m mat) apply(v vec) vec { return vec{dot(m[0], v), dot(m[1], v), dot(m[2], v)} }

func (m mat) mul(b mat) mat {
	var out mat
	for i := range 3 {
		for j := range 3 {
			out[i][j] = m[i][0]*b[0][j] + m[i][1]*b[1][j] + m[i][2]*b[2][j]
		}
	}
	return out
}

// quarterTurns returns the rotation about axis by k·90°.
func quarterTurns(axis, k int) mat {
	k = (k%4 + 4) % 4
	c, s := [4]int{1, 0, -1, 0}[k], [4]int{0, 1, 0, -1}[k]
	switch axis {
	case 0:
		return mat{{1, 0, 0}, {0, c, -s}, {0, s, c}}
	case 1:
		return mat{{c, 0, s}, {0, 1, 0}, {-s, 0, c}}
	}
	return mat{{c, -s, 0}, {s, c, 0}, {0, 0, 1}}
}

func NewCube() *Cube {
	c := &Cube{}
	id := mat{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}
	for x := -1; x <= 1; x++ {
		for y := -1; y <= 1; y++ {
			for z := -1; z <= 1; z++ {
				if x != 0 || y != 0 || z != 0 {
					c.cubies = append(c.cubies, cubie{p: vec{x, y, z}, home: vec{x, y, z}, m: id})
				}
			}
		}
	}
	return c
}

func ValidMove(s string) bool { return moveRE.MatchString(s) }

func Inverse(s string) string {
	switch {
	case strings.HasSuffix(s, "2"):
		return s
	case strings.HasSuffix(s, "'"):
		return s[:1]
	}
	return s + "'"
}

// Apply turns one face; "X" is clockwise seen from outside that face.
func (c *Cube) Apply(move string) error {
	if !ValidMove(move) {
		return fmt.Errorf("unknown move %q", move)
	}
	al := faceAxis[move[0]]
	q := 1
	if strings.HasSuffix(move, "'") {
		q = -1
	} else if strings.HasSuffix(move, "2") {
		q = 2
	}
	r := quarterTurns(al[0], -al[1]*q)
	for i := range c.cubies {
		if cb := &c.cubies[i]; cb.p[al[0]] == al[1] {
			cb.p, cb.m = r.apply(cb.p), r.mul(cb.m)
		}
	}
	return nil
}

func (c *Cube) ApplyAll(moves []string) error {
	for _, m := range moves {
		if err := c.Apply(m); err != nil {
			return err
		}
	}
	return nil
}

// Facelets returns each face as 3 rows read from outside the cube
// (U with B on top, D with F on top, side faces with U on top).
func (c *Cube) Facelets() map[string][3][3]string {
	g := map[string][3][3]string{}
	for _, cb := range c.cubies {
		for i, n := range normals {
			if dot(cb.home, n) != 1 {
				continue
			}
			w, x, y, z := cb.m.apply(n), cb.p[0], cb.p[1], cb.p[2]
			var f string
			for k, nk := range normals {
				if dot(nk, w) == 1 {
					f = faces[k]
				}
			}
			var row, col int
			switch f {
			case "U":
				row, col = z+1, x+1
			case "D":
				row, col = 1-z, x+1
			case "F":
				row, col = 1-y, x+1
			case "B":
				row, col = 1-y, 1-x
			case "R":
				row, col = 1-y, 1-z
			default:
				row, col = 1-y, z+1
			}
			grid := g[f]
			grid[row][col] = letters[i]
			g[f] = grid
		}
	}
	return g
}

// Matched counts stickers that have the colour of their face centre.
func (c *Cube) Matched() int {
	n := 0
	for _, grid := range c.Facelets() {
		for _, row := range grid {
			for _, s := range row {
				if s == grid[1][1] {
					n++
				}
			}
		}
	}
	return n
}

func (c *Cube) Solved() bool { return c.Matched() == 54 }

// Progress counts pieces per layer-by-layer stage (D first, U last). A piece is
// solved when it is in its home place with its home orientation; centres never move.
type Progress struct {
	DEdges, DCorners, MiddleEdges   int // solved
	UEdgesWhiteUp, UEdgesSolved     int
	UCornersInPlace, UCornersSolved int
}

func (c *Cube) Progress() Progress {
	var p Progress
	id := mat{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}
	up := vec{0, 1, 0}
	for _, cb := range c.cubies {
		h := cb.home
		nonzero := h[0]*h[0] + h[1]*h[1] + h[2]*h[2] // 2 = edge, 3 = corner
		if nonzero < 2 {
			continue
		}
		edge, inPlace := nonzero == 2, cb.p == h
		solved := inPlace && cb.m == id
		switch {
		case h[1] == -1 && edge && solved:
			p.DEdges++
		case h[1] == -1 && !edge && solved:
			p.DCorners++
		case h[1] == 0 && solved:
			p.MiddleEdges++
		case h[1] == 1 && edge:
			if cb.m.apply(up) == up {
				p.UEdgesWhiteUp++
			}
			if solved {
				p.UEdgesSolved++
			}
		case h[1] == 1 && !edge:
			if inPlace {
				p.UCornersInPlace++
			}
			if solved {
				p.UCornersSolved++
			}
		}
	}
	return p
}

// Observation modes: the faces as text rows, as a list of pieces by slot, or as
// the picture of StateImage.
const (
	obsText   = "text"
	obsPieces = "pieces"
	obsImage  = "image"
)

func validObservation(o string) error {
	switch o {
	case "", obsText, obsPieces, obsImage:
		return nil
	}
	return fmt.Errorf("unknown observation %q: use %s, %s or %s", o, obsText, obsPieces, obsImage)
}

// slotName names a corner or edge place by its faces, U/D first, then F/B, then R/L.
func slotName(p vec) string {
	var b strings.Builder
	for _, axis := range [3]int{1, 2, 0} {
		for k, n := range normals {
			if p[axis] != 0 && n[axis] == p[axis] {
				b.WriteString(faces[k])
			}
		}
	}
	return b.String()
}

var slotOrder = strings.Fields("UFR UFL UBL UBR DFR DFL DBL DBR UF UR UB UL FR FL BL BR DF DR DB DL")

// piecesText lists every corner and edge slot with the sticker shown on each of
// its faces and the slot the piece belongs to.
func (c *Cube) piecesText() string {
	lines := map[string]string{}
	for _, cb := range c.cubies {
		slot := slotName(cb.p)
		if len(slot) < 2 {
			continue
		}
		var parts []string
		for _, f := range slot {
			for k, n := range normals {
				if faces[k] != string(f) {
					continue
				}
				for i, local := range normals {
					if dot(cb.home, local) == 1 && cb.m.apply(local) == n {
						parts = append(parts, fmt.Sprintf("%c=%s", f, letters[i]))
					}
				}
			}
		}
		where := "belongs at " + slotName(cb.home)
		if cb.p == cb.home {
			where = "in its place, twisted"
			if len(slot) == 2 {
				where = "in its place, flipped"
			}
			if cb.m == (mat{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}) {
				where = "solved"
			}
		}
		lines[slot] = fmt.Sprintf("%s: %s (%s)\n", slot, strings.Join(parts, " "), where)
	}
	var b strings.Builder
	for i, slot := range slotOrder {
		if i == 0 {
			b.WriteString("Corners:\n")
		} else if i == 8 {
			b.WriteString("Edges:\n")
		}
		b.WriteString(lines[slot])
	}
	return b.String()
}

// StateText is the observation every player gets. With obsImage the face rows
// are left out: the faces travel as StateImage next to this text.
func (c *Cube) StateText(history []string, limit int, mode string) string {
	var b strings.Builder
	if mode == obsImage {
		b.WriteString("3x3 Rubik's cube. Colours: white, yellow, red, orange, green, blue.\n")
		b.WriteString(imageLegend)
	} else if mode == obsPieces {
		b.WriteString("3x3 Rubik's cube. Colours: W white, Y yellow, R red, O orange, G green, B blue. Centres never move: U is W, D is Y, F is G, B is B, R is R, L is O.\n")
		b.WriteString("Every corner and edge place is named by the faces it touches (UFR is the corner of Up, Front and Right; UF is the edge of Up and Front). X=c means the sticker of that piece on face X has colour c. In brackets: the place the piece belongs to.\n")
		b.WriteString(c.piecesText())
	} else {
		b.WriteString("3x3 Rubik's cube. Colours: W white, Y yellow, R red, O orange, G green, B blue.\n")
		b.WriteString("Each face is 3 rows, top to bottom, read from outside the cube (U with Back at the top, D with Front at the top, side faces with U at the top).\n")
		g := c.Facelets()
		for _, f := range faces {
			grid := g[f]
			rows := make([]string, 3)
			for i, r := range grid {
				rows[i] = strings.Join(r[:], "")
			}
			fmt.Fprintf(&b, "%s (%s, centre %s): %s\n", f, faceName[f[0]], grid[1][1], strings.Join(rows, " / "))
		}
	}
	fmt.Fprintf(&b, "Stickers matching their face centre: %d/54\n", c.Matched())
	p := c.Progress()
	b.WriteString("Progress by layer (a piece is solved when it is in its own place with its own orientation):\n")
	fmt.Fprintf(&b, "D layer (yellow): edges solved %d/4, corners solved %d/4\n", p.DEdges, p.DCorners)
	fmt.Fprintf(&b, "Middle layer: edges solved %d/4\n", p.MiddleEdges)
	fmt.Fprintf(&b, "U layer (white): edges white-up %d/4, edges solved %d/4, corners in place %d/4, corners solved %d/4",
		p.UEdgesWhiteUp, p.UEdgesSolved, p.UCornersInPlace, p.UCornersSolved)
	if limit > 0 {
		fmt.Fprintf(&b, "\nFace turns used: %d of %d", len(history), limit)
	}
	if len(history) > 0 {
		last := history[len(history)-1]
		fmt.Fprintf(&b, "\nMoves made so far (%d): %s\n", len(history), strings.Join(history, " "))
		fmt.Fprintf(&b, "Previous move: %s (undone by %s)", last, Inverse(last))
	}
	return b.String()
}

// DescribeMove is the criteria text for one offered move.
func DescribeMove(m string) string {
	how := "90° clockwise"
	if strings.HasSuffix(m, "'") {
		how = "90° counter-clockwise"
	} else if strings.HasSuffix(m, "2") {
		how = "180°"
	}
	return fmt.Sprintf("Turn the %s face %s (seen from outside that face)", faceName[m[0]], how)
}

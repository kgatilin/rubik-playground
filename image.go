package main

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// imageLegend replaces the face rows of the text observation when the faces
// are sent as a picture.
const imageLegend = "The faces are in the attached image, which shows the same cube three times. " +
	"Left: the unfolded net, U on top, then L F R B in one row, D at the bottom; every face is drawn as seen from outside the cube, and the net folds along the edges that touch. " +
	"Middle: the cube seen from its U-F-R corner. Right: the cube seen from its D-B-L corner, from below. " +
	"Every centre sticker carries the letter of its face.\n"

var stickerRGB = map[string]color.RGBA{
	"W": {245, 245, 245, 255}, "Y": {255, 213, 0, 255}, "R": {200, 30, 40, 255},
	"O": {255, 130, 20, 255}, "G": {0, 155, 72, 255}, "B": {0, 70, 173, 255},
}

type pt struct{ x, y float64 }

// StateImage renders the faces as one PNG: the net and two corner views.
func (c *Cube) StateImage() []byte {
	const cell = 56
	img := image.NewRGBA(image.Rect(0, 0, 1320, 580))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{28, 28, 32, 255}), image.Point{}, draw.Src)
	white := color.RGBA{230, 230, 230, 255}

	// net: face → origin in cells
	drawText(img, 20, 8, "net: U / L F R B / D", 2, white)
	origin := map[string][2]int{"U": {3, 0}, "L": {0, 3}, "F": {3, 3}, "R": {6, 3}, "B": {9, 3}, "D": {3, 6}}
	for f, grid := range c.Facelets() {
		o := origin[f]
		for r, row := range grid {
			for k, s := range row {
				x, y := float64(20+(o[0]+k)*cell), float64(50+(o[1]+r)*cell)
				fillQuad(img, [4]pt{{x + 2, y + 2}, {x + cell - 2, y + 2}, {x + cell - 2, y + cell - 2}, {x + 2, y + cell - 2}}, stickerRGB[s])
				if r == 1 && k == 1 {
					label(img, pt{x + cell/2, y + cell/2}, f, s)
				}
			}
		}
	}

	drawText(img, 740, 8, "from U-F-R corner", 2, white)
	c.cornerView(img, pt{880, 300}, 52, [3]float64{1, 1, 1})
	drawText(img, 1030, 8, "from D-B-L corner", 2, white)
	c.cornerView(img, pt{1170, 300}, 52, [3]float64{-1, -1, -1})

	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

// cornerView draws the three faces visible from direction d (towards the
// camera) in an orthographic projection centred at o, y of the cube up.
func (c *Cube) cornerView(img *image.RGBA, o pt, scale float64, d [3]float64) {
	cross := func(a, b [3]float64) [3]float64 {
		return [3]float64{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
	}
	norm := func(a [3]float64) [3]float64 {
		l := math.Sqrt(a[0]*a[0] + a[1]*a[1] + a[2]*a[2])
		return [3]float64{a[0] / l, a[1] / l, a[2] / l}
	}
	right := norm(cross([3]float64{0, 1, 0}, d))
	up := norm(cross(d, right))
	project := func(p [3]float64) pt {
		return pt{o.x + scale*(p[0]*right[0]+p[1]*right[1]+p[2]*right[2]),
			o.y - scale*(p[0]*up[0]+p[1]*up[1]+p[2]*up[2])}
	}
	for _, cb := range c.cubies {
		for i, n := range normals {
			if dot(cb.home, n) != 1 {
				continue
			}
			w := cb.m.apply(n)
			if float64(w[0])*d[0]+float64(w[1])*d[1]+float64(w[2])*d[2] <= 0 {
				continue
			}
			// the two axes of the sticker plane
			var a, b [3]float64
			switch {
			case w[0] != 0:
				a, b = [3]float64{0, 1, 0}, [3]float64{0, 0, 1}
			case w[1] != 0:
				a, b = [3]float64{1, 0, 0}, [3]float64{0, 0, 1}
			default:
				a, b = [3]float64{1, 0, 0}, [3]float64{0, 1, 0}
			}
			var centre [3]float64
			for k := range 3 {
				centre[k] = float64(cb.p[k]) + 0.5*float64(w[k])
			}
			var q [4]pt
			for j, sg := range [4][2]float64{{-1, -1}, {1, -1}, {1, 1}, {-1, 1}} {
				var p [3]float64
				for k := range 3 {
					p[k] = centre[k] + 0.46*(sg[0]*a[k]+sg[1]*b[k])
				}
				q[j] = project(p)
			}
			fillQuad(img, q, stickerRGB[letters[i]])
			if cb.home == n { // centre piece
				label(img, project(centre), faces[i], letters[i])
			}
		}
	}
}

// fillQuad fills a convex quadrilateral.
func fillQuad(img *image.RGBA, q [4]pt, col color.RGBA) {
	minX, minY, maxX, maxY := q[0].x, q[0].y, q[0].x, q[0].y
	for _, p := range q[1:] {
		minX, maxX = math.Min(minX, p.x), math.Max(maxX, p.x)
		minY, maxY = math.Min(minY, p.y), math.Max(maxY, p.y)
	}
	for y := int(minY); y <= int(maxY)+1; y++ {
		for x := int(minX); x <= int(maxX)+1; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			pos, neg := false, false
			for j := range 4 {
				a, b := q[j], q[(j+1)%4]
				if s := (b.x-a.x)*(py-a.y) - (b.y-a.y)*(px-a.x); s > 0 {
					pos = true
				} else if s < 0 {
					neg = true
				}
			}
			if !(pos && neg) {
				img.SetRGBA(x, y, col)
			}
		}
	}
}

// label writes a face letter centred on a centre sticker of colour s.
func label(img *image.RGBA, at pt, face, s string) {
	col := color.RGBA{0, 0, 0, 255}
	if s == "R" || s == "G" || s == "B" {
		col = color.RGBA{255, 255, 255, 255}
	}
	drawText(img, int(at.x)-7, int(at.y)-13, face, 2, col)
}

// drawText writes s with its top-left corner at x, y, the 7x13 bitmap font scaled up.
func drawText(img *image.RGBA, x, y int, s string, scale int, col color.RGBA) {
	mask := image.NewAlpha(image.Rect(0, 0, 7*len(s), 13))
	(&font.Drawer{Dst: mask, Src: image.Opaque, Face: basicfont.Face7x13, Dot: fixed.P(0, 11)}).DrawString(s)
	for my := range 13 {
		for mx := range 7 * len(s) {
			if mask.AlphaAt(mx, my).A > 127 {
				draw.Draw(img, image.Rect(x+mx*scale, y+my*scale, x+(mx+1)*scale, y+(my+1)*scale), image.NewUniform(col), image.Point{}, draw.Src)
			}
		}
	}
}

package main

import (
	"bytes"
	"image/png"
	"os"
	"testing"
)

func TestStateImageDecodes(t *testing.T) {
	c := NewCube()
	c.ApplyAll([]string{"R", "U", "F'"})
	data := c.StateImage()
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if p := os.Getenv("CUBE_PNG"); p != "" {
		os.WriteFile(p, data, 0o644)
	}
}

package main

import (
	"image"
	"image/color"
	"testing"
)

// A flat pane is how "the provider ignored the grid" presents. The usable-pane
// rate feeds release gate 2, so a flat fill must never count as a usable image.
func TestIsUniformRejectsFlatPaneAndAcceptsContent(t *testing.T) {
	flat := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := range 8 {
		for x := range 8 {
			flat.Set(x, y, color.RGBA{R: 12, G: 34, B: 56, A: 255})
		}
	}
	if !isUniform(flat) {
		t.Fatal("a single-colour pane must be reported as uniform")
	}

	// One differing pixel is enough to make the pane real content.
	flat.Set(7, 7, color.RGBA{R: 200, G: 0, B: 0, A: 255})
	if isUniform(flat) {
		t.Fatal("a pane with differing pixels must not be reported as uniform")
	}
}

// The cut must be deterministic, row-major, and exactly cover the declared
// grid, or a reviewer cannot reproduce which pane a key refers to.
func TestSliceSheetCutsRowMajorWithEqualCells(t *testing.T) {
	sheet := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for y := range 20 {
		for x := range 40 {
			sheet.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 7, A: 255})
		}
	}
	keys := []string{"a", "b", "c", "d", "e", "f"}
	cells := sliceSheet(sheet, sampleInput{rows: 2, cols: 3, keys: keys, outDir: t.TempDir(), index: 1})

	if len(cells) != len(keys) {
		t.Fatalf("expected %d panes, got %d", len(keys), len(cells))
	}
	for i, c := range cells {
		if c.Key != keys[i] {
			t.Fatalf("pane %d: expected key %q, got %q", i, keys[i], c.Key)
		}
		if c.Width != 40/3 || c.Height != 20/2 {
			t.Fatalf("pane %q: expected %dx%d, got %dx%d", c.Key, 40/3, 20/2, c.Width, c.Height)
		}
		if !c.Usable || c.File == "" {
			t.Fatalf("pane %q: expected a usable written pane, got %+v", c.Key, c)
		}
	}
	// Row-major: the fourth key starts the second row.
	if cells[3].Row != 1 || cells[3].Column != 0 {
		t.Fatalf("expected pane 4 at row 1 col 0, got row %d col %d", cells[3].Row, cells[3].Column)
	}
}

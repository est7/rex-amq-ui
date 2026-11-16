// Package rastertest compares rendered frames with committed reference PNGs.
package rastertest

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// Update rewrites the reference PNGs instead of comparing against them:
// go test ./... -run <Test> -update
var Update = flag.Bool("update", false, "rewrite reference PNGs under testdata")

// Tolerance is the largest per-channel difference a pixel may show against
// its reference: float rounding (fused multiply-add on some CPUs) moves an
// anti-aliased pixel by one or two levels, never more.
const Tolerance = 2

// Golden compares got with the PNG at path, or writes it there with
// -update.
func Golden(t *testing.T, path string, got *image.RGBA) {
	t.Helper()
	if *Update {
		var buf bytes.Buffer
		if err := png.Encode(&buf, got); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", path)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("reference frame: %v (run with -update to create it)", err)
	}
	defer f.Close()
	want, err := png.Decode(f)
	if err != nil {
		t.Fatalf("reference frame %s: %v", path, err)
	}
	if err := Diff(want, got, Tolerance); err != nil {
		t.Errorf("%s: %v (inspect, then run with -update if the change is intended)", path, err)
	}
}

// Diff reports how got differs from want beyond tol per channel: a size
// mismatch, or the count of differing pixels and the worst one.
func Diff(want image.Image, got *image.RGBA, tol int) error {
	if want.Bounds().Size() != got.Bounds().Size() {
		return fmt.Errorf("size %v, want %v", got.Bounds().Size(), want.Bounds().Size())
	}
	wb, gb := want.Bounds(), got.Bounds()
	bad, worst, at := 0, 0, image.Point{}
	for y := 0; y < wb.Dy(); y++ {
		for x := 0; x < wb.Dx(); x++ {
			r1, g1, b1, a1 := want.At(wb.Min.X+x, wb.Min.Y+y).RGBA()
			r2, g2, b2, a2 := got.At(gb.Min.X+x, gb.Min.Y+y).RGBA()
			d := max(absDiff(r1, r2), absDiff(g1, g2), absDiff(b1, b2), absDiff(a1, a2))
			if d > tol {
				bad++
				if d > worst {
					worst, at = d, image.Pt(x, y)
				}
			}
		}
	}
	if bad > 0 {
		return fmt.Errorf("%d pixels differ by more than %d; worst %d at %v", bad, tol, worst, at)
	}
	return nil
}

func absDiff(a, b uint32) int {
	d := int(a>>8) - int(b>>8)
	if d < 0 {
		return -d
	}
	return d
}

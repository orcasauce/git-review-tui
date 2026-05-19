package layout

import "testing"

func TestComputeTooSmall(t *testing.T) {
	cases := []struct {
		name string
		w, h int
	}{
		{"both below", 40, 10},
		{"width just below", MinCols - 1, MinRows},
		{"height just below", MinCols, MinRows - 1},
		{"zero", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Compute(tc.w, tc.h)
			if !got.TooSmall {
				t.Fatalf("expected TooSmall for %dx%d, got %+v", tc.w, tc.h, got)
			}
			if got.SmallMode {
				t.Fatalf("TooSmall should not also set SmallMode")
			}
			if got.TopLeft != (Rect{}) || got.TopRight != (Rect{}) ||
				got.BottomLeft != (Rect{}) || got.BottomRight != (Rect{}) {
				t.Fatalf("expected zero rectangles when TooSmall, got %+v", got)
			}
		})
	}
}

func TestComputeSmallMode(t *testing.T) {
	// w just below SmallModeMinCols, h sufficient
	w, h := SmallModeMinCols-1, 40
	got := Compute(w, h)
	if got.TooSmall {
		t.Fatalf("unexpected TooSmall for %dx%d", w, h)
	}
	if !got.SmallMode {
		t.Fatalf("expected SmallMode for %dx%d, got %+v", w, h, got)
	}
	if got.TopRight != (Rect{}) || got.BottomRight != (Rect{}) {
		t.Fatalf("expected zero right rectangles in small mode, got %+v", got)
	}
	if got.TopLeft.W != w {
		t.Fatalf("top-left should be full width, got %d want %d", got.TopLeft.W, w)
	}
	if got.BottomLeft.W != w {
		t.Fatalf("bottom-left should be full width, got %d want %d", got.BottomLeft.W, w)
	}
	if got.TopLeft.H+got.BottomLeft.H != h-1 {
		t.Fatalf("top+bottom heights should sum to h-1 (status row), got %d+%d=%d, want %d",
			got.TopLeft.H, got.BottomLeft.H, got.TopLeft.H+got.BottomLeft.H, h-1)
	}
	if got.Status.Y != h-1 || got.Status.W != w || got.Status.H != 1 {
		t.Fatalf("status row wrong: %+v", got.Status)
	}
}

func TestComputeSmallModeBoundary(t *testing.T) {
	// At exactly SmallModeMinCols, we should NOT be in small mode.
	got := Compute(SmallModeMinCols, 40)
	if got.SmallMode {
		t.Fatalf("at boundary %d cols should not be SmallMode", SmallModeMinCols)
	}
	if got.TopRight == (Rect{}) || got.BottomRight == (Rect{}) {
		t.Fatalf("right rectangles should be non-zero at boundary, got %+v", got)
	}
}

func TestComputeFullLayout(t *testing.T) {
	cases := []struct {
		name string
		w, h int
	}{
		{"min full", MinCols, MinRows},
		{"100x40", 100, 40},
		{"200x60", 200, 60},
		{"wide", 300, 80},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Compute(tc.w, tc.h)
			if got.TooSmall {
				t.Fatalf("unexpected TooSmall for %dx%d", tc.w, tc.h)
			}
			if got.SmallMode && tc.w >= SmallModeMinCols {
				t.Fatalf("unexpected SmallMode for %dx%d", tc.w, tc.h)
			}

			// Status row at the bottom, one row tall, full width.
			if got.Status.Y != tc.h-1 || got.Status.H != 1 || got.Status.W != tc.w {
				t.Fatalf("status row wrong: %+v", got.Status)
			}

			// In full mode, left + right widths must equal terminal width.
			if !got.SmallMode {
				if got.TopLeft.W+got.TopRight.W != tc.w {
					t.Fatalf("top row widths do not sum to %d: %d+%d", tc.w, got.TopLeft.W, got.TopRight.W)
				}
				if got.BottomLeft.W+got.BottomRight.W != tc.w {
					t.Fatalf("bottom row widths do not sum to %d: %d+%d", tc.w, got.BottomLeft.W, got.BottomRight.W)
				}
				if got.TopLeft.W != got.BottomLeft.W {
					t.Fatalf("left column widths differ: top %d bottom %d", got.TopLeft.W, got.BottomLeft.W)
				}
				if got.TopRight.X != got.TopLeft.W || got.BottomRight.X != got.BottomLeft.W {
					t.Fatalf("right column X mismatch")
				}
			}

			// Top + bottom heights must equal h-1 (status row reserved).
			if got.TopLeft.H+got.BottomLeft.H != tc.h-1 {
				t.Fatalf("top+bottom heights = %d+%d, want %d",
					got.TopLeft.H, got.BottomLeft.H, tc.h-1)
			}
			if !got.SmallMode && got.TopRight.H+got.BottomRight.H != tc.h-1 {
				t.Fatalf("right top+bottom heights = %d+%d, want %d",
					got.TopRight.H, got.BottomRight.H, tc.h-1)
			}

			// Top row honors the minimum.
			if got.TopLeft.H < MinTopRows {
				t.Fatalf("top row height %d below MinTopRows %d", got.TopLeft.H, MinTopRows)
			}

			// All rectangles start at the origin we expect.
			if got.TopLeft.X != 0 || got.TopLeft.Y != 0 {
				t.Fatalf("top-left should originate at (0,0), got (%d,%d)", got.TopLeft.X, got.TopLeft.Y)
			}
			if got.BottomLeft.X != 0 || got.BottomLeft.Y != got.TopLeft.H {
				t.Fatalf("bottom-left should be at (0,%d), got (%d,%d)",
					got.TopLeft.H, got.BottomLeft.X, got.BottomLeft.Y)
			}
		})
	}
}

func TestComputeLargeTerminalUsesFraction(t *testing.T) {
	w, h := 200, 60
	got := Compute(w, h)
	wantLeft := int(float64(w) * LeftColumnFraction)
	if got.TopLeft.W != wantLeft {
		t.Fatalf("left column W = %d, want %d", got.TopLeft.W, wantLeft)
	}
	// usableH = h-1 = 59; topH = floor(59*0.20) = 11
	usableH := h - 1
	wantTop := int(float64(usableH) * TopRowFraction)
	if wantTop < MinTopRows {
		wantTop = MinTopRows
	}
	if got.TopLeft.H != wantTop {
		t.Fatalf("top row H = %d, want %d", got.TopLeft.H, wantTop)
	}
}

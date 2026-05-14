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
			got := Compute(tc.w, tc.h, 0)
			if !got.TooSmall {
				t.Fatalf("expected TooSmall for %dx%d, got %+v", tc.w, tc.h, got)
			}
			if got.SmallMode {
				t.Fatalf("TooSmall should not also set SmallMode")
			}
			if got.TopLeft != (Rect{}) || got.TopRight != (Rect{}) ||
				got.Files != (Rect{}) || got.Diff != (Rect{}) {
				t.Fatalf("expected zero rectangles when TooSmall, got %+v", got)
			}
		})
	}
}

func TestComputeSmallMode(t *testing.T) {
	// w just below SmallModeMinCols, h sufficient
	w, h := SmallModeMinCols-1, 40
	got := Compute(w, h, 3)
	if got.TooSmall {
		t.Fatalf("unexpected TooSmall for %dx%d", w, h)
	}
	if !got.SmallMode {
		t.Fatalf("expected SmallMode for %dx%d, got %+v", w, h, got)
	}
	if got.TopRight != (Rect{}) {
		t.Fatalf("expected zero TopRight in small mode, got %+v", got.TopRight)
	}
	if got.TopLeft.W != w {
		t.Fatalf("top-left should be full width, got %d want %d", got.TopLeft.W, w)
	}
	if got.Files.W != w || got.Diff.W != w {
		t.Fatalf("files/diff should be full width in small mode, got files.W=%d diff.W=%d want %d",
			got.Files.W, got.Diff.W, w)
	}
	// usable rows = h - 1 (status) - 2 (vertical gaps between top/files
	// and files/diff). Panel heights must sum to that.
	wantSum := h - 3
	if got.TopLeft.H+got.Files.H+got.Diff.H != wantSum {
		t.Fatalf("top+files+diff heights should sum to %d, got %d+%d+%d=%d",
			wantSum, got.TopLeft.H, got.Files.H, got.Diff.H,
			got.TopLeft.H+got.Files.H+got.Diff.H)
	}
	if got.Status.Y != h-1 || got.Status.W != w || got.Status.H != 1 {
		t.Fatalf("status row wrong: %+v", got.Status)
	}
}

func TestComputeSmallModeBoundary(t *testing.T) {
	// At exactly SmallModeMinCols, we should NOT be in small mode.
	got := Compute(SmallModeMinCols, 40, 0)
	if got.SmallMode {
		t.Fatalf("at boundary %d cols should not be SmallMode", SmallModeMinCols)
	}
	if got.TopRight == (Rect{}) {
		t.Fatalf("TopRight should be non-zero at boundary, got %+v", got)
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
			got := Compute(tc.w, tc.h, 3)
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

			// Top row widths plus the 1-column gap sum to terminal width
			// in full mode.
			if !got.SmallMode && got.TopLeft.W+got.TopRight.W != tc.w-1 {
				t.Fatalf("top row widths + 1-col gap do not sum to %d: %d+1+%d",
					tc.w, got.TopLeft.W, got.TopRight.W)
			}

			// Files and Diff are always full-width.
			if got.Files.W != tc.w || got.Diff.W != tc.w {
				t.Fatalf("files/diff should be full width, got files.W=%d diff.W=%d want %d",
					got.Files.W, got.Diff.W, tc.w)
			}

			// Heights cover the usable region minus the two 1-row gaps.
			if got.TopLeft.H+got.Files.H+got.Diff.H != tc.h-3 {
				t.Fatalf("top+files+diff heights = %d+%d+%d, want %d",
					got.TopLeft.H, got.Files.H, got.Diff.H, tc.h-3)
			}

			// Top row honors the minimum.
			if got.TopLeft.H < MinTopRows {
				t.Fatalf("top row height %d below MinTopRows %d", got.TopLeft.H, MinTopRows)
			}

			// Origins. Files sits 1 row below the top row; diff sits 1
			// row below files.
			if got.TopLeft.X != 0 || got.TopLeft.Y != 0 {
				t.Fatalf("top-left should originate at (0,0), got (%d,%d)", got.TopLeft.X, got.TopLeft.Y)
			}
			if got.Files.X != 0 || got.Files.Y != got.TopLeft.H+1 {
				t.Fatalf("files should be at (0,%d), got (%d,%d)",
					got.TopLeft.H+1, got.Files.X, got.Files.Y)
			}
			if got.Diff.X != 0 || got.Diff.Y != got.Files.Y+got.Files.H+1 {
				t.Fatalf("diff should be at (0,%d), got (%d,%d)",
					got.Files.Y+got.Files.H+1, got.Diff.X, got.Diff.Y)
			}
			// Status sits flush against diff (no gap).
			if got.Status.Y != got.Diff.Y+got.Diff.H {
				t.Fatalf("status should sit at diff bottom %d, got %d",
					got.Diff.Y+got.Diff.H, got.Status.Y)
			}
			if !got.SmallMode {
				if got.TopRight.X != got.TopLeft.W+1 || got.TopRight.Y != 0 {
					t.Fatalf("top-right origin wrong: (%d,%d) want (%d,0)",
						got.TopRight.X, got.TopRight.Y, got.TopLeft.W+1)
				}
				if got.TopRight.H != got.TopLeft.H {
					t.Fatalf("top row heights differ: left %d right %d", got.TopLeft.H, got.TopRight.H)
				}
			}
		})
	}
}

func TestComputeLargeTerminalUsesFraction(t *testing.T) {
	w, h := 200, 60
	got := Compute(w, h, 5)
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

func TestComputeFilesHeight(t *testing.T) {
	// Files panel height = clamp(1+numFiles, FilesPanelMinRows, FilesPanelMaxRows).
	cases := []struct {
		name     string
		numFiles int
		want     int
	}{
		{"zero files takes floor", 0, FilesPanelMinRows},
		{"one file -> 2 rows", 1, 2},
		{"two files -> 3 rows", 2, 3},
		{"seven files -> cap", 7, FilesPanelMaxRows},
		{"thirty files -> cap", 30, FilesPanelMaxRows},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Compute(200, 60, tc.numFiles)
			if got.Files.H != tc.want {
				t.Fatalf("Files.H = %d, want %d", got.Files.H, tc.want)
			}
		})
	}
}

func TestComputeDiffTakesRemainder(t *testing.T) {
	w, h := 200, 60
	got := Compute(w, h, 4)
	usableH := h - 1
	// Bottom region = usable height - top row - 2 vertical gaps.
	wantBottom := usableH - got.TopLeft.H - 2
	if got.Files.H+got.Diff.H != wantBottom {
		t.Fatalf("files+diff heights = %d+%d, want %d",
			got.Files.H, got.Diff.H, wantBottom)
	}
}

func TestComputeMinSize(t *testing.T) {
	// At the minimum supported size, the diff panel must still be non-empty.
	got := Compute(MinCols, MinRows, 0)
	if got.TooSmall {
		t.Fatalf("MinCols x MinRows should not be TooSmall")
	}
	if got.Diff.H <= 0 {
		t.Fatalf("diff panel collapsed at minimum size: %+v", got.Diff)
	}
}

func TestComputeHorizontalGapFullMode(t *testing.T) {
	w, h := 200, 60
	got := Compute(w, h, 3)
	if got.TopLeft.X != 0 {
		t.Fatalf("TopLeft.X = %d, want 0", got.TopLeft.X)
	}
	if got.TopRight.X != got.TopLeft.W+1 {
		t.Fatalf("TopRight.X = %d, want %d (1-col gap after TopLeft)",
			got.TopRight.X, got.TopLeft.W+1)
	}
	if got.TopLeft.W+1+got.TopRight.W != w {
		t.Fatalf("TopLeft.W + gap + TopRight.W = %d+1+%d, want %d",
			got.TopLeft.W, got.TopRight.W, w)
	}
}

func TestComputeNoHorizontalGapSmallMode(t *testing.T) {
	w, h := SmallModeMinCols-1, 40
	got := Compute(w, h, 2)
	if !got.SmallMode {
		t.Fatalf("expected SmallMode at w=%d", w)
	}
	if got.TopLeft.W != w {
		t.Fatalf("small-mode TopLeft.W = %d, want full width %d", got.TopLeft.W, w)
	}
	if got.TopRight != (Rect{}) {
		t.Fatalf("small-mode TopRight should be zero, got %+v", got.TopRight)
	}
}

func TestComputeVerticalGaps(t *testing.T) {
	cases := []struct {
		name string
		w, h int
	}{
		{"min", MinCols, MinRows},
		{"small mode", SmallModeMinCols - 1, 40},
		{"full mode", 200, 60},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Compute(tc.w, tc.h, 3)
			topH := got.TopLeft.H
			if got.Files.Y != topH+1 {
				t.Fatalf("Files.Y = %d, want %d (1-row gap after top)",
					got.Files.Y, topH+1)
			}
			if got.Diff.Y != got.Files.Y+got.Files.H+1 {
				t.Fatalf("Diff.Y = %d, want %d (1-row gap after files)",
					got.Diff.Y, got.Files.Y+got.Files.H+1)
			}
		})
	}
}

func TestComputeDiffFlushAgainstStatus(t *testing.T) {
	cases := []struct {
		name string
		w, h int
	}{
		{"min", MinCols, MinRows},
		{"small mode", SmallModeMinCols - 1, 40},
		{"full mode", 200, 60},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Compute(tc.w, tc.h, 3)
			if got.Status.Y != tc.h-1 {
				t.Fatalf("Status.Y = %d, want %d", got.Status.Y, tc.h-1)
			}
			if got.Diff.Y+got.Diff.H != got.Status.Y {
				t.Fatalf("Diff bottom %d should be flush against status %d",
					got.Diff.Y+got.Diff.H, got.Status.Y)
			}
		})
	}
}

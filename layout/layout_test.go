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
			if got.Log != (Rect{}) || got.Metadata != (Rect{}) ||
				got.Files != (Rect{}) || got.Message != (Rect{}) ||
				got.Diff != (Rect{}) {
				t.Fatalf("expected zero rectangles when TooSmall, got %+v", got)
			}
		})
	}
}

func TestMinRowsDerivation(t *testing.T) {
	// MinRows must equal log + middle + diff + status + 2 gaps so that
	// any change to the panel row constants automatically updates the
	// floor.
	want := LogContentRows + 1 + MiddleContentRows + 1 + MinDiffContentRows + 1 + 1 + 2
	if MinRows != want {
		t.Fatalf("MinRows = %d, want %d (derived from panel constants)", MinRows, want)
	}
	// At exactly MinRows the diff panel must still hold at least
	// MinDiffContentRows + 1 rows.
	got := Compute(MinCols, MinRows)
	if got.TooSmall {
		t.Fatalf("at MinCols x MinRows expected layout to render, got TooSmall")
	}
	if got.Diff.H < MinDiffContentRows+1 {
		t.Fatalf("at MinRows diff.H = %d, want >= %d", got.Diff.H, MinDiffContentRows+1)
	}
}

func TestComputeSmallMode(t *testing.T) {
	w, h := SmallModeMinCols-1, 40
	got := Compute(w, h)
	if got.TooSmall {
		t.Fatalf("unexpected TooSmall for %dx%d", w, h)
	}
	if !got.SmallMode {
		t.Fatalf("expected SmallMode for %dx%d, got %+v", w, h, got)
	}
	// All four named middle-row panels share one rectangle in small
	// mode so the caller can render any active tab into it.
	if got.Metadata != got.Files || got.Files != got.Message {
		t.Fatalf("small-mode Metadata/Files/Message should share a rect, got\nM=%+v\nF=%+v\nMsg=%+v",
			got.Metadata, got.Files, got.Message)
	}
	if got.Log.W != w {
		t.Fatalf("log should be full width in small mode, got %d want %d", got.Log.W, w)
	}
	if got.Files.W != w || got.Message.W != w || got.Diff.W != w {
		t.Fatalf("middle/diff should be full width in small mode, got files.W=%d message.W=%d diff.W=%d want %d",
			got.Files.W, got.Message.W, got.Diff.W, w)
	}
	// Panel heights plus 2 vertical gaps plus status sum to h.
	wantSum := h - 1 - 2
	if got.Log.H+got.Files.H+got.Diff.H != wantSum {
		t.Fatalf("log+middle+diff heights should sum to %d, got %d+%d+%d=%d",
			wantSum, got.Log.H, got.Files.H, got.Diff.H,
			got.Log.H+got.Files.H+got.Diff.H)
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
	if got.Message.W == got.Files.W {
		// In full mode the columns must have distinct widths (60/40
		// split with a 1-col gap).
		t.Fatalf("full mode left/right column widths should differ, got files.W=%d message.W=%d",
			got.Files.W, got.Message.W)
	}
}

func TestComputeFullLayout(t *testing.T) {
	cases := []struct {
		name string
		w, h int
	}{
		{"min full", MinCols, MinRows},
		{"at small-mode boundary", SmallModeMinCols, MinRows + 10},
		{"comfortably above", 140, 50},
		{"wide", 240, 80},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Compute(tc.w, tc.h)
			if got.TooSmall {
				t.Fatalf("unexpected TooSmall for %dx%d", tc.w, tc.h)
			}
			smallExpected := tc.w < SmallModeMinCols
			if got.SmallMode != smallExpected {
				t.Fatalf("SmallMode = %v, want %v at %dx%d", got.SmallMode, smallExpected, tc.w, tc.h)
			}

			// Status row: 1 row tall, full width, at the bottom.
			if got.Status.Y != tc.h-1 || got.Status.H != 1 || got.Status.W != tc.w {
				t.Fatalf("status row wrong: %+v", got.Status)
			}

			// Log is always full-width on top with LogContentRows+1 rows.
			if got.Log.X != 0 || got.Log.Y != 0 {
				t.Fatalf("log should originate at (0,0), got (%d,%d)", got.Log.X, got.Log.Y)
			}
			if got.Log.W != tc.w {
				t.Fatalf("log.W = %d, want full width %d", got.Log.W, tc.w)
			}
			if got.Log.H != LogContentRows+1 {
				t.Fatalf("log.H = %d, want %d (content + header)", got.Log.H, LogContentRows+1)
			}

			// Middle row sits 1 row below the log and is
			// MiddleContentRows + 1 tall.
			middleY := got.Log.H + 1
			if got.Message.Y != middleY {
				t.Fatalf("message.Y = %d, want %d", got.Message.Y, middleY)
			}
			if got.Message.H != MiddleContentRows+1 {
				t.Fatalf("message.H = %d, want %d", got.Message.H, MiddleContentRows+1)
			}

			if !got.SmallMode {
				// 60/40 split.
				wantLeft := int(float64(tc.w) * LeftColumnFraction)
				if got.Metadata.W != wantLeft {
					t.Fatalf("metadata.W = %d, want %d", got.Metadata.W, wantLeft)
				}
				if got.Files.W != wantLeft {
					t.Fatalf("files.W = %d, want %d", got.Files.W, wantLeft)
				}
				if got.Metadata.X != 0 || got.Files.X != 0 {
					t.Fatalf("left-column rects should originate at X=0, got metadata.X=%d files.X=%d",
						got.Metadata.X, got.Files.X)
				}
				if got.Message.X != wantLeft+1 {
					t.Fatalf("message.X = %d, want %d (1-col gap after left column)",
						got.Message.X, wantLeft+1)
				}
				if got.Metadata.W+1+got.Message.W != tc.w {
					t.Fatalf("left + gap + message widths = %d+1+%d, want %d",
						got.Metadata.W, got.Message.W, tc.w)
				}

				// Metadata is fixed at MetadataContentRows; files
				// fills the rest of the middle row's left column.
				if got.Metadata.Y != middleY {
					t.Fatalf("metadata.Y = %d, want %d", got.Metadata.Y, middleY)
				}
				if got.Metadata.H != MetadataContentRows {
					t.Fatalf("metadata.H = %d, want %d", got.Metadata.H, MetadataContentRows)
				}
				if got.Files.Y != middleY+MetadataContentRows {
					t.Fatalf("files.Y = %d, want %d (directly below metadata)",
						got.Files.Y, middleY+MetadataContentRows)
				}
				if got.Files.H != MiddleContentRows+1-MetadataContentRows {
					t.Fatalf("files.H = %d, want %d (middle - metadata)",
						got.Files.H, MiddleContentRows+1-MetadataContentRows)
				}
				// Metadata + Files together span the same height as
				// Message — the two columns must visually align.
				if got.Metadata.H+got.Files.H != got.Message.H {
					t.Fatalf("metadata.H + files.H = %d+%d, want = message.H = %d",
						got.Metadata.H, got.Files.H, got.Message.H)
				}
			}

			// Diff sits 1 row below the middle row and fills the
			// rest above the status row.
			wantDiffY := middleY + (MiddleContentRows + 1) + 1
			if got.Diff.Y != wantDiffY {
				t.Fatalf("diff.Y = %d, want %d", got.Diff.Y, wantDiffY)
			}
			if got.Diff.W != tc.w {
				t.Fatalf("diff.W = %d, want %d (full width)", got.Diff.W, tc.w)
			}
			if got.Diff.Y+got.Diff.H != got.Status.Y {
				t.Fatalf("diff bottom %d should be flush against status %d",
					got.Diff.Y+got.Diff.H, got.Status.Y)
			}
			if got.Diff.H < MinDiffContentRows+1 {
				t.Fatalf("diff.H = %d, want >= %d (floor)", got.Diff.H, MinDiffContentRows+1)
			}
		})
	}
}

func TestComputeRectsDoNotOverlap(t *testing.T) {
	// In full mode the five named rectangles plus the status row
	// must not overlap each other.
	got := Compute(200, 60)
	rects := []struct {
		name string
		r    Rect
	}{
		{"log", got.Log},
		{"metadata", got.Metadata},
		{"files", got.Files},
		{"message", got.Message},
		{"diff", got.Diff},
		{"status", got.Status},
	}
	for i := range rects {
		for j := i + 1; j < len(rects); j++ {
			a, b := rects[i], rects[j]
			if rectsOverlap(a.r, b.r) {
				t.Errorf("%s %+v overlaps %s %+v", a.name, a.r, b.name, b.r)
			}
		}
	}
}

func rectsOverlap(a, b Rect) bool {
	if a.W == 0 || a.H == 0 || b.W == 0 || b.H == 0 {
		return false
	}
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

func TestScrollbarThumb(t *testing.T) {
	cases := []struct {
		name                           string
		total, visible, offset, height int
		wantStart, wantLength          int
		wantDraw                       bool
	}{
		{
			name: "content fits exactly",
			total: 10, visible: 10, offset: 0, height: 10,
			wantStart: 0, wantLength: 0, wantDraw: false,
		},
		{
			name: "content smaller than viewport",
			total: 5, visible: 10, offset: 0, height: 10,
			wantStart: 0, wantLength: 0, wantDraw: false,
		},
		{
			name: "zero height",
			total: 100, visible: 10, offset: 0, height: 0,
			wantStart: 0, wantLength: 0, wantDraw: false,
		},
		{
			name: "offset zero -> thumb at top",
			total: 100, visible: 10, offset: 0, height: 10,
			wantStart: 0, wantLength: 1, wantDraw: true,
		},
		{
			name: "offset max -> thumb at bottom",
			total: 100, visible: 10, offset: 90, height: 10,
			wantStart: 9, wantLength: 1, wantDraw: true,
		},
		{
			name: "offset above max is clamped",
			total: 100, visible: 10, offset: 999, height: 10,
			wantStart: 9, wantLength: 1, wantDraw: true,
		},
		{
			name: "negative offset is clamped",
			total: 100, visible: 10, offset: -5, height: 10,
			wantStart: 0, wantLength: 1, wantDraw: true,
		},
		{
			name: "mid scroll -> proportional start",
			total: 100, visible: 20, offset: 40, height: 10,
			wantStart: 4, wantLength: 2, wantDraw: true,
		},
		{
			name: "thumb length proportional",
			total: 100, visible: 50, offset: 0, height: 20,
			wantStart: 0, wantLength: 10, wantDraw: true,
		},
		{
			name: "tiny visible vs huge total -> length at least 1",
			total: 100000, visible: 1, offset: 0, height: 10,
			wantStart: 0, wantLength: 1, wantDraw: true,
		},
		{
			name: "tiny visible at max offset",
			total: 100000, visible: 1, offset: 99999, height: 10,
			wantStart: 9, wantLength: 1, wantDraw: true,
		},
		{
			name: "height 1 with overflow",
			total: 100, visible: 10, offset: 0, height: 1,
			wantStart: 0, wantLength: 1, wantDraw: true,
		},
		{
			name: "height 1 at max offset",
			total: 100, visible: 10, offset: 90, height: 1,
			wantStart: 0, wantLength: 1, wantDraw: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, length, draw := ScrollbarThumb(tc.total, tc.visible, tc.offset, tc.height)
			if draw != tc.wantDraw {
				t.Fatalf("draw = %v, want %v", draw, tc.wantDraw)
			}
			if start != tc.wantStart {
				t.Fatalf("start = %d, want %d", start, tc.wantStart)
			}
			if length != tc.wantLength {
				t.Fatalf("length = %d, want %d", length, tc.wantLength)
			}
			if draw {
				if length < 1 {
					t.Fatalf("length %d < 1 when draw=true", length)
				}
				if start < 0 || start+length > tc.height {
					t.Fatalf("thumb [%d,%d) escapes track of height %d",
						start, start+length, tc.height)
				}
			}
		})
	}
}

func TestScrollbarThumbMonotonic(t *testing.T) {
	total, visible, height := 200, 20, 30
	maxOffset := total - visible
	prevStart := -1
	var lastStart, lastLength int
	for off := 0; off <= maxOffset; off++ {
		start, length, draw := ScrollbarThumb(total, visible, off, height)
		if !draw {
			t.Fatalf("offset %d: want draw=true", off)
		}
		if start < prevStart {
			t.Fatalf("offset %d: start %d went backwards from %d", off, start, prevStart)
		}
		prevStart = start
		lastStart, lastLength = start, length
	}
	if lastStart+lastLength != height {
		t.Fatalf("at max offset, thumb end = %d, want %d", lastStart+lastLength, height)
	}
}

// Package layout computes panel rectangles from terminal dimensions.
//
// The layout has three regions stacked vertically: a top row containing
// the log and message panels side-by-side (full mode) or a single
// full-width panel (small mode), a full-width files panel below it, and
// a full-width diff panel filling the remaining height. A one-row
// status strip sits at the very bottom. A 1-cell gap is inserted
// between adjacent panels: between TopLeft and TopRight in full mode,
// between the top row and Files, and between Files and Diff. The
// status row sits flush against Diff and against the terminal edges.
// Below MinCols x MinRows the caller is expected to render a "too
// small" placeholder and ignore the returned rectangles.
package layout

// Layout fractions and thresholds. Exported so they can be tuned
// without restructuring callers.
const (
	TopRowFraction     = 0.20
	LeftColumnFraction = 0.40

	MinTopRows = 5

	SmallModeMinCols = 100

	MinCols = 80
	MinRows = 24

	// FilesPanelMaxRows is the cap on the files panel height (title row
	// plus up to seven file rows). FilesPanelMinRows is the floor so the
	// "Clean merge" / "No changes" placeholder always has room to render.
	FilesPanelMaxRows = 8
	FilesPanelMinRows = 2
)

// Rect is a panel rectangle in cells. Origin is the top-left of the
// terminal; X grows right, Y grows down.
type Rect struct {
	X, Y, W, H int
}

// Layout is the set of panel rectangles for a given terminal size.
// In small mode (terminal width < SmallModeMinCols) TopRight is
// zero-sized: the caller swaps the message content into TopLeft on
// demand. Files and Diff are always full-width.
type Layout struct {
	TopLeft  Rect
	TopRight Rect
	Files    Rect
	Diff     Rect

	// Status is the one-row strip at the very bottom of the terminal.
	Status Rect

	SmallMode bool
	TooSmall  bool
}

// Compute returns the panel rectangles for the given terminal size and
// current file count. The files panel takes min(1+numFiles,
// FilesPanelMaxRows) rows, with a floor of FilesPanelMinRows; the diff
// panel takes the rest of the bottom region. When w < MinCols or
// h < MinRows, Layout.TooSmall is true and the other fields are zeroed.
func Compute(w, h, numFiles int) Layout {
	if w < MinCols || h < MinRows {
		return Layout{TooSmall: true}
	}

	statusRow := Rect{X: 0, Y: h - 1, W: w, H: 1}
	usableH := h - 1

	topH := int(float64(usableH) * TopRowFraction)
	if topH < MinTopRows {
		topH = MinTopRows
	}
	// Two 1-row vertical gaps live inside the usable region: one
	// between the top row and the files panel, one between the files
	// panel and the diff panel.
	bottomH := usableH - topH - 2

	filesH := 1 + numFiles
	if filesH > FilesPanelMaxRows {
		filesH = FilesPanelMaxRows
	}
	if filesH < FilesPanelMinRows {
		filesH = FilesPanelMinRows
	}
	if filesH > bottomH {
		filesH = bottomH
	}
	diffH := bottomH - filesH

	filesY := topH + 1
	diffY := filesY + filesH + 1
	files := Rect{X: 0, Y: filesY, W: w, H: filesH}
	diff := Rect{X: 0, Y: diffY, W: w, H: diffH}

	if w < SmallModeMinCols {
		return Layout{
			TopLeft:   Rect{X: 0, Y: 0, W: w, H: topH},
			Files:     files,
			Diff:      diff,
			Status:    statusRow,
			SmallMode: true,
		}
	}

	// One 1-column horizontal gap between TopLeft and TopRight.
	leftW := int(float64(w) * LeftColumnFraction)
	rightW := w - leftW - 1

	return Layout{
		TopLeft:  Rect{X: 0, Y: 0, W: leftW, H: topH},
		TopRight: Rect{X: leftW + 1, Y: 0, W: rightW, H: topH},
		Files:    files,
		Diff:     diff,
		Status:   statusRow,
	}
}

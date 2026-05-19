// Package layout computes panel rectangles from terminal dimensions.
//
// The layout is a 2x2 grid: top row (log + message) and bottom row
// (files + diff). When the terminal is narrower than SmallModeMinCols
// the layout collapses to a single full-width column per section.
// Below MinCols x MinRows the caller is expected to render a
// "too small" placeholder and ignore the returned rectangles.
package layout

// Layout fractions and thresholds. Exported so they can be tuned
// without restructuring callers.
const (
	TopRowFraction    = 0.20
	LeftColumnFraction = 0.40

	MinTopRows = 5

	SmallModeMinCols = 100

	MinCols = 80
	MinRows = 24
)

// Rect is a panel rectangle in cells. Origin is the top-left of the
// terminal; X grows right, Y grows down.
type Rect struct {
	X, Y, W, H int
}

// Layout is the set of panel rectangles for a given terminal size.
// In small mode (terminal width < SmallModeMinCols) the right
// rectangles are zero-sized: the caller swaps right content into
// the left rectangle on demand.
type Layout struct {
	TopLeft     Rect
	TopRight    Rect
	BottomLeft  Rect
	BottomRight Rect

	// Status is the one-row strip at the very bottom of the terminal.
	Status Rect

	SmallMode bool
	TooSmall  bool
}

// Compute returns the panel rectangles for the given terminal size.
// When w < MinCols or h < MinRows, Layout.TooSmall is true and the
// other fields are zeroed.
func Compute(w, h int) Layout {
	if w < MinCols || h < MinRows {
		return Layout{TooSmall: true}
	}

	// One row at the bottom is reserved for the status strip.
	statusRow := Rect{X: 0, Y: h - 1, W: w, H: 1}
	usableH := h - 1

	topH := int(float64(usableH) * TopRowFraction)
	if topH < MinTopRows {
		topH = MinTopRows
	}
	bottomH := usableH - topH

	if w < SmallModeMinCols {
		return Layout{
			TopLeft:    Rect{X: 0, Y: 0, W: w, H: topH},
			BottomLeft: Rect{X: 0, Y: topH, W: w, H: bottomH},
			Status:     statusRow,
			SmallMode:  true,
		}
	}

	leftW := int(float64(w) * LeftColumnFraction)
	rightW := w - leftW

	return Layout{
		TopLeft:     Rect{X: 0, Y: 0, W: leftW, H: topH},
		TopRight:    Rect{X: leftW, Y: 0, W: rightW, H: topH},
		BottomLeft:  Rect{X: 0, Y: topH, W: leftW, H: bottomH},
		BottomRight: Rect{X: leftW, Y: topH, W: rightW, H: bottomH},
		Status:      statusRow,
	}
}

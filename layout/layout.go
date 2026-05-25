// Package layout computes panel rectangles from terminal dimensions.
//
// The layout has four named regions stacked vertically: a full-width
// commit log at the top, a middle row that splits horizontally into a
// metadata-plus-files left column and a message right column (or
// collapses to a single tabbed region in small mode), a full-width
// diff panel filling the remaining height, and a one-row status strip
// flush against the diff and the terminal edges. A 1-row gap sits
// between the log and the middle row, and another between the middle
// row and the diff; a 1-column gap separates the middle row's left
// and right columns in full mode. Below MinCols x MinRows the caller
// is expected to render a "too small" placeholder and ignore the
// returned rectangles.
package layout

// Layout fractions and thresholds. Exported so they can be tuned
// without restructuring callers.
const (
	// LogContentRows is the number of content rows in the commit-log
	// panel (excluding the panel's own title row).
	LogContentRows = 6
	// MiddleContentRows is the number of content rows in the middle
	// row (excluding the panel's own title row). The message panel
	// occupies the whole middle row on the right; the left column's
	// metadata (3 rows, no header) plus the files panel together
	// occupy MiddleContentRows + 1 rows so the two columns share an
	// identical visible height.
	MiddleContentRows = 14
	// MetadataContentRows is the fixed number of rows the metadata
	// panel occupies in the left column of the middle row. The
	// metadata panel has no title row of its own; its three rows are
	// short-sha+refs, author+date, and the tags summary.
	MetadataContentRows = 3

	// LeftColumnFraction is the proportion of terminal width given to
	// the left column (metadata + files) in the middle row in full
	// mode. The right column (message) takes the remainder minus a
	// one-column gap.
	LeftColumnFraction = 0.60

	// SmallModeMinCols is the minimum terminal width for full mode.
	// Below this the middle row collapses to a single tabbed region.
	SmallModeMinCols = 100

	// MinDiffContentRows is the floor for diff-panel content (i.e.
	// excluding its title row). Used only to derive MinRows.
	MinDiffContentRows = 5

	MinCols = 80
)

// MinRows is the minimum terminal height the layout can render into.
// Derived from the panel row counts so that adjusting a panel
// automatically adjusts the floor: log + log header + middle + middle
// header + diff floor + diff header + status + 2 inter-region gaps.
const MinRows = LogContentRows + 1 + MiddleContentRows + 1 + MinDiffContentRows + 1 + 1 + 2

// Rect is a panel rectangle in cells. Origin is the top-left of the
// terminal; X grows right, Y grows down.
type Rect struct {
	X, Y, W, H int
}

// Layout is the set of panel rectangles for a given terminal size.
//
// In full mode the middle row splits horizontally: Metadata pins the
// top MetadataContentRows of the left column, Files fills the rest of
// the left column, and Message occupies the right column for the full
// middle height. In small mode (terminal width < SmallModeMinCols)
// the three middle-row rectangles share an identical rect — the
// caller picks which to render based on the active middle tab.
type Layout struct {
	Log      Rect
	Metadata Rect
	Files    Rect
	Message  Rect
	Diff     Rect

	// Status is the one-row strip at the very bottom of the terminal.
	Status Rect

	SmallMode bool
	TooSmall  bool
}

// ScrollbarThumb computes the start row and length of a scrollbar thumb
// inside a vertical track that is `height` cells tall. `total` is the
// total content rows the panel could display, `visible` is the number of
// rows currently in view (the viewport height), and `offset` is the
// top-most visible row (0..total-visible).
//
// Returns draw=false when content fits the viewport (`total <= visible`)
// or when `height <= 0` — callers should skip drawing the scrollbar and
// reclaim the column for body content. When draw=true, the returned
// `start` is in [0, height-length] and `length` is at least 1, so the
// thumb is always visible.
//
// The thumb's length is proportional to the visible fraction of content
// (`height * visible / total`, floored to 1). The thumb's start moves
// linearly from 0 (offset==0) to height-length (offset==total-visible).
// Out-of-range offsets are clamped.
func ScrollbarThumb(total, visible, offset, height int) (start, length int, draw bool) {
	if height <= 0 || visible <= 0 || total <= visible {
		return 0, 0, false
	}

	maxOffset := total - visible
	if offset < 0 {
		offset = 0
	}
	if offset > maxOffset {
		offset = maxOffset
	}

	length = height * visible / total
	if length < 1 {
		length = 1
	}
	if length > height {
		length = height
	}

	maxStart := height - length
	if maxOffset == 0 || maxStart <= 0 {
		start = 0
	} else {
		start = maxStart * offset / maxOffset
	}
	if start < 0 {
		start = 0
	}
	if start > maxStart {
		start = maxStart
	}

	return start, length, true
}

// Compute returns the panel rectangles for the given terminal size.
// Equivalent to ComputeWith(w, h, -1): the middle row uses its full
// MiddleContentRows + 1 height regardless of how many files are
// visible.
func Compute(w, h int) Layout {
	return ComputeWith(w, h, -1)
}

// ComputeWith returns the panel rectangles for the given terminal
// size, shrinking the middle row to fit `visibleFiles` rows in the
// files panel when that is smaller than the default. Pass
// visibleFiles < 0 to disable shrinking (full middle height). In
// small mode the middle row is a single tabbed region shared by all
// middle tabs, so shrinking is not applied there.
//
// The log panel is pinned at LogContentRows + 1 rows; the middle row
// is at most MiddleContentRows + 1 rows; the diff panel fills the
// remaining height above the one-row status strip. When w < MinCols
// or h < MinRows, Layout.TooSmall is true and the other fields are
// zeroed.
func ComputeWith(w, h, visibleFiles int) Layout {
	if w < MinCols || h < MinRows {
		return Layout{TooSmall: true}
	}

	statusRow := Rect{X: 0, Y: h - 1, W: w, H: 1}
	usableH := h - 1

	logH := LogContentRows + 1
	middleH := MiddleContentRows + 1
	if visibleFiles >= 0 && w >= SmallModeMinCols {
		// Files panel = 1 title row + content rows. Cap content at
		// the default (MiddleContentRows + 1 - MetadataContentRows -
		// 1) and floor at 1 so the panel always has at least one
		// content row to show "no matches" / a single entry.
		maxContent := MiddleContentRows + 1 - MetadataContentRows - 1
		filesContent := visibleFiles
		if filesContent < 1 {
			filesContent = 1
		}
		if filesContent > maxContent {
			filesContent = maxContent
		}
		middleH = MetadataContentRows + filesContent + 1
	}
	// Two 1-row vertical gaps inside the usable region: one between
	// the log and the middle row, one between the middle row and the
	// diff.
	diffH := usableH - logH - middleH - 2

	logRect := Rect{X: 0, Y: 0, W: w, H: logH}
	middleY := logH + 1
	diffY := middleY + middleH + 1
	diffRect := Rect{X: 0, Y: diffY, W: w, H: diffH}

	if w < SmallModeMinCols {
		middle := Rect{X: 0, Y: middleY, W: w, H: middleH}
		return Layout{
			Log:       logRect,
			Metadata:  middle,
			Files:     middle,
			Message:   middle,
			Diff:      diffRect,
			Status:    statusRow,
			SmallMode: true,
		}
	}

	// One 1-column horizontal gap between the left column and the
	// message panel.
	leftW := int(float64(w) * LeftColumnFraction)
	rightW := w - leftW - 1

	metadataRect := Rect{X: 0, Y: middleY, W: leftW, H: MetadataContentRows}
	filesRect := Rect{X: 0, Y: middleY + MetadataContentRows, W: leftW, H: middleH - MetadataContentRows}
	messageRect := Rect{X: leftW + 1, Y: middleY, W: rightW, H: middleH}

	return Layout{
		Log:      logRect,
		Metadata: metadataRect,
		Files:    filesRect,
		Message:  messageRect,
		Diff:     diffRect,
		Status:   statusRow,
	}
}

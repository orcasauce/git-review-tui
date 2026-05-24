// Package hunkstate tracks a per-(commit, file) "active hunk" index for
// the diff panel. It is TUI-agnostic: it owns no rendering, no key
// bindings, and no scroll math. Callers ask for the active index when
// switching to a (sha, path) pair, record a new index when the user
// jumps between hunks, and advance an index by direction with wrap.
package hunkstate

// NoActiveHunk is the sentinel returned when there is no active hunk
// (typically because the file has zero hunks).
const NoActiveHunk = -1

// Tracker holds the active hunk index for each (sha, path) pair seen
// so far. The zero value is not ready for use; call New.
type Tracker struct {
	indices map[string]int
}

// New constructs an empty Tracker.
func New() *Tracker {
	return &Tracker{indices: map[string]int{}}
}

// Get returns the active hunk index for (sha, path), clamped to a valid
// index given the file's current total hunk count. Pairs that have
// never been recorded default to 0. Returns NoActiveHunk when total is
// zero.
func (t *Tracker) Get(sha, path string, total int) int {
	if total <= 0 {
		return NoActiveHunk
	}
	idx, ok := t.indices[key(sha, path)]
	if !ok {
		return 0
	}
	if idx < 0 {
		return 0
	}
	if idx >= total {
		return total - 1
	}
	return idx
}

// Set records the active hunk index for (sha, path). Callers pass the
// index they have already validated; Set does no clamping of its own.
func (t *Tracker) Set(sha, path string, idx int) {
	t.indices[key(sha, path)] = idx
}

// Kind distinguishes the two shapes of AdvancePlan returned by
// PlanAdvance: a within-file step or a cross-file step.
type Kind int

const (
	// WithinFile means the caller should set its active hunk index to
	// AdvancePlan.NewHunkIdx without changing the selected file.
	WithinFile Kind = iota
	// CrossFile means the caller should switch file selection to
	// AdvancePlan.NewFileIdx and, once the destination file's diff
	// loads, land on the hunk indicated by AdvancePlan.Position.
	CrossFile
)

// Position is which hunk the caller should land on after a CrossFile
// step: the first hunk for forward navigation, the last for backward.
type Position int

const (
	// First lands on hunk index 0 of the destination file.
	First Position = iota
	// Last lands on the destination file's final hunk
	// (total - 1).
	Last
)

// AdvancePlan is the result of PlanAdvance. When Kind is WithinFile
// only NewHunkIdx is meaningful; when Kind is CrossFile only
// NewFileIdx and Position are meaningful.
type AdvancePlan struct {
	Kind       Kind
	NewHunkIdx int
	NewFileIdx int
	Position   Position
}

// PlanAdvance describes the next n/N navigation step. It is pure: no
// I/O, no async coordination, no awareness of binaries, filters, or
// the model. Callers pass the *visible* file index and count (already
// filter-resolved) and receive back either a WithinFile step (caller
// advances activeHunk in place) or a CrossFile step (caller switches
// file selection and applies the indicated landing position when the
// new diff arrives).
//
// dir is +1 for forward (n) and -1 for backward (N).
//
// The single-file case (numFiles == 1) is not specially handled:
// PlanAdvance returns a CrossFile result whose NewFileIdx wraps to
// the same file. The caller still goes through the file-switch path,
// naturally producing same-file wrap behavior.
//
// A negative currentHunkIdx is treated as 0 (matching Advance), so a
// NoActiveHunk input with positive currentHunkTotal does not violate
// the contract — though in practice the model never passes that
// combination.
func PlanAdvance(currentFileIdx, currentHunkIdx, currentHunkTotal, numFiles, dir int) AdvancePlan {
	if !atBoundary(currentHunkIdx, currentHunkTotal, dir) {
		return AdvancePlan{
			Kind:       WithinFile,
			NewHunkIdx: Advance(currentHunkIdx, currentHunkTotal, dir),
		}
	}
	pos := First
	if dir < 0 {
		pos = Last
	}
	newFile := 0
	if numFiles > 0 {
		newFile = ((currentFileIdx+dir)%numFiles + numFiles) % numFiles
	}
	return AdvancePlan{
		Kind:       CrossFile,
		NewFileIdx: newFile,
		Position:   pos,
	}
}

func atBoundary(currentHunkIdx, currentHunkTotal, dir int) bool {
	if currentHunkTotal <= 0 {
		return true
	}
	ch := currentHunkIdx
	if ch < 0 {
		ch = 0
	}
	if dir > 0 && ch == currentHunkTotal-1 {
		return true
	}
	if dir < 0 && ch == 0 {
		return true
	}
	return false
}

// Advance returns the new active hunk index after stepping `dir` from
// `current`, wrapping at both ends. Returns NoActiveHunk when total is
// zero. A negative `current` is treated as 0 (i.e. the first hunk is
// the starting point).
func Advance(current, total, dir int) int {
	if total <= 0 {
		return NoActiveHunk
	}
	if current < 0 {
		current = 0
	}
	if current >= total {
		current = total - 1
	}
	n := (current+dir)%total + total
	return n % total
}

func key(sha, path string) string {
	return sha + "\x00" + path
}

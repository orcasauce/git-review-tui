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

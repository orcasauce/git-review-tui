// Package revertstate owns the per-hunk revert-mark map for the
// hunk-revert feature (PRD: prd-hunk-revert.md). It is TUI-agnostic:
// no rendering, no key bindings.
//
// Marks are keyed by (SHA, FileID, hunkIndex) and carry a canonical
// HunkHash so that the post-rebase adoption walk (slice 13) can re-
// attach marks onto rewritten history by content match. The FileID is
// minted by the fileid package; callers resolve the file path to a
// FileID before invoking the tracker.
package revertstate

import "github.com/orcasauce/git-review-tui/fileid"

// HunkMark is the value stored per flagged hunk. HunkHash is the
// canonical content fingerprint produced by hunkpatch.Hash and is used
// by BuildAdoptionTable / Adopt to match marks onto rewritten history
// after a rebase.
type HunkMark struct {
	HunkHash string
}

// AdoptionKey identifies a candidate hunk in the post-rebase walk: the
// FileID lookup means identical hunks in two different files do not
// cross-adopt, and the HunkHash means a moved-but-unchanged hunk still
// finds its mark.
type AdoptionKey struct {
	ID       fileid.FileID
	HunkHash string
}

// Tracker holds the set of revert-flagged hunks across all visited
// (commit, file) pairs in the current session. The zero value is not
// ready for use; call New.
type Tracker struct {
	marks map[key]HunkMark
}

type key struct {
	sha string
	id  fileid.FileID
	idx int
}

// New constructs an empty Tracker.
func New() *Tracker {
	return &Tracker{marks: map[key]HunkMark{}}
}

// Toggle flips the revert mark on hunk idx of (sha, id). When the mark
// is added the supplied hash is stored as the mark's HunkHash; when it
// is removed the hash argument is ignored. Returns the post-toggle
// state: true if the hunk is now flagged, false if it was just
// un-flagged.
func (t *Tracker) Toggle(sha string, id fileid.FileID, idx int, hash string) bool {
	k := key{sha, id, idx}
	if _, ok := t.marks[k]; ok {
		delete(t.marks, k)
		return false
	}
	t.marks[k] = HunkMark{HunkHash: hash}
	return true
}

// IsFlagged reports whether hunk idx of (sha, id) is currently flagged
// for revert.
func (t *Tracker) IsFlagged(sha string, id fileid.FileID, idx int) bool {
	_, ok := t.marks[key{sha, id, idx}]
	return ok
}

// HashFor returns the stored HunkHash for the mark at (sha, id, idx),
// or the empty string when no mark is present.
func (t *Tracker) HashFor(sha string, id fileid.FileID, idx int) string {
	return t.marks[key{sha, id, idx}].HunkHash
}

// MarksForFile returns the sorted hunk indices flagged on (sha, id).
// The returned slice is freshly allocated; callers may mutate it.
func (t *Tracker) MarksForFile(sha string, id fileid.FileID) []int {
	var out []int
	for k := range t.marks {
		if k.sha == sha && k.id == id {
			out = append(out, k.idx)
		}
	}
	sortInts(out)
	return out
}

// MarksForCommit returns the total count of hunks flagged across all
// files of the given sha. Used by the log view (slice 08) to decide
// whether a commit shows `*` or `D` in its action column.
func (t *Tracker) MarksForCommit(sha string) int {
	n := 0
	for k := range t.marks {
		if k.sha == sha {
			n++
		}
	}
	return n
}

// CountForFile returns the number of hunks flagged on (sha, id). Used
// by the file list (slice 08) to compare against the file's total hunk
// count and decide whether the row should be dimmed.
func (t *Tracker) CountForFile(sha string, id fileid.FileID) int {
	n := 0
	for k := range t.marks {
		if k.sha == sha && k.id == id {
			n++
		}
	}
	return n
}

// BuildAdoptionTable returns the (FileID, HunkHash) → count multiset
// of currently-tracked marks. Post-rebase adoption (slice 13) walks
// the rewritten history against this table, decrementing counts as
// matches are found and discarding any leftovers. Marks with an empty
// HunkHash are skipped — they have no content fingerprint and so
// cannot be adopted.
func (t *Tracker) BuildAdoptionTable() map[AdoptionKey]int {
	out := map[AdoptionKey]int{}
	for k, v := range t.marks {
		if v.HunkHash == "" {
			continue
		}
		out[AdoptionKey{ID: k.id, HunkHash: v.HunkHash}]++
	}
	return out
}

// Adopt re-attaches a mark onto rewritten history when the table has
// remaining capacity for (id, hash). On a hit the table's count is
// decremented and a new mark is written at (newSHA, id, idx) with the
// supplied hash. Returns true when adoption succeeded, false when the
// table had no remaining count for this key (the caller treats this as
// a discard).
func (t *Tracker) Adopt(table map[AdoptionKey]int, newSHA string, id fileid.FileID, idx int, hash string) bool {
	ak := AdoptionKey{ID: id, HunkHash: hash}
	n, ok := table[ak]
	if !ok || n <= 0 {
		return false
	}
	table[ak] = n - 1
	t.marks[key{newSHA, id, idx}] = HunkMark{HunkHash: hash}
	return true
}

// ClearSHA removes every mark recorded against sha and returns the
// number of marks removed. Used by the post-rebase success path to
// drop marks on the processed cursor commit and on each co-processed
// drop commit before BuildAdoptionTable scans the remaining set.
func (t *Tracker) ClearSHA(sha string) int {
	n := 0
	for k := range t.marks {
		if k.sha == sha {
			delete(t.marks, k)
			n++
		}
	}
	return n
}

// Snapshot returns an opaque copy of the current mark set. Used by the
// ctrl-s rebase flow to restore marks on cancel/error.
func (t *Tracker) Snapshot() Snapshot {
	cp := make(map[key]HunkMark, len(t.marks))
	for k, v := range t.marks {
		cp[k] = v
	}
	return Snapshot{marks: cp}
}

// Restore replaces the current mark set with the given snapshot.
func (t *Tracker) Restore(s Snapshot) {
	cp := make(map[key]HunkMark, len(s.marks))
	for k, v := range s.marks {
		cp[k] = v
	}
	t.marks = cp
}

// Snapshot is an opaque copy of a Tracker's mark set, taken via
// Snapshot and replayed via Restore. The zero value is an empty
// snapshot (replay leaves no marks).
type Snapshot struct {
	marks map[key]HunkMark
}

func sortInts(s []int) {
	// Small slice insertion sort — typical mark counts per file are
	// in the single digits.
	for i := 1; i < len(s); i++ {
		v := s[i]
		j := i - 1
		for j >= 0 && s[j] > v {
			s[j+1] = s[j]
			j--
		}
		s[j+1] = v
	}
}

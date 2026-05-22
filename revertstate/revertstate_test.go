package revertstate

import (
	"reflect"
	"testing"

	"github.com/orcasauce/git-review-tui/fileid"
)

// fid is a small helper for tests: allocate a FileID off the package
// without needing a real registry. Distinct positive values stand in
// for distinct files; identical values stand in for the same file.
func fid(n uint64) fileid.FileID { return fileid.FileID(n) }

func TestToggle_AddThenRemove(t *testing.T) {
	tr := New()
	if got := tr.Toggle("sha", fid(1), 0, "h0"); got != true {
		t.Fatalf("first Toggle = %v, want true (added)", got)
	}
	if !tr.IsFlagged("sha", fid(1), 0) {
		t.Errorf("IsFlagged after add = false, want true")
	}
	if got := tr.HashFor("sha", fid(1), 0); got != "h0" {
		t.Errorf("HashFor = %q, want %q", got, "h0")
	}
	if got := tr.Toggle("sha", fid(1), 0, "ignored"); got != false {
		t.Fatalf("second Toggle = %v, want false (removed)", got)
	}
	if tr.IsFlagged("sha", fid(1), 0) {
		t.Errorf("IsFlagged after remove = true, want false")
	}
	if got := tr.HashFor("sha", fid(1), 0); got != "" {
		t.Errorf("HashFor after remove = %q, want empty", got)
	}
}

func TestToggle_IndependentKeys(t *testing.T) {
	tr := New()
	tr.Toggle("a", fid(1), 0, "h")
	tr.Toggle("a", fid(1), 1, "h")
	tr.Toggle("a", fid(2), 0, "h")
	tr.Toggle("b", fid(1), 0, "h")

	cases := []struct {
		sha  string
		id   fileid.FileID
		idx  int
		want bool
	}{
		{"a", fid(1), 0, true},
		{"a", fid(1), 1, true},
		{"a", fid(1), 2, false},
		{"a", fid(2), 0, true},
		{"a", fid(2), 1, false},
		{"b", fid(1), 0, true},
		{"b", fid(2), 0, false},
	}
	for _, c := range cases {
		if got := tr.IsFlagged(c.sha, c.id, c.idx); got != c.want {
			t.Errorf("IsFlagged(%q,%d,%d) = %v, want %v",
				c.sha, c.id, c.idx, got, c.want)
		}
	}
}

func TestMarksForFile_SortedAndScopedToFile(t *testing.T) {
	tr := New()
	tr.Toggle("a", fid(1), 2, "h")
	tr.Toggle("a", fid(1), 0, "h")
	tr.Toggle("a", fid(1), 5, "h")
	tr.Toggle("a", fid(2), 0, "h")
	tr.Toggle("b", fid(1), 9, "h")

	got := tr.MarksForFile("a", fid(1))
	want := []int{0, 2, 5}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MarksForFile = %v, want %v", got, want)
	}
}

func TestMarksForFile_EmptyForUnseen(t *testing.T) {
	tr := New()
	if got := tr.MarksForFile("nope", fid(99)); got != nil {
		t.Errorf("MarksForFile unseen = %v, want nil", got)
	}
}

func TestMarksForCommit_CountsAcrossFiles(t *testing.T) {
	tr := New()
	tr.Toggle("a", fid(1), 0, "h")
	tr.Toggle("a", fid(1), 1, "h")
	tr.Toggle("a", fid(2), 0, "h")
	tr.Toggle("b", fid(1), 0, "h")

	if got := tr.MarksForCommit("a"); got != 3 {
		t.Errorf("MarksForCommit(a) = %d, want 3", got)
	}
	if got := tr.MarksForCommit("b"); got != 1 {
		t.Errorf("MarksForCommit(b) = %d, want 1", got)
	}
	if got := tr.MarksForCommit("c"); got != 0 {
		t.Errorf("MarksForCommit(c) = %d, want 0", got)
	}
}

func TestMarksForCommit_DecreasesAfterToggleOff(t *testing.T) {
	tr := New()
	tr.Toggle("a", fid(1), 0, "h")
	tr.Toggle("a", fid(1), 1, "h")
	tr.Toggle("a", fid(1), 0, "h") // toggle 0 back off

	if got := tr.MarksForCommit("a"); got != 1 {
		t.Errorf("MarksForCommit after toggle-off = %d, want 1", got)
	}
}

func TestSnapshotRestore_RoundTrip(t *testing.T) {
	tr := New()
	tr.Toggle("a", fid(1), 0, "h0")
	tr.Toggle("a", fid(1), 1, "h1")

	snap := tr.Snapshot()

	tr.Toggle("a", fid(1), 0, "") // remove
	tr.Toggle("b", fid(2), 7, "x")

	if tr.IsFlagged("a", fid(1), 0) {
		t.Errorf("pre-restore: expected (a,1,0) unflagged")
	}
	if !tr.IsFlagged("b", fid(2), 7) {
		t.Errorf("pre-restore: expected (b,2,7) flagged")
	}

	tr.Restore(snap)

	if !tr.IsFlagged("a", fid(1), 0) {
		t.Errorf("post-restore: expected (a,1,0) flagged again")
	}
	if got := tr.HashFor("a", fid(1), 0); got != "h0" {
		t.Errorf("post-restore HashFor = %q, want %q", got, "h0")
	}
	if !tr.IsFlagged("a", fid(1), 1) {
		t.Errorf("post-restore: expected (a,1,1) still flagged")
	}
	if tr.IsFlagged("b", fid(2), 7) {
		t.Errorf("post-restore: expected (b,2,7) to be gone")
	}
}

func TestSnapshot_IsolatedFromTracker(t *testing.T) {
	tr := New()
	tr.Toggle("a", fid(1), 0, "h")
	snap := tr.Snapshot()
	tr.Toggle("a", fid(1), 0, "") // remove after snapshot

	tr.Restore(snap)
	if !tr.IsFlagged("a", fid(1), 0) {
		t.Errorf("snapshot did not preserve mark across an interleaved remove")
	}
}

func TestCountForFile_VsTotal(t *testing.T) {
	cases := []struct {
		name  string
		flags []int
		total int
		want  int
		full  bool
	}{
		{"empty file, zero total", nil, 0, 0, false},
		{"empty file, three hunks", nil, 3, 0, false},
		{"partial: one of three", []int{1}, 3, 1, false},
		{"full: every hunk", []int{0, 1, 2}, 3, 3, true},
		{"single-hunk file fully flagged", []int{0}, 1, 1, true},
		{"overflow: stray mark beyond total", []int{0, 1, 5}, 3, 3, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := New()
			for _, idx := range c.flags {
				tr.Toggle("s", fid(1), idx, "h")
			}
			got := tr.CountForFile("s", fid(1))
			if got != c.want {
				t.Errorf("CountForFile = %d, want %d", got, c.want)
			}
			full := c.total > 0 && got == c.total
			if full != c.full {
				t.Errorf("full = %v (count=%d total=%d), want %v",
					full, got, c.total, c.full)
			}
		})
	}
}

func TestCountForFile_ScopedToFile(t *testing.T) {
	tr := New()
	tr.Toggle("a", fid(1), 0, "h")
	tr.Toggle("a", fid(1), 1, "h")
	tr.Toggle("a", fid(2), 0, "h")
	tr.Toggle("b", fid(1), 0, "h")
	if got := tr.CountForFile("a", fid(1)); got != 2 {
		t.Errorf("CountForFile(a,1) = %d, want 2", got)
	}
	if got := tr.CountForFile("a", fid(2)); got != 1 {
		t.Errorf("CountForFile(a,2) = %d, want 1", got)
	}
	if got := tr.CountForFile("b", fid(1)); got != 1 {
		t.Errorf("CountForFile(b,1) = %d, want 1", got)
	}
	if got := tr.CountForFile("none", fid(99)); got != 0 {
		t.Errorf("CountForFile(none,99) = %d, want 0", got)
	}
}

func TestCountForFile_DecreasesAfterToggleOff(t *testing.T) {
	tr := New()
	tr.Toggle("a", fid(1), 0, "h")
	tr.Toggle("a", fid(1), 1, "h")
	tr.Toggle("a", fid(1), 0, "h")
	if got := tr.CountForFile("a", fid(1)); got != 1 {
		t.Errorf("CountForFile after toggle-off = %d, want 1", got)
	}
}

func TestRestore_ZeroSnapshotClearsMarks(t *testing.T) {
	tr := New()
	tr.Toggle("a", fid(1), 0, "h")
	tr.Restore(Snapshot{})
	if tr.IsFlagged("a", fid(1), 0) {
		t.Errorf("zero-value Snapshot did not clear marks")
	}
	if got := tr.MarksForCommit("a"); got != 0 {
		t.Errorf("MarksForCommit after zero-restore = %d, want 0", got)
	}
}

func TestBuildAdoptionTable_CountsByIDAndHash(t *testing.T) {
	tr := New()
	// Two marks for (id=1, hash=h1): same hunk content, two SHAs.
	tr.Toggle("a", fid(1), 0, "h1")
	tr.Toggle("b", fid(1), 0, "h1")
	// One mark for (id=1, hash=h2): different hunk content, same file.
	tr.Toggle("a", fid(1), 1, "h2")
	// One mark for (id=2, hash=h1): identical hash but different file.
	tr.Toggle("a", fid(2), 0, "h1")

	tbl := tr.BuildAdoptionTable()
	want := map[AdoptionKey]int{
		{ID: fid(1), HunkHash: "h1"}: 2,
		{ID: fid(1), HunkHash: "h2"}: 1,
		{ID: fid(2), HunkHash: "h1"}: 1,
	}
	if !reflect.DeepEqual(tbl, want) {
		t.Errorf("BuildAdoptionTable = %v, want %v", tbl, want)
	}
}

func TestBuildAdoptionTable_SkipsEmptyHash(t *testing.T) {
	tr := New()
	tr.Toggle("a", fid(1), 0, "")
	tr.Toggle("a", fid(1), 1, "h")
	tbl := tr.BuildAdoptionTable()
	if got, want := len(tbl), 1; got != want {
		t.Fatalf("len(tbl) = %d, want %d (empty-hash entries should be skipped)", got, want)
	}
	if tbl[AdoptionKey{ID: fid(1), HunkHash: "h"}] != 1 {
		t.Errorf("non-empty entry missing from table: %v", tbl)
	}
}

func TestAdopt_DecrementsAndInserts(t *testing.T) {
	tr := New()
	tbl := map[AdoptionKey]int{
		{ID: fid(1), HunkHash: "h1"}: 2,
	}

	if !tr.Adopt(tbl, "new", fid(1), 3, "h1") {
		t.Fatalf("first Adopt = false, want true")
	}
	if !tr.IsFlagged("new", fid(1), 3) {
		t.Errorf("Adopt did not write mark at (new,1,3)")
	}
	if got := tbl[AdoptionKey{ID: fid(1), HunkHash: "h1"}]; got != 1 {
		t.Errorf("table count after first Adopt = %d, want 1", got)
	}

	if !tr.Adopt(tbl, "new", fid(1), 7, "h1") {
		t.Fatalf("second Adopt = false, want true")
	}
	if got := tbl[AdoptionKey{ID: fid(1), HunkHash: "h1"}]; got != 0 {
		t.Errorf("table count after second Adopt = %d, want 0", got)
	}

	// Third attempt has no remaining count → discard, no mark written.
	if tr.Adopt(tbl, "new", fid(1), 9, "h1") {
		t.Errorf("third Adopt = true, want false (count exhausted)")
	}
	if tr.IsFlagged("new", fid(1), 9) {
		t.Errorf("Adopt should not have written (new,1,9) when count was 0")
	}
}

func TestAdopt_MissingKeyIsDiscard(t *testing.T) {
	tr := New()
	tbl := map[AdoptionKey]int{
		{ID: fid(1), HunkHash: "h1"}: 1,
	}
	// Wrong hash → miss; wrong id → miss.
	if tr.Adopt(tbl, "new", fid(1), 0, "nope") {
		t.Errorf("Adopt with unknown hash returned true")
	}
	if tr.Adopt(tbl, "new", fid(2), 0, "h1") {
		t.Errorf("Adopt with unknown id returned true")
	}
	if tbl[AdoptionKey{ID: fid(1), HunkHash: "h1"}] != 1 {
		t.Errorf("misses should not decrement the table")
	}
}

func TestClearSHA_RemovesEveryMarkOnSHA(t *testing.T) {
	tr := New()
	tr.Toggle("a", fid(1), 0, "h0")
	tr.Toggle("a", fid(2), 0, "h1")
	tr.Toggle("a", fid(2), 1, "h2")
	tr.Toggle("b", fid(1), 0, "h0") // same key shape on a different sha — must survive.

	if got, want := tr.ClearSHA("a"), 3; got != want {
		t.Fatalf("ClearSHA returned %d, want %d", got, want)
	}
	if got := tr.MarksForCommit("a"); got != 0 {
		t.Errorf("MarksForCommit(a) after ClearSHA = %d, want 0", got)
	}
	if !tr.IsFlagged("b", fid(1), 0) {
		t.Errorf("ClearSHA(a) wrongly cleared a mark on sha b")
	}
}

func TestClearSHA_UnknownSHAIsNoop(t *testing.T) {
	tr := New()
	tr.Toggle("a", fid(1), 0, "h0")
	if got, want := tr.ClearSHA("nope"), 0; got != want {
		t.Errorf("ClearSHA(unknown) = %d, want %d", got, want)
	}
	if !tr.IsFlagged("a", fid(1), 0) {
		t.Errorf("ClearSHA(unknown) wrongly affected existing marks")
	}
}

// TestAdopt_WalkMultiCommitMultiFile mirrors the main.go adoption walk:
// iterate post-rebase commits oldest-first, attempt to Adopt each
// candidate hunk against the table built from the pre-rebase marks.
// Exercises three PRD invariants together — multiset-N adopts up to N
// times, identical hunks in different files don't cross-adopt, and a
// no-match candidate is silently discarded without disturbing the
// table.
func TestAdopt_WalkMultiCommitMultiFile(t *testing.T) {
	tr := New()
	// Pre-rebase marks: two on (id=1, h1), one on (id=2, h1), one
	// orphan (id=3, h9) that won't match anything in the new history.
	tr.Toggle("oldA", fid(1), 0, "h1")
	tr.Toggle("oldB", fid(1), 0, "h1")
	tr.Toggle("oldA", fid(2), 0, "h1")
	tr.Toggle("oldC", fid(3), 0, "h9")
	tbl := tr.BuildAdoptionTable()
	// Caller resets the tracker before the walk, matching what
	// handleRevertDone does on success.
	tr = New()

	type cand struct {
		sha  string
		id   fileid.FileID
		idx  int
		hash string
	}
	// Walk in oldest-first order. The new history has three commits;
	// commit "X" carries two id=1 hunks (one adopt should consume each
	// count for id=1/h1), commit "Y" has one id=2 hunk (consumes the
	// remaining id=2/h1 count), commit "Z" has one id=1 hunk whose
	// table count is already exhausted, so it's a discard.
	walk := []cand{
		{"X", fid(1), 0, "h1"},
		{"X", fid(1), 3, "h1"},
		{"Y", fid(2), 0, "h1"},
		{"Z", fid(1), 7, "h1"},
	}
	adopted := 0
	for _, c := range walk {
		if tr.Adopt(tbl, c.sha, c.id, c.idx, c.hash) {
			adopted++
		}
	}
	if adopted != 3 {
		t.Errorf("adopted = %d, want 3", adopted)
	}
	// Discards = total marks (4) - adopted (3) = 1, matching the
	// orphan (id=3, h9) entry left in the table.
	leftover := 0
	for _, n := range tbl {
		leftover += n
	}
	if leftover != 1 {
		t.Errorf("leftover table counts = %d, want 1", leftover)
	}
	// The adopted marks are keyed by the new SHAs.
	if !tr.IsFlagged("X", fid(1), 0) || !tr.IsFlagged("X", fid(1), 3) || !tr.IsFlagged("Y", fid(2), 0) {
		t.Errorf("adopted marks missing from tracker: %+v", tr.marks)
	}
	if tr.IsFlagged("Z", fid(1), 7) {
		t.Errorf("Z's id=1 hunk should have been discarded (table exhausted)")
	}
}

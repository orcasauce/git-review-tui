package hunkstate

import "testing"

func TestGet_UnseenPairDefaultsToZero(t *testing.T) {
	tr := New()
	if got := tr.Get("sha", "path", 3); got != 0 {
		t.Errorf("Get unseen = %d, want 0", got)
	}
}

func TestGet_TotalZeroReturnsNoActiveHunk(t *testing.T) {
	tr := New()
	if got := tr.Get("sha", "path", 0); got != NoActiveHunk {
		t.Errorf("Get total=0 = %d, want NoActiveHunk (%d)", got, NoActiveHunk)
	}
}

func TestSetThenGet_ReturnsStoredIndex(t *testing.T) {
	tr := New()
	tr.Set("sha", "path", 2)
	if got := tr.Get("sha", "path", 5); got != 2 {
		t.Errorf("Get after Set(2) = %d, want 2", got)
	}
}

func TestGet_ClampsWhenTotalShrunk(t *testing.T) {
	tr := New()
	tr.Set("sha", "path", 7)
	if got := tr.Get("sha", "path", 3); got != 2 {
		t.Errorf("Get clamped = %d, want 2 (last valid index for total=3)", got)
	}
}

func TestGet_PairsAreIndependent(t *testing.T) {
	tr := New()
	tr.Set("a", "f", 1)
	tr.Set("b", "f", 2)
	tr.Set("a", "g", 3)
	cases := []struct {
		sha, path string
		total     int
		want      int
	}{
		{"a", "f", 5, 1},
		{"b", "f", 5, 2},
		{"a", "g", 5, 3},
		{"b", "g", 5, 0}, // unseen
	}
	for _, c := range cases {
		if got := tr.Get(c.sha, c.path, c.total); got != c.want {
			t.Errorf("Get(%q,%q,%d) = %d, want %d", c.sha, c.path, c.total, got, c.want)
		}
	}
}

func TestAdvance_ForwardWrapsPastLast(t *testing.T) {
	if got := Advance(2, 3, 1); got != 0 {
		t.Errorf("Advance(2, 3, +1) = %d, want 0", got)
	}
}

func TestAdvance_BackwardWrapsPastFirst(t *testing.T) {
	if got := Advance(0, 3, -1); got != 2 {
		t.Errorf("Advance(0, 3, -1) = %d, want 2", got)
	}
}

func TestAdvance_WithinRange(t *testing.T) {
	cases := []struct {
		current, total, dir, want int
	}{
		{0, 3, 1, 1},
		{1, 3, 1, 2},
		{2, 3, -1, 1},
		{1, 3, -1, 0},
	}
	for _, c := range cases {
		if got := Advance(c.current, c.total, c.dir); got != c.want {
			t.Errorf("Advance(%d, %d, %d) = %d, want %d", c.current, c.total, c.dir, got, c.want)
		}
	}
}

func TestAdvance_TotalZeroReturnsNoActiveHunk(t *testing.T) {
	if got := Advance(0, 0, 1); got != NoActiveHunk {
		t.Errorf("Advance(0, 0, +1) = %d, want NoActiveHunk (%d)", got, NoActiveHunk)
	}
	if got := Advance(0, 0, -1); got != NoActiveHunk {
		t.Errorf("Advance(0, 0, -1) = %d, want NoActiveHunk (%d)", got, NoActiveHunk)
	}
}

func TestAdvance_TotalOneStaysAtZero(t *testing.T) {
	if got := Advance(0, 1, 1); got != 0 {
		t.Errorf("Advance(0, 1, +1) = %d, want 0", got)
	}
	if got := Advance(0, 1, -1); got != 0 {
		t.Errorf("Advance(0, 1, -1) = %d, want 0", got)
	}
}

func TestAdvance_NegativeCurrentTreatedAsZero(t *testing.T) {
	if got := Advance(-1, 3, 1); got != 1 {
		t.Errorf("Advance(-1, 3, +1) = %d, want 1", got)
	}
	if got := Advance(-1, 3, -1); got != 2 {
		t.Errorf("Advance(-1, 3, -1) = %d, want 2 (wrap)", got)
	}
}

func TestPlanAdvance_WithinFileForwardAndBackward(t *testing.T) {
	cases := []struct {
		name              string
		curFile, curHunk  int
		total, numFiles   int
		dir, wantNewHunk  int
	}{
		{"forward middle", 1, 0, 3, 4, +1, 1},
		{"forward middle 2", 1, 1, 3, 4, +1, 2},
		{"backward middle", 1, 2, 3, 4, -1, 1},
		{"backward middle 2", 1, 1, 3, 4, -1, 0},
	}
	for _, c := range cases {
		got := PlanAdvance(c.curFile, c.curHunk, c.total, c.numFiles, c.dir)
		if got.Kind != WithinFile {
			t.Errorf("%s: Kind = %d, want WithinFile", c.name, got.Kind)
		}
		if got.NewHunkIdx != c.wantNewHunk {
			t.Errorf("%s: NewHunkIdx = %d, want %d", c.name, got.NewHunkIdx, c.wantNewHunk)
		}
	}
}

func TestPlanAdvance_BoundaryForwardCrossesAndLandsOnFirst(t *testing.T) {
	got := PlanAdvance(1, 2, 3, 4, +1)
	if got.Kind != CrossFile {
		t.Fatalf("Kind = %d, want CrossFile", got.Kind)
	}
	if got.NewFileIdx != 2 {
		t.Errorf("NewFileIdx = %d, want 2", got.NewFileIdx)
	}
	if got.Position != First {
		t.Errorf("Position = %d, want First", got.Position)
	}
}

func TestPlanAdvance_BoundaryBackwardCrossesAndLandsOnLast(t *testing.T) {
	got := PlanAdvance(2, 0, 3, 4, -1)
	if got.Kind != CrossFile {
		t.Fatalf("Kind = %d, want CrossFile", got.Kind)
	}
	if got.NewFileIdx != 1 {
		t.Errorf("NewFileIdx = %d, want 1", got.NewFileIdx)
	}
	if got.Position != Last {
		t.Errorf("Position = %d, want Last", got.Position)
	}
}

func TestPlanAdvance_ZeroHunkFileCrossesInBothDirections(t *testing.T) {
	fwd := PlanAdvance(1, NoActiveHunk, 0, 3, +1)
	if fwd.Kind != CrossFile || fwd.NewFileIdx != 2 || fwd.Position != First {
		t.Errorf("forward zero-hunk = %+v, want CrossFile NewFileIdx=2 First", fwd)
	}
	bwd := PlanAdvance(1, NoActiveHunk, 0, 3, -1)
	if bwd.Kind != CrossFile || bwd.NewFileIdx != 0 || bwd.Position != Last {
		t.Errorf("backward zero-hunk = %+v, want CrossFile NewFileIdx=0 Last", bwd)
	}
}

func TestPlanAdvance_SingleFileBoundaryWrapsToSameFile(t *testing.T) {
	fwd := PlanAdvance(0, 2, 3, 1, +1)
	if fwd.Kind != CrossFile || fwd.NewFileIdx != 0 || fwd.Position != First {
		t.Errorf("single-file forward = %+v, want CrossFile NewFileIdx=0 First", fwd)
	}
	bwd := PlanAdvance(0, 0, 3, 1, -1)
	if bwd.Kind != CrossFile || bwd.NewFileIdx != 0 || bwd.Position != Last {
		t.Errorf("single-file backward = %+v, want CrossFile NewFileIdx=0 Last", bwd)
	}
}

func TestPlanAdvance_FileWrapForwardFromLastFile(t *testing.T) {
	got := PlanAdvance(2, 1, 2, 3, +1)
	if got.Kind != CrossFile {
		t.Fatalf("Kind = %d, want CrossFile", got.Kind)
	}
	if got.NewFileIdx != 0 {
		t.Errorf("NewFileIdx = %d, want 0", got.NewFileIdx)
	}
}

func TestPlanAdvance_FileWrapBackwardFromFirstFile(t *testing.T) {
	got := PlanAdvance(0, 0, 2, 3, -1)
	if got.Kind != CrossFile {
		t.Fatalf("Kind = %d, want CrossFile", got.Kind)
	}
	if got.NewFileIdx != 2 {
		t.Errorf("NewFileIdx = %d, want 2", got.NewFileIdx)
	}
}

func TestPlanAdvance_NoActiveHunkWithPositiveTotal(t *testing.T) {
	// -1 (NoActiveHunk) is treated as 0. With total=3 forward is
	// within-file (0 → 1); backward is boundary (cross with Last).
	fwd := PlanAdvance(1, NoActiveHunk, 3, 3, +1)
	if fwd.Kind != WithinFile || fwd.NewHunkIdx != 1 {
		t.Errorf("forward NoActiveHunk = %+v, want WithinFile NewHunkIdx=1", fwd)
	}
	bwd := PlanAdvance(1, NoActiveHunk, 3, 3, -1)
	if bwd.Kind != CrossFile || bwd.NewFileIdx != 0 || bwd.Position != Last {
		t.Errorf("backward NoActiveHunk = %+v, want CrossFile NewFileIdx=0 Last", bwd)
	}
}

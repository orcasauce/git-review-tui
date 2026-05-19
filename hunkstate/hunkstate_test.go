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

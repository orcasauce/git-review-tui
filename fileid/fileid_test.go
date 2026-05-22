package fileid

import "testing"

func TestResolve_FirstCallAllocatesStableID(t *testing.T) {
	r := New()
	id1 := r.Resolve("foo.txt")
	if id1 == 0 {
		t.Fatalf("Resolve(%q) = 0, want non-zero", "foo.txt")
	}
	id2 := r.Resolve("foo.txt")
	if id1 != id2 {
		t.Errorf("Resolve(%q) second call = %d, want %d", "foo.txt", id2, id1)
	}
	if got := r.Path(id1); got != "foo.txt" {
		t.Errorf("Path(%d) = %q, want %q", id1, got, "foo.txt")
	}
}

func TestResolve_DistinctPathsDistinctIDs(t *testing.T) {
	r := New()
	a := r.Resolve("a.txt")
	b := r.Resolve("b.txt")
	if a == b {
		t.Errorf("Resolve(a)=%d == Resolve(b)=%d, want distinct", a, b)
	}
}

func TestResolve_EmptyPathIsZero(t *testing.T) {
	r := New()
	if id := r.Resolve(""); id != 0 {
		t.Errorf("Resolve(%q) = %d, want 0", "", id)
	}
}

func TestPath_UnknownIDIsEmpty(t *testing.T) {
	r := New()
	if got := r.Path(42); got != "" {
		t.Errorf("Path(unknown) = %q, want empty", got)
	}
}

func TestApplyRename_PreservesIDAndUpdatesCurrentPath(t *testing.T) {
	r := New()
	id := r.Resolve("foo.txt")
	r.ApplyRename("foo.txt", "bar.txt")

	if got := r.Resolve("bar.txt"); got != id {
		t.Errorf("Resolve(bar) = %d, want %d (same as foo)", got, id)
	}
	if got := r.Resolve("foo.txt"); got != id {
		t.Errorf("Resolve(foo) after rename = %d, want %d (old path still resolves)", got, id)
	}
	if got := r.Path(id); got != "bar.txt" {
		t.Errorf("Path(%d) = %q, want %q", id, got, "bar.txt")
	}
}

func TestApplyRename_AllocatesIfMissing(t *testing.T) {
	r := New()
	r.ApplyRename("foo.txt", "bar.txt")
	id := r.Resolve("bar.txt")
	if id == 0 {
		t.Fatalf("Resolve(bar) after rename of never-seen foo = 0, want allocated")
	}
	if got := r.Resolve("foo.txt"); got != id {
		t.Errorf("Resolve(foo) = %d, want %d", got, id)
	}
}

func TestApplyRename_EmptyOrSelfRenameIsNoop(t *testing.T) {
	r := New()
	id := r.Resolve("foo.txt")
	r.ApplyRename("", "bar.txt")
	r.ApplyRename("foo.txt", "")
	r.ApplyRename("foo.txt", "foo.txt")
	if got := r.Path(id); got != "foo.txt" {
		t.Errorf("Path after no-op renames = %q, want %q", got, "foo.txt")
	}
}

func TestApplyRename_UndoRoundTrip(t *testing.T) {
	r := New()
	id := r.Resolve("foo.txt")
	r.ApplyRename("foo.txt", "bar.txt")
	r.UndoRename("bar.txt", "foo.txt")

	if got := r.Path(id); got != "foo.txt" {
		t.Errorf("Path after round-trip = %q, want %q", got, "foo.txt")
	}
	if got := r.Resolve("foo.txt"); got != id {
		t.Errorf("Resolve(foo) after round-trip = %d, want %d", got, id)
	}
	if _, ok := r.pathToID["bar.txt"]; ok {
		t.Errorf("bar.txt still resolvable after UndoRename")
	}
}

func TestUndoRename_UnknownNewIsNoop(t *testing.T) {
	r := New()
	id := r.Resolve("foo.txt")
	r.UndoRename("never-seen.txt", "foo.txt")
	if got := r.Path(id); got != "foo.txt" {
		t.Errorf("Path = %q, want unchanged %q", got, "foo.txt")
	}
}

func TestSeed_RenameChainCollapsesToOneID(t *testing.T) {
	r := New()
	r.Seed([]Event{
		{Path: "foo"},
		{OldPath: "foo", Path: "bar"},
		{OldPath: "bar", Path: "baz"},
	})

	foo := r.Resolve("foo")
	bar := r.Resolve("bar")
	baz := r.Resolve("baz")
	if foo != bar || bar != baz {
		t.Errorf("rename chain: foo=%d bar=%d baz=%d, want all equal", foo, bar, baz)
	}
	if got := r.Path(foo); got != "baz" {
		t.Errorf("Path(%d) = %q, want %q (most recent)", foo, got, "baz")
	}
}

func TestSeed_RenameCollisionCollapsesIDs(t *testing.T) {
	r := New()
	// Two distinct files (a, b) in different branches of history that
	// both end up named c. They share a FileID after Seed, per the
	// PRD's collision semantics.
	r.Seed([]Event{
		{Path: "a"},
		{Path: "b"},
		{OldPath: "a", Path: "c"},
		{OldPath: "b", Path: "c"},
	})

	a := r.Resolve("a")
	b := r.Resolve("b")
	c := r.Resolve("c")
	if a != c || b != c {
		t.Errorf("collision: a=%d b=%d c=%d, want all equal", a, b, c)
	}
}

func TestSeed_PlainTouchAllocates(t *testing.T) {
	r := New()
	r.Seed([]Event{
		{Path: "x"},
		{Path: "y"},
	})
	x := r.Resolve("x")
	y := r.Resolve("y")
	if x == 0 || y == 0 {
		t.Errorf("plain touches: x=%d y=%d, want both allocated", x, y)
	}
	if x == y {
		t.Errorf("plain touches: x=%d == y=%d, want distinct", x, y)
	}
}

func TestSeed_EmptyEventsSkipped(t *testing.T) {
	r := New()
	r.Seed([]Event{
		{},
		{Path: "z"},
		{},
	})
	if id := r.Resolve("z"); id == 0 {
		t.Errorf("Resolve(z) = 0 after Seed with empties, want allocated")
	}
}

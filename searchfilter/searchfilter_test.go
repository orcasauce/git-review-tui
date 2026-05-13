package searchfilter

import "testing"

func TestScore_EmptyQueryAlwaysMatches(t *testing.T) {
	if s, ok := Score("", "anything"); !ok || s != 0 {
		t.Fatalf("Score(\"\", \"anything\") = (%d, %v); want (0, true)", s, ok)
	}
}

func TestScore_NonMatch(t *testing.T) {
	if _, ok := Score("xyz", "abcdef"); ok {
		t.Fatalf("Score(\"xyz\", \"abcdef\") matched; want no match")
	}
}

func TestScore_CaseInsensitive(t *testing.T) {
	s1, ok1 := Score("Foo", "foobar")
	s2, ok2 := Score("foo", "foobar")
	if !ok1 || !ok2 {
		t.Fatalf("expected both case variants to match; got ok1=%v ok2=%v", ok1, ok2)
	}
	if s1 != s2 {
		t.Fatalf("case variants should score equal; got %d vs %d", s1, s2)
	}
}

func TestScore_ConsecutivePreferredOverGap(t *testing.T) {
	// "foo" appears contiguously in "foobar" but is spread out in
	// "f_o_o_x". The contiguous match must score higher.
	s1, _ := Score("foo", "foobar")
	s2, _ := Score("foo", "f_o_o_x")
	if s1 <= s2 {
		t.Fatalf("contiguous should beat spread: %d (contiguous) vs %d (spread)", s1, s2)
	}
}

func TestScore_EarlierMatchPreferred(t *testing.T) {
	// Identical match patterns but at different positions.
	s1, _ := Score("foo", "foo_bar_baz")
	s2, _ := Score("foo", "bar_baz_foo")
	if s1 <= s2 {
		t.Fatalf("earlier match should score higher: %d vs %d", s1, s2)
	}
}

func TestScore_WordBoundaryBonus(t *testing.T) {
	// "bar" right after a separator beats "bar" embedded mid-word.
	s1, _ := Score("bar", "foo_barbaz")
	s2, _ := Score("bar", "foobarbaz")
	if s1 <= s2 {
		t.Fatalf("word-boundary match should beat mid-word: %d vs %d", s1, s2)
	}
}

func TestRank_EmptyQueryReturnsNil(t *testing.T) {
	got := Rank("", []string{"a", "b"})
	if got != nil {
		t.Fatalf("empty query should return nil; got %v", got)
	}
}

func TestRank_NoMatchesReturnsEmpty(t *testing.T) {
	got := Rank("zzz", []string{"abc", "def"})
	if len(got) != 0 {
		t.Fatalf("expected zero matches; got %v", got)
	}
}

func TestRank_OrderAndIndices(t *testing.T) {
	items := []string{
		"refactor: clean up auth helpers", // 0
		"fix: revoke old auth tokens",     // 1
		"docs: rewrite contributing.md",   // 2 -- no 'a' anywhere; non-match
		"feat: add auth/oauth support",    // 3 -- best (boundary + contiguous early)
		"chore: bump dependencies",        // 4 -- no match
	}
	got := Rank("auth", items)
	if len(got) != 3 {
		t.Fatalf("expected 3 matches; got %d (%v)", len(got), got)
	}
	// Best match should be the one with "auth" right after a separator
	// and earliest in the string (item 3, "feat: add auth/oauth support").
	// Items 2 and 4 must not appear.
	if got[0].Index != 3 {
		t.Fatalf("expected best match to be index 3; got %d (full: %v)", got[0].Index, got)
	}
	for _, m := range got {
		if m.Index == 2 || m.Index == 4 {
			t.Fatalf("non-matching items should not appear in results; got %v", got)
		}
	}
	// Scores must be non-increasing.
	for i := 1; i < len(got); i++ {
		if got[i-1].Score < got[i].Score {
			t.Fatalf("rank not sorted by descending score: %v", got)
		}
	}
}

func TestRank_StableTieBreak(t *testing.T) {
	// Two identical strings — should appear in input order.
	items := []string{"abc", "abc"}
	got := Rank("ab", items)
	if len(got) != 2 || got[0].Index != 0 || got[1].Index != 1 {
		t.Fatalf("expected stable tie-break by index; got %v", got)
	}
}

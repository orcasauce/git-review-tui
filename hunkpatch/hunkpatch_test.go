package hunkpatch

import (
	"strings"
	"testing"
)

func TestCanonical_StripsHeaderAndTrailingNewline(t *testing.T) {
	in := "@@ -1,3 +1,3 @@\n context\n-old\n+new\n"
	want := " context\n-old\n+new"
	if got := Canonical(in); got != want {
		t.Errorf("Canonical:\n got %q\nwant %q", got, want)
	}
}

func TestCanonical_LineNumberInsensitive(t *testing.T) {
	a := "@@ -1,3 +1,3 @@\n ctx\n-old\n+new"
	b := "@@ -42,3 +99,3 @@\n ctx\n-old\n+new"
	if Canonical(a) != Canonical(b) {
		t.Errorf("Canonical line-number sensitive: %q vs %q",
			Canonical(a), Canonical(b))
	}
}

func TestCanonical_KeepsBackslashNoNewlineMarker(t *testing.T) {
	in := "@@ -1 +1 @@\n-a\n+b\n\\ No newline at end of file"
	got := Canonical(in)
	if !strings.Contains(got, "\\ No newline at end of file") {
		t.Errorf("Canonical dropped \\ marker: %q", got)
	}
}

func TestCanonical_Empty(t *testing.T) {
	if got := Canonical(""); got != "" {
		t.Errorf("Canonical(\"\") = %q, want \"\"", got)
	}
}

func TestCanonical_HeaderlessHunk(t *testing.T) {
	in := " ctx\n-old\n+new"
	if got := Canonical(in); got != in {
		t.Errorf("Canonical headerless: got %q, want %q", got, in)
	}
}

func TestHash_DeterministicAndSensitive(t *testing.T) {
	a := Hash("-old\n+new")
	b := Hash("-old\n+new")
	c := Hash("-old\n+newer")
	if a != b {
		t.Errorf("Hash not deterministic: %q vs %q", a, b)
	}
	if a == c {
		t.Errorf("Hash collision on different inputs: both %q", a)
	}
	if len(a) != 16 {
		t.Errorf("Hash length = %d, want 16", len(a))
	}
}

func TestExtractByIndex_PureAdd(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/f b/f",
		"index 1111111..2222222 100644",
		"--- a/f",
		"+++ b/f",
		"@@ -0,0 +1,2 @@",
		"+a",
		"+b",
	}, "\n")
	got := ExtractByIndex(diff, 0)
	want := "@@ -0,0 +1,2 @@\n+a\n+b"
	if got != want {
		t.Errorf("ExtractByIndex pure-add:\n got %q\nwant %q", got, want)
	}
}

func TestExtractByIndex_PureDelete(t *testing.T) {
	diff := strings.Join([]string{
		"--- a/f",
		"+++ b/f",
		"@@ -1,2 +0,0 @@",
		"-a",
		"-b",
	}, "\n")
	got := ExtractByIndex(diff, 0)
	want := "@@ -1,2 +0,0 @@\n-a\n-b"
	if got != want {
		t.Errorf("ExtractByIndex pure-del:\n got %q\nwant %q", got, want)
	}
}

func TestExtractByIndex_Mixed(t *testing.T) {
	diff := strings.Join([]string{
		"--- a/f",
		"+++ b/f",
		"@@ -1,3 +1,3 @@",
		" ctx1",
		"-old",
		"+new",
	}, "\n")
	got := ExtractByIndex(diff, 0)
	want := "@@ -1,3 +1,3 @@\n ctx1\n-old\n+new"
	if got != want {
		t.Errorf("ExtractByIndex mixed:\n got %q\nwant %q", got, want)
	}
}

func TestExtractByIndex_MultipleHunks(t *testing.T) {
	diff := strings.Join([]string{
		"--- a/f",
		"+++ b/f",
		"@@ -1,2 +1,2 @@",
		" ctx",
		"-a",
		"+A",
		"@@ -10,2 +10,2 @@",
		" ctx",
		"-b",
		"+B",
	}, "\n")
	cases := []struct {
		idx  int
		want string
	}{
		{0, "@@ -1,2 +1,2 @@\n ctx\n-a\n+A"},
		{1, "@@ -10,2 +10,2 @@\n ctx\n-b\n+B"},
	}
	for _, c := range cases {
		if got := ExtractByIndex(diff, c.idx); got != c.want {
			t.Errorf("ExtractByIndex(%d):\n got %q\nwant %q",
				c.idx, got, c.want)
		}
	}
}

func TestExtractByIndex_OutOfRangeAndNegative(t *testing.T) {
	diff := "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-a\n+b"
	if got := ExtractByIndex(diff, 1); got != "" {
		t.Errorf("ExtractByIndex past end = %q, want \"\"", got)
	}
	if got := ExtractByIndex(diff, -1); got != "" {
		t.Errorf("ExtractByIndex(-1) = %q, want \"\"", got)
	}
	if got := ExtractByIndex("", 0); got != "" {
		t.Errorf("ExtractByIndex(empty) = %q, want \"\"", got)
	}
}

func TestExtractByIndex_IgnoresCombinedHeader(t *testing.T) {
	diff := strings.Join([]string{
		"--- a/f",
		"+++ b/f",
		"@@@ -1,2 -1,2 +1,2 @@@",
		"- a",
		"++b",
	}, "\n")
	if got := ExtractByIndex(diff, 0); got != "" {
		t.Errorf("ExtractByIndex combined-diff = %q, want \"\" (no @@ hunks)", got)
	}
}

func TestCombineForFile_TwoHunks(t *testing.T) {
	hunks := []string{
		"@@ -1,2 +1,2 @@\n ctx\n-a\n+A",
		"@@ -10,2 +10,2 @@\n ctx\n-b\n+B",
	}
	got := CombineForFile("dir/f.txt", hunks)
	want := "diff --git a/dir/f.txt b/dir/f.txt\n" +
		"--- a/dir/f.txt\n" +
		"+++ b/dir/f.txt\n" +
		"@@ -1,2 +1,2 @@\n ctx\n-a\n+A\n" +
		"@@ -10,2 +10,2 @@\n ctx\n-b\n+B\n"
	if got != want {
		t.Errorf("CombineForFile:\n got %q\nwant %q", got, want)
	}
}

func TestCombineForFile_EmptyInputs(t *testing.T) {
	if got := CombineForFile("", []string{"@@ -1 +1 @@\n-a\n+b"}); got != "" {
		t.Errorf("CombineForFile empty path = %q, want \"\"", got)
	}
	if got := CombineForFile("f", nil); got != "" {
		t.Errorf("CombineForFile nil hunks = %q, want \"\"", got)
	}
}

func TestCombineForFile_TrailingNewlineNormalised(t *testing.T) {
	hunks := []string{"@@ -1 +1 @@\n-a\n+b\n\n"}
	got := CombineForFile("f", hunks)
	if !strings.HasSuffix(got, "+b\n") {
		t.Errorf("CombineForFile did not trim extra trailing newlines:\n%q", got)
	}
	if strings.HasSuffix(got, "\n\n") {
		t.Errorf("CombineForFile left double trailing newline:\n%q", got)
	}
}

func TestExtractThenCanonical_RoundTrip(t *testing.T) {
	diff := strings.Join([]string{
		"--- a/f",
		"+++ b/f",
		"@@ -1,3 +1,3 @@",
		" ctx",
		"-old",
		"+new",
		"@@ -20,3 +20,3 @@",
		" ctx2",
		"-x",
		"+y",
	}, "\n")
	h0 := ExtractByIndex(diff, 0)
	h1 := ExtractByIndex(diff, 1)
	if Canonical(h0) == Canonical(h1) {
		t.Errorf("Canonical collapsed two distinct hunks: %q", Canonical(h0))
	}
	if Hash(Canonical(h0)) == Hash(Canonical(h1)) {
		t.Errorf("Hash collapsed two distinct hunks")
	}
}

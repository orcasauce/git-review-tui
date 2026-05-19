package diffrender

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func init() {
	// Force a 256-color profile so style.Render() emits ANSI escapes
	// even when tests run without a TTY.
	lipgloss.SetColorProfile(termenv.ANSI256)
}

const sampleDiff = `diff --git a/a.txt b/a.txt
index 0000000..1111111 100644
--- a/a.txt
+++ b/a.txt
@@ -1,3 +1,4 @@
 one
-two
+TWO
 three
+four
`

func TestParse_LineCountsAndKinds(t *testing.T) {
	r := Parse(sampleDiff, "")
	if got, want := len(r.Lines), 5; got != want {
		t.Fatalf("len(Lines) = %d, want %d: %+v", got, want, r.Lines)
	}
	wantKinds := []Kind{Context, Del, Add, Context, Add}
	for i, k := range wantKinds {
		if r.Lines[i].Kind != k {
			t.Errorf("Lines[%d].Kind = %v, want %v", i, r.Lines[i].Kind, k)
		}
	}
}

func TestParse_HunkStartsAndAtAtStripped(t *testing.T) {
	r := Parse(sampleDiff, "")
	if got, want := len(r.HunkStarts), 1; got != want {
		t.Fatalf("len(HunkStarts) = %d, want %d", got, want)
	}
	if got, want := r.HunkStarts[0], 0; got != want {
		t.Errorf("HunkStarts[0] = %d, want %d", got, want)
	}
	for i, l := range r.Lines {
		if strings.HasPrefix(l.Text, "@@") {
			t.Errorf("Lines[%d].Text = %q, @@ should be stripped", i, l.Text)
		}
	}
}

func TestParse_GutterLineNumbers(t *testing.T) {
	r := Parse(sampleDiff, "")
	// Expected old/new pairs per line:
	//   one    (ctx)   old=1 new=1
	//   -two            old=2 new=0
	//   +TWO            old=0 new=2
	//   three  (ctx)   old=3 new=3
	//   +four           old=0 new=4
	want := []struct{ old, new int }{
		{1, 1}, {2, 0}, {0, 2}, {3, 3}, {0, 4},
	}
	for i, w := range want {
		if r.Lines[i].OldNum != w.old || r.Lines[i].NewNum != w.new {
			t.Errorf("Lines[%d] gutter = (%d,%d), want (%d,%d)",
				i, r.Lines[i].OldNum, r.Lines[i].NewNum, w.old, w.new)
		}
	}
	if r.OldW != 1 || r.NewW != 1 {
		t.Errorf("OldW/NewW = %d/%d, want 1/1", r.OldW, r.NewW)
	}
}

func TestParse_MultipleHunks(t *testing.T) {
	in := `diff --git a/x b/x
--- a/x
+++ b/x
@@ -1,1 +1,2 @@
 first
+added-1
@@ -10,2 +11,2 @@
-old-ten
+new-ten
 eleven
`
	r := Parse(in, "")
	if len(r.HunkStarts) != 2 {
		t.Fatalf("HunkStarts = %v, want 2 entries", r.HunkStarts)
	}
	// First hunk starts at index 0; second hunk starts where the 3rd
	// line of content begins (after "first" and "+added-1").
	if r.HunkStarts[0] != 0 {
		t.Errorf("HunkStarts[0] = %d, want 0", r.HunkStarts[0])
	}
	if r.HunkStarts[1] != 2 {
		t.Errorf("HunkStarts[1] = %d, want 2", r.HunkStarts[1])
	}
	// Second hunk renumbers the gutter.
	second := r.Lines[r.HunkStarts[1]]
	if second.Kind != Del || second.OldNum != 10 {
		t.Errorf("second hunk first line = %+v, want Del with OldNum=10", second)
	}
}

func TestParse_StripsNoNewlineMarker(t *testing.T) {
	in := `--- a/x
+++ b/x
@@ -1 +1 @@
-old
\ No newline at end of file
+new
`
	r := Parse(in, "")
	if len(r.Lines) != 2 {
		t.Fatalf("len(Lines) = %d, want 2 (no-newline marker should be stripped): %+v",
			len(r.Lines), r.Lines)
	}
	if r.Lines[0].Kind != Del || r.Lines[1].Kind != Add {
		t.Errorf("kinds = %v / %v, want Del / Add", r.Lines[0].Kind, r.Lines[1].Kind)
	}
}

func TestFormatLine_StylingOnAddAndDel(t *testing.T) {
	r := Parse(sampleDiff, "")
	addIdx := -1
	delIdx := -1
	ctxIdx := -1
	for i, l := range r.Lines {
		switch l.Kind {
		case Add:
			if addIdx == -1 {
				addIdx = i
			}
		case Del:
			if delIdx == -1 {
				delIdx = i
			}
		case Context:
			if ctxIdx == -1 {
				ctxIdx = i
			}
		}
	}
	addOut := r.FormatLine(addIdx, 40, 0)
	delOut := r.FormatLine(delIdx, 40, 0)
	ctxOut := r.FormatLine(ctxIdx, 40, 0)
	if !strings.Contains(addOut, "\x1b[") {
		t.Errorf("expected ANSI escape on add line: %q", addOut)
	}
	if !strings.Contains(delOut, "\x1b[") {
		t.Errorf("expected ANSI escape on del line: %q", delOut)
	}
	// Context line content itself shouldn't carry the +/- color — but
	// the gutter does, so we strip the gutter prefix before checking.
	rest := strings.TrimPrefix(ctxOut, gutterStyle.Render(""))
	if strings.Contains(rest, "[38;5;114m") || strings.Contains(rest, "[38;5;203m") {
		t.Errorf("context line should not carry +/- color: %q", ctxOut)
	}
	if !strings.Contains(addOut, "+TWO") {
		t.Errorf("add line lost its content: %q", addOut)
	}
	if !strings.Contains(delOut, "-two") {
		t.Errorf("del line lost its content: %q", delOut)
	}
}

func TestFormatLine_GutterHasLineNumbers(t *testing.T) {
	in := `--- a/x
+++ b/x
@@ -9,2 +9,3 @@
 nine
-ten
+TEN
+eleven
`
	r := Parse(in, "")
	if r.OldW != 2 || r.NewW != 2 {
		t.Fatalf("OldW/NewW = %d/%d, want 2/2", r.OldW, r.NewW)
	}
	// Context line: gutter should contain " 9  9".
	out := r.FormatLine(0, 80, 0)
	if !strings.Contains(out, " 9  9") {
		t.Errorf("expected gutter ' 9  9' in context line: %q", out)
	}
	// Delete line: " 10 " with no new num.
	out = r.FormatLine(1, 80, 0)
	if !strings.Contains(out, "10") {
		t.Errorf("expected old line number 10 on del line: %q", out)
	}
	// Add line: only new num.
	out = r.FormatLine(2, 80, 0)
	if !strings.Contains(out, "10") {
		t.Errorf("expected new line number 10 on add line: %q", out)
	}
}

func TestFormatLine_HorizontalScroll(t *testing.T) {
	in := `--- a/x
+++ b/x
@@ -1 +1 @@
-ABCDEFGHIJ
+abcdefghij
`
	r := Parse(in, "")
	out := r.FormatLine(1, 40, 4)
	// hScroll=4 should drop the first 4 chars of "abcdefghij" → "efghij",
	// then prepend the "+" marker → "+efghij".
	if !strings.Contains(out, "+efghij") {
		t.Errorf("expected '+efghij' after hScroll=4: %q", out)
	}
	if strings.Contains(out, "abcd") {
		t.Errorf("expected 'abcd' to be scrolled off: %q", out)
	}
}

func TestParse_EmptyInput(t *testing.T) {
	r := Parse("", "")
	if len(r.Lines) != 0 {
		t.Errorf("expected 0 lines, got %d", len(r.Lines))
	}
	if len(r.HunkStarts) != 0 {
		t.Errorf("expected 0 hunks, got %d", len(r.HunkStarts))
	}
}

func TestParse_HunkStarts_SingleHunk(t *testing.T) {
	r := Parse(sampleDiff, "")
	if got, want := r.HunkStarts, []int{0}; len(got) != len(want) || got[0] != want[0] {
		t.Errorf("HunkStarts = %v, want %v", got, want)
	}
}

func TestParse_HunkStarts_ManyHunks(t *testing.T) {
	in := `--- a/x
+++ b/x
@@ -1,1 +1,2 @@
 a
+b
@@ -10,2 +11,2 @@
-c
+C
 d
@@ -20,1 +21,2 @@
 e
+f
@@ -30,1 +32,1 @@
-g
+G
`
	r := Parse(in, "")
	wantStarts := []int{0, 2, 5, 7}
	if len(r.HunkStarts) != len(wantStarts) {
		t.Fatalf("HunkStarts = %v, want %v", r.HunkStarts, wantStarts)
	}
	for i, w := range wantStarts {
		if r.HunkStarts[i] != w {
			t.Errorf("HunkStarts[%d] = %d, want %d", i, r.HunkStarts[i], w)
		}
	}
	// Each start index points at the first line of its hunk.
	if r.Lines[r.HunkStarts[1]].OldNum != 10 {
		t.Errorf("hunk 1 first line OldNum = %d, want 10", r.Lines[r.HunkStarts[1]].OldNum)
	}
	if r.Lines[r.HunkStarts[2]].OldNum != 20 {
		t.Errorf("hunk 2 first line OldNum = %d, want 20", r.Lines[r.HunkStarts[2]].OldNum)
	}
}

func TestParse_HunkStarts_AllContext(t *testing.T) {
	in := `--- a/x
+++ b/x
@@ -1,3 +1,3 @@
 one
 two
 three
`
	r := Parse(in, "")
	if len(r.HunkStarts) != 1 || r.HunkStarts[0] != 0 {
		t.Errorf("HunkStarts = %v, want [0]", r.HunkStarts)
	}
	for i, l := range r.Lines {
		if l.Kind != Context {
			t.Errorf("Lines[%d].Kind = %v, want Context", i, l.Kind)
		}
	}
}

func TestParse_HunkStarts_AllChanges(t *testing.T) {
	in := `--- a/x
+++ b/x
@@ -1,3 +1,3 @@
-one
-two
-three
+ONE
+TWO
+THREE
`
	r := Parse(in, "")
	if len(r.HunkStarts) != 1 || r.HunkStarts[0] != 0 {
		t.Errorf("HunkStarts = %v, want [0]", r.HunkStarts)
	}
	if len(r.Lines) != 6 {
		t.Fatalf("len(Lines) = %d, want 6", len(r.Lines))
	}
	for i := 0; i < 3; i++ {
		if r.Lines[i].Kind != Del {
			t.Errorf("Lines[%d].Kind = %v, want Del", i, r.Lines[i].Kind)
		}
	}
	for i := 3; i < 6; i++ {
		if r.Lines[i].Kind != Add {
			t.Errorf("Lines[%d].Kind = %v, want Add", i, r.Lines[i].Kind)
		}
	}
}

// goDiff exercises chroma's Go lexer: a keyword (`package`) on a
// context line, a string literal on an add line.
const goDiff = `diff --git a/x.go b/x.go
--- a/x.go
+++ b/x.go
@@ -1,2 +1,3 @@
 package main
-var greeting = "hi"
+var greeting = "hello"
+func main() {}
`

func TestParse_SyntaxHighlightingForKnownLanguage(t *testing.T) {
	plain := Parse(goDiff, "")
	hl := Parse(goDiff, "x.go")
	if len(plain.Lines) != len(hl.Lines) {
		t.Fatalf("line count diverged between plain and highlighted: %d vs %d",
			len(plain.Lines), len(hl.Lines))
	}
	// Highlighted result must have segs populated on every line; plain
	// result must not.
	for i := range hl.Lines {
		if plain.Lines[i].segs != nil {
			t.Errorf("plain Lines[%d].segs unexpectedly populated", i)
		}
		if hl.Lines[i].segs == nil {
			t.Errorf("highlighted Lines[%d].segs is nil", i)
		}
	}
	// Rendered output should differ between plain and highlighted on
	// at least one line — chroma must be emitting different escapes
	// than the simple +/- foreground.
	differs := false
	for i := range hl.Lines {
		if hl.FormatLine(i, 80, 0) != plain.FormatLine(i, 80, 0) {
			differs = true
			break
		}
	}
	if !differs {
		t.Errorf("highlighted output identical to plain output for goDiff")
	}
	// The marker glyph on the add line must still appear (diff signal
	// preserved): look for a "+" somewhere in the body of an add line.
	var addIdx int = -1
	for i, l := range hl.Lines {
		if l.Kind == Add {
			addIdx = i
			break
		}
	}
	if addIdx < 0 {
		t.Fatalf("expected at least one Add line in goDiff")
	}
	out := hl.FormatLine(addIdx, 80, 0)
	if !strings.Contains(out, "+") {
		t.Errorf("highlighted add line lost its '+' marker: %q", out)
	}
	// Content tokens still survive in the rendered bytes.
	if !strings.Contains(out, "greeting") && !strings.Contains(out, "hello") && !strings.Contains(out, "main") {
		t.Errorf("highlighted add line lost its content: %q", out)
	}
}

func TestParse_UnknownExtensionFallsBackToPlain(t *testing.T) {
	// `.zzzzz` is not a real language; chroma should miss and we should
	// emit the same bytes as the plain (filename="") path.
	hl := Parse(sampleDiff, "weird.zzzzz")
	plain := Parse(sampleDiff, "")
	for i := range hl.Lines {
		if hl.Lines[i].segs != nil {
			t.Errorf("Lines[%d].segs should be nil for unknown extension", i)
		}
	}
	if hl.FormatLine(0, 40, 0) != plain.FormatLine(0, 40, 0) {
		t.Errorf("rendered output differs between unknown-extension and plain modes")
	}
}

func TestFormatLine_HighlightedRespectsWidthAndScroll(t *testing.T) {
	r := Parse(goDiff, "x.go")
	// Width-truncate: a width of (gutter + 5) leaves 5 cells for marker
	// + body; the output should not exceed that printable length.
	w := r.GutterRenderWidth() + 5
	out := r.FormatLine(1, w, 0)
	if got, want := visibleLen(out), w; got != want {
		t.Errorf("visibleLen(out) = %d, want %d (out=%q)", got, want, out)
	}
	// Horizontal scroll past the start: marker should remain and some
	// trailing characters should appear.
	scrolled := r.FormatLine(1, 80, 4)
	if !strings.Contains(scrolled, "-") {
		t.Errorf("scrolled highlighted del line missing marker: %q", scrolled)
	}
}

// visibleLen counts the printable cells in s by stripping CSI escape
// sequences. Sufficient for the ANSI256 foreground/SGR escapes lipgloss
// emits in tests.
func visibleLen(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
			continue
		}
		n++
	}
	return n
}

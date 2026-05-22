package diffrender

import (
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
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
	r := Parse(sampleDiff, "", "")
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
	r := Parse(sampleDiff, "", "")
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
	r := Parse(sampleDiff, "", "")
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
	r := Parse(in, "", "")
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
	r := Parse(in, "", "")
	if len(r.Lines) != 2 {
		t.Fatalf("len(Lines) = %d, want 2 (no-newline marker should be stripped): %+v",
			len(r.Lines), r.Lines)
	}
	if r.Lines[0].Kind != Del || r.Lines[1].Kind != Add {
		t.Errorf("kinds = %v / %v, want Del / Add", r.Lines[0].Kind, r.Lines[1].Kind)
	}
}

func TestFormatLine_StylingOnAddAndDel(t *testing.T) {
	r := Parse(sampleDiff, "", "")
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
	// Context lines should not carry any of the add/del banding colours.
	for _, hex := range []string{addBodyBg, delBodyBg, addGutterBg, delGutterBg} {
		if strings.Contains(ctxOut, hex) {
			t.Errorf("context line unexpectedly contains banding hex %s: %q", hex, ctxOut)
		}
	}
	if !strings.Contains(addOut, "TWO") {
		t.Errorf("add line lost its content: %q", addOut)
	}
	if !strings.Contains(delOut, "two") {
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
	r := Parse(in, "", "")
	if r.OldW != 2 || r.NewW != 2 {
		t.Fatalf("OldW/NewW = %d/%d, want 2/2", r.OldW, r.NewW)
	}
	// Context line: OldNum == NewNum on the leading row, so the
	// hide-when-equal rule blanks NewNum and the gutter shows " 9    ".
	out := r.FormatLine(0, 80, 0)
	if !strings.Contains(out, " 9    ") {
		t.Errorf("expected leading context gutter ' 9    ' (NewNum hidden, equal to OldNum): %q", out)
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
	r := Parse(in, "", "")
	out := r.FormatLine(1, 40, 4)
	// hScroll=4 should drop the first 4 chars of "abcdefghij" → "efghij".
	if !strings.Contains(out, "efghij") {
		t.Errorf("expected 'efghij' after hScroll=4: %q", out)
	}
	if strings.Contains(out, "abcd") {
		t.Errorf("expected 'abcd' to be scrolled off: %q", out)
	}
}

func TestParse_EmptyInput(t *testing.T) {
	r := Parse("", "", "")
	if len(r.Lines) != 0 {
		t.Errorf("expected 0 lines, got %d", len(r.Lines))
	}
	if len(r.HunkStarts) != 0 {
		t.Errorf("expected 0 hunks, got %d", len(r.HunkStarts))
	}
}

func TestParse_HunkStarts_SingleHunk(t *testing.T) {
	r := Parse(sampleDiff, "", "")
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
	r := Parse(in, "", "")
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
	r := Parse(in, "", "")
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
	r := Parse(in, "", "")
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
	plain := Parse(goDiff, "", "")
	hl := Parse(goDiff, "", "x.go")
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
	// Content tokens still survive in the rendered bytes.
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
	if !strings.Contains(out, "greeting") && !strings.Contains(out, "hello") && !strings.Contains(out, "main") {
		t.Errorf("highlighted add line lost its content: %q", out)
	}
}

func TestParse_UnknownExtensionFallsBackToPlain(t *testing.T) {
	// `.zzzzz` is not a real language; chroma should miss and we should
	// emit the same bytes as the plain (filename="") path.
	hl := Parse(sampleDiff, "", "weird.zzzzz")
	plain := Parse(sampleDiff, "", "")
	for i := range hl.Lines {
		if hl.Lines[i].segs != nil {
			t.Errorf("Lines[%d].segs should be nil for unknown extension", i)
		}
	}
	if hl.FormatLine(0, 40, 0) != plain.FormatLine(0, 40, 0) {
		t.Errorf("rendered output differs between unknown-extension and plain modes")
	}
}

// TestFormatLine_HighlightedNeverContainsNewline guards against a
// chroma quirk where single-line-comment tokens carry a trailing "\n"
// in their token value (the lexer matches "// ... \n" as one comment
// token). If that newline leaks through FormatLine, the diff panel
// emits an extra terminal row, the View() output overshoots the
// terminal height, and bubbletea drops the top row of the screen —
// eating the panel titles.
func TestFormatLine_HighlightedNeverContainsNewline(t *testing.T) {
	const tsDiff = `diff --git a/a.ts b/a.ts
index 0000000..1111111 100644
--- a/a.ts
+++ b/a.ts
@@ -1,2 +1,2 @@
-        //this.#caught = caught;
+        // resolved
`
	r := Parse(tsDiff, "", "a.ts")
	for i := range r.Lines {
		out := r.FormatLine(i, 120, 0)
		if strings.ContainsAny(out, "\n\r") {
			t.Errorf("FormatLine(%d) contains a newline or CR — diff row will spill into the next terminal row: %q", i, out)
		}
	}
}

func TestFormatLine_HighlightedRespectsWidthAndScroll(t *testing.T) {
	r := Parse(goDiff, "", "x.go")
	// Width-truncate: a width of (gutter + 5) leaves 5 cells for marker
	// + body; the output should not exceed that printable length.
	w := r.GutterRenderWidth() + 5
	out := r.FormatLine(1, w, 0)
	if got, want := visibleLen(out), w; got != want {
		t.Errorf("visibleLen(out) = %d, want %d (out=%q)", got, want, out)
	}
	// Horizontal scroll past the start: gutter band remains and some
	// trailing characters should appear.
	scrolled := r.FormatLine(1, 80, 4)
	if !strings.Contains(scrolled, styleOpen(delGutterBandStyle)) {
		t.Errorf("scrolled highlighted del line missing gutter band: %q", scrolled)
	}
}

// styleOpen returns the opening SGR escape that lipgloss emits when
// rendering this style — extracted by rendering a marker character and
// slicing off everything up to it. Robust against colour-profile
// degradation (the helper picks up whatever escape lipgloss actually
// emits at the current profile, rather than asserting a hard-coded
// truecolor hex).
func styleOpen(s lipgloss.Style) string {
	const marker = "\x01"
	r := s.Render(marker)
	i := strings.Index(r, marker)
	if i < 0 {
		return ""
	}
	return r[:i]
}

// findAddDelCtxIdx returns the first index of each Kind in r.Lines, or
// -1 if none exists.
func findAddDelCtxIdx(r Result) (add, del, ctx int) {
	add, del, ctx = -1, -1, -1
	for i, l := range r.Lines {
		switch l.Kind {
		case Add:
			if add == -1 {
				add = i
			}
		case Del:
			if del == -1 {
				del = i
			}
		case Context:
			if ctx == -1 {
				ctx = i
			}
		}
	}
	return
}

func TestFormatLine_AddRowBodyBackground(t *testing.T) {
	r := Parse(sampleDiff, "", "")
	addIdx, _, _ := findAddDelCtxIdx(r)
	if addIdx < 0 {
		t.Fatalf("sampleDiff has no add row")
	}
	out := r.FormatLine(addIdx, 40, 0)
	bgOpen := styleOpen(addBodyBgStyle)
	if bgOpen == "" {
		t.Fatalf("could not derive opening escape for addBodyBgStyle")
	}
	if !strings.Contains(out, bgOpen) {
		t.Errorf("add row missing body-bg escape %q: %q", bgOpen, out)
	}
}

func TestFormatLine_DelRowBodyBackground(t *testing.T) {
	r := Parse(sampleDiff, "", "")
	_, delIdx, _ := findAddDelCtxIdx(r)
	if delIdx < 0 {
		t.Fatalf("sampleDiff has no del row")
	}
	out := r.FormatLine(delIdx, 40, 0)
	bgOpen := styleOpen(delBodyBgStyle)
	if bgOpen == "" {
		t.Fatalf("could not derive opening escape for delBodyBgStyle")
	}
	if !strings.Contains(out, bgOpen) {
		t.Errorf("del row missing body-bg escape %q: %q", bgOpen, out)
	}
}

func TestFormatLine_AddRowGutterBand(t *testing.T) {
	r := Parse(sampleDiff, "", "")
	addIdx, _, _ := findAddDelCtxIdx(r)
	if addIdx < 0 {
		t.Fatalf("sampleDiff has no add row")
	}
	out := r.FormatLine(addIdx, 40, 0)
	bandOpen := styleOpen(addGutterBandStyle)
	if !strings.Contains(out, bandOpen) {
		t.Errorf("add row missing gutter-band escape %q: %q", bandOpen, out)
	}
}

func TestFormatLine_DelRowGutterBand(t *testing.T) {
	r := Parse(sampleDiff, "", "")
	_, delIdx, _ := findAddDelCtxIdx(r)
	if delIdx < 0 {
		t.Fatalf("sampleDiff has no del row")
	}
	out := r.FormatLine(delIdx, 40, 0)
	bandOpen := styleOpen(delGutterBandStyle)
	if !strings.Contains(out, bandOpen) {
		t.Errorf("del row missing gutter-band escape %q: %q", bandOpen, out)
	}
}

func TestFormatLine_ContextRowHasNoBanding(t *testing.T) {
	r := Parse(sampleDiff, "", "")
	_, _, ctxIdx := findAddDelCtxIdx(r)
	if ctxIdx < 0 {
		t.Fatalf("sampleDiff has no context row")
	}
	out := r.FormatLine(ctxIdx, 40, 0)
	for name, s := range map[string]lipgloss.Style{
		"addBodyBg":    addBodyBgStyle,
		"delBodyBg":    delBodyBgStyle,
		"addGutterBg":  addGutterBandStyle,
		"delGutterBg":  delGutterBandStyle,
	} {
		open := styleOpen(s)
		if open != "" && strings.Contains(out, open) {
			t.Errorf("context row unexpectedly carries %s escape %q: %q", name, open, out)
		}
	}
}

// TestFormatLine_GutterOnlyAtZeroBodyWidth: render an add row at a
// width that leaves zero body cells, and confirm the gutter band is
// still rendered and the body region is empty.
func TestFormatLine_GutterOnlyAtZeroBodyWidth(t *testing.T) {
	r := Parse(sampleDiff, "", "")
	addIdx, _, _ := findAddDelCtxIdx(r)
	if addIdx < 0 {
		t.Fatalf("sampleDiff has no add row")
	}
	w := r.GutterRenderWidth()
	out := r.FormatLine(addIdx, w, 0)
	if !strings.Contains(out, styleOpen(addGutterBandStyle)) {
		t.Errorf("expected gutter band escape at zero-body width: %q", out)
	}
	if got, want := visibleLen(out), w; got != want {
		t.Errorf("visibleLen(out) = %d, want %d (out=%q)", got, want, out)
	}
	if strings.Contains(out, "TWO") {
		t.Errorf("add row body content leaked into zero-body render: %q", out)
	}
}

func TestFormatLine_HighlightedAddRowChromaFgOverBodyBg(t *testing.T) {
	r := Parse(goDiff, "", "x.go")
	addIdx, _, _ := findAddDelCtxIdx(r)
	if addIdx < 0 {
		t.Fatalf("goDiff has no add row")
	}
	out := r.FormatLine(addIdx, 80, 0)
	if !strings.Contains(out, styleOpen(addBodyBgStyle)) {
		t.Errorf("highlighted add row missing body-bg escape: %q", out)
	}
	// A chroma-coloured token should still emit a foreground SGR
	// somewhere — the bg tint must compose with, not replace, the
	// per-token fg.
	if !strings.Contains(out, "\x1b[38;") {
		t.Errorf("highlighted add row missing any chroma foreground escape: %q", out)
	}
}

// TestStyleFor_TokenAnsiMapping checks the contract spelled out in the
// PRD: each meaningful token category resolves to a specific ANSI
// palette index, subcategories inherit from their parent (e.g.
// LiteralStringDouble → LiteralString → 2), and Operator/Punctuation
// emit no foreground escape at all (terminal default fg).
func TestStyleFor_TokenAnsiMapping(t *testing.T) {
	cases := []struct {
		name string
		tt   chroma.TokenType
		ansi string // "" means: expect no fg escape
	}{
		{"Comment", chroma.Comment, "8"},
		{"CommentSingle (subcategory)", chroma.CommentSingle, "8"},
		{"Keyword", chroma.Keyword, "4"},
		{"KeywordType", chroma.KeywordType, "6"},
		{"LiteralString", chroma.LiteralString, "2"},
		{"LiteralStringDouble (subcategory)", chroma.LiteralStringDouble, "2"},
		{"LiteralNumber", chroma.LiteralNumber, "3"},
		{"LiteralNumberInteger (subcategory)", chroma.LiteralNumberInteger, "3"},
		{"NameClass", chroma.NameClass, "6"},
		{"NameFunction", chroma.NameFunction, "5"},
		{"NameBuiltin", chroma.NameBuiltin, "1"},
		{"NameConstant", chroma.NameConstant, "1"},
		{"Operator", chroma.Operator, ""},
		{"Punctuation", chroma.Punctuation, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rendered := styleFor(c.tt).Render("x")
			if c.ansi == "" {
				if strings.Contains(rendered, "\x1b[") {
					t.Errorf("%v: expected no SGR escape, got %q", c.tt, rendered)
				}
				return
			}
			wantOpen := lipgloss.NewStyle().Foreground(lipgloss.Color(c.ansi)).Render("\x01")
			i := strings.Index(wantOpen, "\x01")
			if i <= 0 {
				t.Fatalf("could not derive opening escape for ANSI %s", c.ansi)
			}
			open := wantOpen[:i]
			if !strings.Contains(rendered, open) {
				t.Errorf("%v: rendered %q does not contain ANSI %s opener %q",
					c.tt, rendered, c.ansi, open)
			}
		})
	}
}

// TestStyleFor_DropsBoldItalic ensures the new mapping never emits bold
// (SGR 1) or italic (SGR 3) escapes, even for token types where chroma's
// native style would have set them (Keyword is bold under the native
// style, for instance).
func TestStyleFor_DropsBoldItalic(t *testing.T) {
	for _, tt := range []chroma.TokenType{
		chroma.Keyword, chroma.NameFunction, chroma.NameClass, chroma.Comment,
	} {
		rendered := styleFor(tt).Render("x")
		// SGR "1" = bold, SGR "3" = italic. Look for them as standalone
		// parameters (preceded by '[' or ';', followed by ';' or 'm').
		for _, bad := range []string{"\x1b[1m", "\x1b[3m", ";1m", ";3m", "[1;", "[3;"} {
			if strings.Contains(rendered, bad) {
				t.Errorf("%v rendered with bold/italic (%q) in %q", tt, bad, rendered)
			}
		}
	}
}

// TestSetFlaggedHunks_PureAddTombstone renders a pure-addition hunk
// after flagging it and verifies the body carries the grey tombstone
// bg (not the green add bg).
func TestSetFlaggedHunks_PureAddTombstone(t *testing.T) {
	in := `--- a/x
+++ b/x
@@ -1,1 +1,3 @@
 first
+added-a
+added-b
`
	r := Parse(in, in, "")
	r.SetFlaggedHunks([]int{0})
	if len(r.HunkStarts) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(r.HunkStarts))
	}
	addIdx, _, _ := findAddDelCtxIdx(r)
	if addIdx < 0 {
		t.Fatalf("no add line found")
	}
	out := r.FormatLine(addIdx, 40, 0)
	flagOpen := styleOpen(flagBodyStyle)
	if !strings.Contains(out, flagOpen) {
		t.Errorf("flagged add line missing flagBodyStyle escape %q: %q", flagOpen, out)
	}
	if strings.Contains(out, styleOpen(addBodyBgStyle)) {
		t.Errorf("flagged add line still carries addBodyBg escape: %q", out)
	}
	if !strings.Contains(out, styleOpen(flagGutterBandStyle)) {
		t.Errorf("flagged add line missing flagGutterBand escape: %q", out)
	}
	if strings.Contains(out, styleOpen(addGutterBandStyle)) {
		t.Errorf("flagged add line still carries addGutterBand escape: %q", out)
	}
}

// TestSetFlaggedHunks_PureDelTombstone is the mirror of the above for a
// pure-deletion hunk: grey body bg and grey gutter, not red.
func TestSetFlaggedHunks_PureDelTombstone(t *testing.T) {
	in := `--- a/x
+++ b/x
@@ -1,3 +1,1 @@
 first
-removed-a
-removed-b
`
	r := Parse(in, in, "")
	r.SetFlaggedHunks([]int{0})
	_, delIdx, _ := findAddDelCtxIdx(r)
	if delIdx < 0 {
		t.Fatalf("no del line found")
	}
	out := r.FormatLine(delIdx, 40, 0)
	if !strings.Contains(out, styleOpen(flagBodyStyle)) {
		t.Errorf("flagged del line missing flagBodyStyle escape: %q", out)
	}
	if strings.Contains(out, styleOpen(delBodyBgStyle)) {
		t.Errorf("flagged del line still carries delBodyBg escape: %q", out)
	}
	if !strings.Contains(out, styleOpen(flagGutterBandStyle)) {
		t.Errorf("flagged del line missing flagGutterBand escape: %q", out)
	}
	if strings.Contains(out, styleOpen(delGutterBandStyle)) {
		t.Errorf("flagged del line still carries delGutterBand escape: %q", out)
	}
}

// TestSetFlaggedHunks_MixedHunkAllGrey: both + and - lines in a mixed
// hunk render under one grey block; neither add-green nor del-red
// styling leaks through.
func TestSetFlaggedHunks_MixedHunkAllGrey(t *testing.T) {
	r := Parse(sampleDiff, sampleDiff, "")
	r.SetFlaggedHunks([]int{0})
	addIdx, delIdx, _ := findAddDelCtxIdx(r)
	if addIdx < 0 || delIdx < 0 {
		t.Fatalf("sampleDiff missing add or del lines")
	}
	for _, idx := range []int{addIdx, delIdx} {
		out := r.FormatLine(idx, 40, 0)
		if !strings.Contains(out, styleOpen(flagBodyStyle)) {
			t.Errorf("line %d missing flag body escape: %q", idx, out)
		}
		if strings.Contains(out, styleOpen(addBodyBgStyle)) {
			t.Errorf("flagged line %d still carries addBodyBg escape: %q", idx, out)
		}
		if strings.Contains(out, styleOpen(delBodyBgStyle)) {
			t.Errorf("flagged line %d still carries delBodyBg escape: %q", idx, out)
		}
	}
}

// TestSetFlaggedHunks_UnflaggedHunksUnaffected verifies that flagging
// one hunk leaves a sibling hunk in the same file rendering with its
// usual red/green styling.
func TestSetFlaggedHunks_UnflaggedHunksUnaffected(t *testing.T) {
	in := `--- a/x
+++ b/x
@@ -1,1 +1,2 @@
 first
+added-1
@@ -10,2 +11,2 @@
-old-ten
+new-ten
 eleven
`
	r := Parse(in, in, "")
	if len(r.HunkStarts) != 2 {
		t.Fatalf("expected 2 hunks, got %d", len(r.HunkStarts))
	}
	r.SetFlaggedHunks([]int{0}) // only flag the first hunk

	// First hunk's +added-1 line is flagged → grey.
	firstAddIdx := r.HunkStarts[0]
	if r.Lines[firstAddIdx].Kind != Add {
		// Find an add line within the first hunk.
		for i := r.HunkStarts[0]; i <= r.HunkEnds[0]; i++ {
			if r.Lines[i].Kind == Add {
				firstAddIdx = i
				break
			}
		}
	}
	outFirst := r.FormatLine(firstAddIdx, 60, 0)
	if !strings.Contains(outFirst, styleOpen(flagBodyStyle)) {
		t.Errorf("first-hunk add line missing flag styling: %q", outFirst)
	}

	// Second hunk's -old-ten line is unflagged → still red.
	secondDelIdx := -1
	for i := r.HunkStarts[1]; i <= r.HunkEnds[1]; i++ {
		if r.Lines[i].Kind == Del {
			secondDelIdx = i
			break
		}
	}
	if secondDelIdx < 0 {
		t.Fatalf("could not find del line in second hunk")
	}
	outSecond := r.FormatLine(secondDelIdx, 60, 0)
	if strings.Contains(outSecond, styleOpen(flagBodyStyle)) {
		t.Errorf("unflagged second-hunk del line should NOT carry flag styling: %q", outSecond)
	}
	if !strings.Contains(outSecond, styleOpen(delBodyBgStyle)) {
		t.Errorf("unflagged del line lost its red body bg: %q", outSecond)
	}
}

// TestSetFlaggedHunks_ContextOutsideUnaffected: the leading context
// line before a flagged hunk in `sampleDiff` should render without
// any banding (flag, add, or del).
func TestSetFlaggedHunks_ContextOutsideUnaffected(t *testing.T) {
	r := Parse(sampleDiff, sampleDiff, "")
	r.SetFlaggedHunks([]int{0})
	// sampleDiff's first line is a context line (" one"). Confirm it
	// stays neutral after flagging the only hunk — the flagged region
	// begins at the first changed line, not before it.
	_, _, ctxIdx := findAddDelCtxIdx(r)
	if ctxIdx < 0 {
		t.Fatalf("no context line in sampleDiff")
	}
	if ctxIdx > r.HunkStarts[0] {
		// The context-line guard only matters when the context line is
		// before the hunk's start; sampleDiff's " one" is at index 0
		// and the hunk starts at 1 (the "-two" line).
		t.Skip("context line is inside the flagged hunk range; covered elsewhere")
	}
	out := r.FormatLine(ctxIdx, 40, 0)
	for name, s := range map[string]lipgloss.Style{
		"flagBody":    flagBodyStyle,
		"addBodyBg":   addBodyBgStyle,
		"delBodyBg":   delBodyBgStyle,
		"flagGutter":  flagGutterBandStyle,
		"addGutter":   addGutterBandStyle,
		"delGutter":   delGutterBandStyle,
	} {
		open := styleOpen(s)
		if open != "" && strings.Contains(out, open) {
			t.Errorf("outside-context line unexpectedly carries %s escape: %q", name, out)
		}
	}
}

// TestSetFlaggedHunks_EmptyClears verifies that calling SetFlaggedHunks
// with nil/empty clears all flagging.
func TestSetFlaggedHunks_EmptyClears(t *testing.T) {
	r := Parse(sampleDiff, sampleDiff, "")
	r.SetFlaggedHunks([]int{0})
	r.SetFlaggedHunks(nil)
	addIdx, _, _ := findAddDelCtxIdx(r)
	out := r.FormatLine(addIdx, 40, 0)
	if strings.Contains(out, styleOpen(flagBodyStyle)) {
		t.Errorf("after clearing flags, add line should not carry flag styling: %q", out)
	}
	if !strings.Contains(out, styleOpen(addBodyBgStyle)) {
		t.Errorf("after clearing flags, add line should regain its green bg: %q", out)
	}
}

// TestSetFlaggedHunks_HighlightedBodyGreysChroma confirms the
// chroma-highlighted code path also greys out per-token colours
// inside a flagged hunk — otherwise syntax highlighting would
// re-introduce a non-grey signal.
func TestSetFlaggedHunks_HighlightedBodyGreysChroma(t *testing.T) {
	r := Parse(goDiff, goDiff, "x.go")
	addIdx, _, _ := findAddDelCtxIdx(r)
	if addIdx < 0 {
		t.Fatalf("no add line in goDiff")
	}
	// Unflagged: chroma fg escape should appear.
	unflagged := r.FormatLine(addIdx, 80, 0)
	if !strings.Contains(unflagged, "\x1b[38;") {
		t.Errorf("unflagged highlighted add row missing any chroma fg escape: %q", unflagged)
	}
	// Flag and re-render: the body bg must be flag grey, and the per
	// token fg should be the flag fg, not whatever chroma assigned.
	r.SetFlaggedHunks([]int{0})
	flagged := r.FormatLine(addIdx, 80, 0)
	if !strings.Contains(flagged, styleOpen(flagBodyStyle)) {
		t.Errorf("flagged highlighted add row missing flag body escape: %q", flagged)
	}
	if strings.Contains(flagged, styleOpen(addBodyBgStyle)) {
		t.Errorf("flagged highlighted add row still carries addBodyBg escape: %q", flagged)
	}
}

// TestFormatLineActive_UnflaggedBorderColours verifies that the
// active-hunk overline/underline uses the add-green SGR 58 sequence on
// an add anchor and the del-red sequence on a del anchor when the
// active hunk is unflagged.
func TestFormatLineActive_UnflaggedBorderColours(t *testing.T) {
	r := Parse(sampleDiff, sampleDiff, "")
	if len(r.HunkStarts) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(r.HunkStarts))
	}
	// sampleDiff hunk: first changed line is Del ("two"), last is Add ("four").
	firstIdx := r.HunkStarts[0]
	lastIdx := r.HunkEnds[0]
	if r.Lines[firstIdx].Kind != Del {
		t.Fatalf("expected first changed line to be Del, got %v", r.Lines[firstIdx].Kind)
	}
	if r.Lines[lastIdx].Kind != Add {
		t.Fatalf("expected last changed line to be Add, got %v", r.Lines[lastIdx].Kind)
	}
	outFirst := r.FormatLineActive(firstIdx, 40, 0, 0)
	if !strings.Contains(outFirst, "\x1b[58;2;207;106;76m") {
		t.Errorf("unflagged del anchor missing del-red SGR 58: %q", outFirst)
	}
	if strings.Contains(outFirst, "\x1b[58;2;108;108;108m") {
		t.Errorf("unflagged del anchor unexpectedly carries flag-grey SGR 58: %q", outFirst)
	}
	outLast := r.FormatLineActive(lastIdx, 40, 0, 0)
	if !strings.Contains(outLast, "\x1b[58;2;143;157;106m") {
		t.Errorf("unflagged add anchor missing add-green SGR 58: %q", outLast)
	}
	if strings.Contains(outLast, "\x1b[58;2;108;108;108m") {
		t.Errorf("unflagged add anchor unexpectedly carries flag-grey SGR 58: %q", outLast)
	}
}

// TestFormatLineActive_FlaggedBorderIsGrey verifies that flagging the
// active hunk switches the SGR 58 boundary colour to flag grey on
// both the add and del anchors, replacing the green/red colours used
// when unflagged.
func TestFormatLineActive_FlaggedBorderIsGrey(t *testing.T) {
	r := Parse(sampleDiff, sampleDiff, "")
	r.SetFlaggedHunks([]int{0})
	firstIdx := r.HunkStarts[0]
	lastIdx := r.HunkEnds[0]
	outFirst := r.FormatLineActive(firstIdx, 40, 0, 0)
	if !strings.Contains(outFirst, "\x1b[58;2;108;108;108m") {
		t.Errorf("flagged anchor missing flag-grey SGR 58: %q", outFirst)
	}
	if strings.Contains(outFirst, "\x1b[58;2;207;106;76m") {
		t.Errorf("flagged anchor still carries del-red SGR 58: %q", outFirst)
	}
	outLast := r.FormatLineActive(lastIdx, 40, 0, 0)
	if !strings.Contains(outLast, "\x1b[58;2;108;108;108m") {
		t.Errorf("flagged anchor missing flag-grey SGR 58: %q", outLast)
	}
	if strings.Contains(outLast, "\x1b[58;2;143;157;106m") {
		t.Errorf("flagged anchor still carries add-green SGR 58: %q", outLast)
	}
}

// TestFormatLineActive_FlagToggleUpdatesBorder verifies that toggling
// flagged state changes the next-render border colour: setting the
// flag switches to grey, clearing it restores red/green.
func TestFormatLineActive_FlagToggleUpdatesBorder(t *testing.T) {
	r := Parse(sampleDiff, sampleDiff, "")
	firstIdx := r.HunkStarts[0]
	before := r.FormatLineActive(firstIdx, 40, 0, 0)
	if !strings.Contains(before, "\x1b[58;2;207;106;76m") {
		t.Fatalf("baseline del anchor missing red SGR 58: %q", before)
	}
	r.SetFlaggedHunks([]int{0})
	flagged := r.FormatLineActive(firstIdx, 40, 0, 0)
	if !strings.Contains(flagged, "\x1b[58;2;108;108;108m") {
		t.Errorf("flagged anchor missing grey SGR 58: %q", flagged)
	}
	r.SetFlaggedHunks(nil)
	after := r.FormatLineActive(firstIdx, 40, 0, 0)
	if !strings.Contains(after, "\x1b[58;2;207;106;76m") {
		t.Errorf("after clearing flags, del anchor should regain red SGR 58: %q", after)
	}
	if strings.Contains(after, "\x1b[58;2;108;108;108m") {
		t.Errorf("after clearing flags, del anchor still carries grey SGR 58: %q", after)
	}
}

// TestVisibleRows_NoFlags asserts that the visible-row sequence is a
// 1:1 mapping onto Lines when no hunks are flagged, and that
// HunkVisibleRange agrees with HunkRange.
func TestVisibleRows_NoFlags(t *testing.T) {
	r := Parse(sampleDiff, sampleDiff, "")
	if got, want := r.RowCount(), len(r.Lines); got != want {
		t.Errorf("RowCount = %d, want %d", got, want)
	}
	for i := 0; i < r.RowCount(); i++ {
		li, ok := r.RowLineIndex(i)
		if !ok || li != i {
			t.Errorf("RowLineIndex(%d) = (%d, %v), want (%d, true)", i, li, ok, i)
		}
	}
	for h := range r.HunkStarts {
		gotFirst, gotLast, ok := r.HunkVisibleRange(h)
		wantFirst, wantLast, wantOK := r.HunkRange(h)
		if ok != wantOK || gotFirst != wantFirst || gotLast != wantLast {
			t.Errorf("HunkVisibleRange(%d) = (%d, %d, %v), want HunkRange = (%d, %d, %v)",
				h, gotFirst, gotLast, ok, wantFirst, wantLast, wantOK)
		}
	}
}

// TestVisibleRows_MixedFlaggedHunkCollapsesAdds asserts that flagging
// sampleDiff's mixed hunk removes the Add rows from the visible-row
// sequence and leaves Del + Context rows intact.
func TestVisibleRows_MixedFlaggedHunkCollapsesAdds(t *testing.T) {
	r := Parse(sampleDiff, sampleDiff, "")
	// sampleDiff Lines: [ctx "one", del "two", add "TWO", ctx "three", add "four"]
	// Flagging the hunk drops both Adds → RowCount = 3.
	r.SetFlaggedHunks([]int{0})
	if got, want := r.RowCount(), 3; got != want {
		t.Fatalf("RowCount = %d, want %d", got, want)
	}
	wantLineIdx := []int{0, 1, 3} // ctx "one", del "two", ctx "three"
	for i, want := range wantLineIdx {
		li, ok := r.RowLineIndex(i)
		if !ok || li != want {
			t.Errorf("RowLineIndex(%d) = (%d, %v), want (%d, true)", i, li, ok, want)
		}
		if r.Lines[li].Kind == Add {
			t.Errorf("visible row %d resolves to an Add line — flagged Adds should be hidden", i)
		}
	}
	// HunkVisibleRange should anchor on the only visible changed line
	// (the Del at row 1).
	first, last, ok := r.HunkVisibleRange(0)
	if !ok || first != 1 || last != 1 {
		t.Errorf("HunkVisibleRange(0) = (%d, %d, %v), want (1, 1, true)", first, last, ok)
	}
}

// TestVisibleRows_PureDelFlaggedHunk: flagging a hunk that has only Del
// changed lines leaves every Del visible; HunkVisibleRange spans the
// first and last Del rows.
func TestVisibleRows_PureDelFlaggedHunk(t *testing.T) {
	in := `--- a/x
+++ b/x
@@ -1,3 +1,1 @@
 first
-removed-a
-removed-b
`
	r := Parse(in, in, "")
	if got, want := r.RowCount(), 3; got != want {
		t.Fatalf("RowCount = %d, want %d (parse: %d lines)", got, want, len(r.Lines))
	}
	r.SetFlaggedHunks([]int{0})
	if got, want := r.RowCount(), 3; got != want {
		t.Fatalf("RowCount after flag = %d, want %d", got, want)
	}
	first, last, ok := r.HunkVisibleRange(0)
	if !ok || first != 1 || last != 2 {
		t.Errorf("HunkVisibleRange(0) = (%d, %d, %v), want (1, 2, true)", first, last, ok)
	}
}

// TestVisibleRows_PureAddFlaggedHunkPlaceholder: flagging a hunk with
// only Add changed lines collapses the Adds into a single placeholder
// row; HunkVisibleRange returns that placeholder row index for both
// firstRow and lastRow.
func TestVisibleRows_PureAddFlaggedHunkPlaceholder(t *testing.T) {
	in := `--- a/x
+++ b/x
@@ -1,1 +1,3 @@
 first
+added-a
+added-b
`
	r := Parse(in, in, "")
	if got, want := r.RowCount(), 3; got != want {
		t.Fatalf("RowCount = %d, want %d", got, want)
	}
	r.SetFlaggedHunks([]int{0})
	// 1 context row + 1 placeholder row in place of the 2 Adds.
	if got, want := r.RowCount(), 2; got != want {
		t.Errorf("RowCount after flag = %d, want %d", got, want)
	}
	first, last, ok := r.HunkVisibleRange(0)
	if !ok || first != 1 || last != 1 {
		t.Errorf("HunkVisibleRange(0) = (%d, %d, %v), want (1, 1, true)", first, last, ok)
	}
}

// TestVisibleRows_MultipleFlaggedHunks: flag one of two hunks; the
// unflagged hunk's row range is unchanged (Adds still present); the
// flagged hunk's Add lines are absent from the visible-row sequence.
func TestVisibleRows_MultipleFlaggedHunks(t *testing.T) {
	in := `--- a/x
+++ b/x
@@ -1,1 +1,2 @@
 first
+added-1
@@ -10,2 +11,2 @@
-old-ten
+new-ten
 eleven
`
	r := Parse(in, in, "")
	totalLines := len(r.Lines)
	if r.RowCount() != totalLines {
		t.Fatalf("RowCount = %d, want %d (no flags)", r.RowCount(), totalLines)
	}
	r.SetFlaggedHunks([]int{0}) // flag first hunk only
	// First hunk is pure-Add: its one Add row is replaced by one
	// placeholder row, so RowCount stays the same as the unflagged total.
	if got, want := r.RowCount(), totalLines; got != want {
		t.Errorf("RowCount after flagging hunk 0 = %d, want %d", got, want)
	}
	// Second hunk's lines (Del, Add, Context) must all still resolve.
	secondAddPresent := false
	for i := 0; i < r.RowCount(); i++ {
		li, _ := r.RowLineIndex(i)
		if r.Lines[li].Kind == Add && li >= r.HunkStarts[1] {
			secondAddPresent = true
		}
	}
	if !secondAddPresent {
		t.Errorf("second-hunk Add line missing from visible rows after flagging hunk 0")
	}
	// First-hunk Add must NOT be present.
	for i := 0; i < r.RowCount(); i++ {
		li, _ := r.RowLineIndex(i)
		if r.Lines[li].Kind == Add && li < r.HunkStarts[1] {
			t.Errorf("flagged-hunk Add line at lineIdx=%d still visible (row %d)", li, i)
		}
	}
	// HunkVisibleRange for the unflagged hunk stays at its underlying
	// line positions: hunk 0 emitted a placeholder (one row in, one row
	// out), so hunk 1's [HunkStart=2, HunkEnd=3] remains at rows [2, 3].
	got1, got2, ok := r.HunkVisibleRange(1)
	if !ok || got1 != 2 || got2 != 3 {
		t.Errorf("HunkVisibleRange(1) = (%d, %d, %v), want (2, 3, true)", got1, got2, ok)
	}
}

// TestVisibleRows_UnflagRestoresRows: flagging then unflagging a hunk
// restores the full visible-row count and per-hunk visible range.
func TestVisibleRows_UnflagRestoresRows(t *testing.T) {
	r := Parse(sampleDiff, sampleDiff, "")
	originalCount := r.RowCount()
	r.SetFlaggedHunks([]int{0})
	if r.RowCount() == originalCount {
		t.Fatalf("flagging did not change RowCount")
	}
	r.SetFlaggedHunks(nil)
	if got := r.RowCount(); got != originalCount {
		t.Errorf("RowCount after unflag = %d, want %d", got, originalCount)
	}
	first, last, ok := r.HunkVisibleRange(0)
	wantFirst, wantLast, wantOK := r.HunkRange(0)
	if ok != wantOK || first != wantFirst || last != wantLast {
		t.Errorf("HunkVisibleRange(0) after unflag = (%d, %d, %v), want (%d, %d, %v)",
			first, last, ok, wantFirst, wantLast, wantOK)
	}
}

// TestFormatRow_ActiveBorderOnVisibleRow: when a flagged hunk's first
// visible row is a Del (its Adds collapsed out), FormatRow with that
// row index as the active hunk's first row emits the grey SGR 58
// overline+underline. The row index is the *visible*-row position, not
// the underlying line index.
func TestFormatRow_ActiveBorderOnVisibleRow(t *testing.T) {
	r := Parse(sampleDiff, sampleDiff, "")
	r.SetFlaggedHunks([]int{0})
	firstRow, lastRow, ok := r.HunkVisibleRange(0)
	if !ok {
		t.Fatalf("HunkVisibleRange(0) returned ok=false unexpectedly")
	}
	// sampleDiff flagged: the only visible changed row is the Del → first==last==1.
	if firstRow != lastRow {
		t.Fatalf("expected single-row range for flagged sampleDiff hunk, got [%d,%d]", firstRow, lastRow)
	}
	out := r.FormatRow(firstRow, 40, 0, 0)
	if !strings.Contains(out, "\x1b[58;2;108;108;108m") {
		t.Errorf("flagged active-row missing grey SGR 58: %q", out)
	}
	if !strings.Contains(out, "\x1b[53m") {
		t.Errorf("flagged active-row missing overline SGR 53 (first row): %q", out)
	}
	if !strings.Contains(out, "\x1b[4m") {
		t.Errorf("flagged active-row missing underline SGR 4 (last row): %q", out)
	}
}

// TestVisibleRows_PureAddPlaceholder: flagging a pure-Add hunk emits
// exactly one placeholder row in place of every collapsed Add, with
// HunkVisibleRange collapsed onto that single row.
func TestVisibleRows_PureAddPlaceholder(t *testing.T) {
	in := `--- a/x
+++ b/x
@@ -1,1 +1,4 @@
 first
+added-a
+added-b
+added-c
`
	r := Parse(in, in, "")
	if got, want := r.RowCount(), 4; got != want {
		t.Fatalf("RowCount before flag = %d, want %d", got, want)
	}
	r.SetFlaggedHunks([]int{0})
	// One context row + one placeholder row = 2 visible rows.
	if got, want := r.RowCount(), 2; got != want {
		t.Fatalf("RowCount after flag = %d, want %d", got, want)
	}
	// Row 0 is the context line; row 1 is the placeholder.
	if li, ok := r.RowLineIndex(0); !ok || r.Lines[li].Kind != Context {
		t.Errorf("row 0 should resolve to context line, got li=%d ok=%v", li, ok)
	}
	if _, ok := r.RowLineIndex(1); ok {
		t.Errorf("row 1 should be a placeholder (RowLineIndex returns ok=false)")
	}
	first, last, ok := r.HunkVisibleRange(0)
	if !ok || first != 1 || last != 1 {
		t.Errorf("HunkVisibleRange(0) = (%d, %d, %v), want (1, 1, true)", first, last, ok)
	}
}

// TestFormatRow_PlaceholderText: the placeholder row contains the
// "── N lines reverted ──" message with the right count.
func TestFormatRow_PlaceholderText(t *testing.T) {
	in := `--- a/x
+++ b/x
@@ -1,1 +1,4 @@
 first
+a
+b
+c
`
	r := Parse(in, in, "")
	r.SetFlaggedHunks([]int{0})
	first, _, ok := r.HunkVisibleRange(0)
	if !ok {
		t.Fatalf("HunkVisibleRange(0) returned ok=false")
	}
	out := r.FormatRow(first, 60, 0, 0)
	if !strings.Contains(out, "── 3 lines reverted ──") {
		t.Errorf("placeholder text missing: %q", out)
	}
	if !strings.Contains(out, styleOpen(flagBodyStyle)) {
		t.Errorf("placeholder missing flagBodyStyle escape: %q", out)
	}
	// Active anchor on the placeholder: both overline and underline,
	// in flag grey.
	if !strings.Contains(out, "\x1b[53m") {
		t.Errorf("placeholder active row missing overline SGR 53: %q", out)
	}
	if !strings.Contains(out, "\x1b[4m") {
		t.Errorf("placeholder active row missing underline SGR 4: %q", out)
	}
	if !strings.Contains(out, "\x1b[58;2;108;108;108m") {
		t.Errorf("placeholder active row missing flag-grey SGR 58: %q", out)
	}
}

// TestFormatRow_PlaceholderFullWidth: the placeholder row pads to the
// full requested width.
func TestFormatRow_PlaceholderFullWidth(t *testing.T) {
	in := `--- a/x
+++ b/x
@@ -1,1 +1,3 @@
 first
+a
+b
`
	r := Parse(in, in, "")
	r.SetFlaggedHunks([]int{0})
	first, _, ok := r.HunkVisibleRange(0)
	if !ok {
		t.Fatalf("HunkVisibleRange(0) returned ok=false")
	}
	const w = 60
	// Render without activeHunk so wrapActiveSGR doesn't interfere.
	out := r.FormatRow(first, w, 0, -1)
	if got := visibleCellLen(out); got != w {
		t.Errorf("placeholder visibleCellLen = %d, want %d (out=%q)", got, w, out)
	}
}

// visibleCellLen is visibleLen but rune-aware: it counts visible runes
// after stripping CSI escapes. Sufficient for content that mixes ASCII
// and single-width box-drawing characters.
func visibleCellLen(s string) int {
	stripped := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
			continue
		}
		stripped = append(stripped, s[i])
	}
	n := 0
	for range string(stripped) {
		n++
	}
	return n
}

// TestVisibleRows_MixedFlaggedHunkNoPlaceholder: flagging a mixed
// hunk (with at least one Del) must NOT emit a placeholder — the
// remaining Del rows already provide the flagged signal.
func TestVisibleRows_MixedFlaggedHunkNoPlaceholder(t *testing.T) {
	r := Parse(sampleDiff, sampleDiff, "")
	r.SetFlaggedHunks([]int{0})
	for i := 0; i < r.RowCount(); i++ {
		if _, ok := r.RowLineIndex(i); !ok {
			t.Errorf("row %d unexpectedly resolves as placeholder in a mixed flagged hunk", i)
		}
	}
}

// TestVisibleRows_UnflagRemovesPlaceholder: unflagging a pure-Add hunk
// drops the placeholder and restores the original Add rows.
func TestVisibleRows_UnflagRemovesPlaceholder(t *testing.T) {
	in := `--- a/x
+++ b/x
@@ -1,1 +1,3 @@
 first
+a
+b
`
	r := Parse(in, in, "")
	original := r.RowCount()
	r.SetFlaggedHunks([]int{0})
	r.SetFlaggedHunks(nil)
	if got := r.RowCount(); got != original {
		t.Errorf("RowCount after unflag = %d, want %d", got, original)
	}
	for i := 0; i < r.RowCount(); i++ {
		if _, ok := r.RowLineIndex(i); !ok {
			t.Errorf("row %d still a placeholder after unflag", i)
		}
	}
}

// TestFormatRow_MatchesFormatLineActiveWhenUnflagged: with no flags
// applied, FormatRow at row r should produce the same bytes as
// FormatLineActive at line r.
func TestFormatRow_MatchesFormatLineActiveWhenUnflagged(t *testing.T) {
	r := Parse(sampleDiff, sampleDiff, "")
	for i := 0; i < r.RowCount(); i++ {
		got := r.FormatRow(i, 60, 0, 0)
		want := r.FormatLineActive(i, 60, 0, 0)
		if got != want {
			t.Errorf("row %d: FormatRow != FormatLineActive\n got: %q\nwant: %q", i, got, want)
		}
	}
}

// gutterFor renders Lines[idx] at a width that leaves no body cells
// and returns the printable text (with ANSI escapes stripped) so a
// test can read the gutter columns verbatim.
func gutterFor(t *testing.T, r Result, idx int) string {
	t.Helper()
	w := r.GutterRenderWidth()
	out := r.FormatLine(idx, w, 0)
	return stripANSI(out)
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// gutterNewNumStr extracts the right-side NewNum text from a rendered
// gutter (after ANSI stripping). The gutter format is "<oldW> <newW> "
// (trailing space). With OldW and NewW known, we slice the NewNum
// field directly. Trailing/leading spaces inside the NewNum field are
// stripped so the result is either the digits or "".
func gutterNewNumStr(t *testing.T, r Result, idx int) string {
	t.Helper()
	g := gutterFor(t, r, idx)
	// Expected layout: OldW chars, one space, NewW chars, one trailing space.
	want := r.OldW + 1 + r.NewW + 1
	if len(g) < want {
		t.Fatalf("gutter %q shorter than expected width %d", g, want)
	}
	newField := g[r.OldW+1 : r.OldW+1+r.NewW]
	return strings.TrimSpace(newField)
}

// TestFormatLine_HideNewNumWhenEqualNoFlags: with no flagging, context
// lines whose adjusted NewNum equals OldNum hide the NewNum column,
// while context after a shift-introducing change shows it.
func TestFormatLine_HideNewNumWhenEqualNoFlags(t *testing.T) {
	in := `--- a/x
+++ b/x
@@ -1,4 +1,5 @@
 ctx-1
 ctx-2
+added
 ctx-3
 ctx-4
`
	r := Parse(in, in, "")
	// Lines: [ctx-1(1,1), ctx-2(2,2), +added(0,3), ctx-3(3,4), ctx-4(4,5)]
	// Pre-shift context lines: NewNum == OldNum → hidden.
	for _, i := range []int{0, 1} {
		if got := gutterNewNumStr(t, r, i); got != "" {
			t.Errorf("pre-shift ctx Lines[%d]: NewNum field = %q, want empty (NewNum == OldNum)", i, got)
		}
	}
	// Added line: NewNum shown.
	if got := gutterNewNumStr(t, r, 2); got != "3" {
		t.Errorf("Add Lines[2]: NewNum field = %q, want %q", got, "3")
	}
	// Post-shift context: NewNum != OldNum, shown.
	if got := gutterNewNumStr(t, r, 3); got != "4" {
		t.Errorf("post-shift ctx Lines[3]: NewNum field = %q, want %q", got, "4")
	}
	if got := gutterNewNumStr(t, r, 4); got != "5" {
		t.Errorf("post-shift ctx Lines[4]: NewNum field = %q, want %q", got, "5")
	}
}

// TestFormatLine_FlaggedPureAddAtTopHidesAllNewNums: flagging a leading
// pure-Add hunk drops its net shift, so every later context line's
// adjusted NewNum equals OldNum and is hidden — the regression case the
// PRD was written to fix.
func TestFormatLine_FlaggedPureAddAtTopHidesAllNewNums(t *testing.T) {
	in := `--- a/x
+++ b/x
@@ -1,3 +1,5 @@
+added-a
+added-b
 ctx-1
 ctx-2
 ctx-3
`
	r := Parse(in, in, "")
	r.SetFlaggedHunks([]int{0})
	// After flagging the pure-Add hunk, the post-revert file is just
	// ctx-1/ctx-2/ctx-3 with NewNum == OldNum for each. With the
	// hide-when-equal rule, every context NewNum is hidden.
	for i, ln := range r.Lines {
		if ln.Kind != Context {
			continue
		}
		if got := gutterNewNumStr(t, r, i); got != "" {
			t.Errorf("ctx Lines[%d]: NewNum field = %q, want empty (post-revert equals OldNum)", i, got)
		}
	}
}

// TestFormatLine_FlaggedMidHunkAfterPriorShift: with an unflagged Add
// hunk followed by a flagged hunk, context between the two carries the
// prior +1 shift, and context after the flagged hunk shows the *same*
// shift — the flagged hunk contributes zero.
func TestFormatLine_FlaggedMidHunkAfterPriorShift(t *testing.T) {
	in := `--- a/x
+++ b/x
@@ -1,2 +1,3 @@
 ctx-1
+added-a
 ctx-2
@@ -10,2 +11,3 @@
 ctx-x
+add-Z
 ctx-y
`
	r := Parse(in, in, "")
	if len(r.HunkStarts) != 2 {
		t.Fatalf("expected 2 hunks, got %d", len(r.HunkStarts))
	}
	// Lines: [ctx-1(1,1), +added-a(0,2), ctx-2(2,3), ctx-x(10,11), +add-Z(0,12), ctx-y(11,13)]
	// idx     0           1               2           3              4              5
	// Hunk 0 unflagged shifts by +1 (one Add). Hunk 1 flagged contributes 0 net shift.
	// ctx-y (idx=5): Old=11, adj=12 (prior +1 carries through the flagged hunk).
	// Unflagged ctx-y: Old=11, adj=13 (both hunks shift +1, total +2).
	r.SetFlaggedHunks([]int{1})
	wantAdj := map[int]string{
		0: "",   // ctx-1 Old=1, adj=1, hide
		2: "3",  // ctx-2 Old=2, adj=3, show
		3: "11", // ctx-x Old=10, adj=11, show
		5: "12", // ctx-y Old=11, adj=12, show
	}
	for i, want := range wantAdj {
		if got := gutterNewNumStr(t, r, i); got != want {
			t.Errorf("ctx Lines[%d]: NewNum field = %q, want %q", i, got, want)
		}
	}
	// Sanity: unflagged ctx-y has adj=13, not 12 — confirms the flagged
	// hunk's contribution actually dropped out.
	r.SetFlaggedHunks(nil)
	if got := gutterNewNumStr(t, r, 5); got != "13" {
		t.Errorf("unflagged ctx-y: NewNum field = %q, want %q", got, "13")
	}
}

// TestFormatLine_FlagUnflagRoundTrip: SetFlaggedHunks(...) then
// SetFlaggedHunks(nil) restores the same gutter rendering as the freshly
// parsed Result.
func TestFormatLine_FlagUnflagRoundTrip(t *testing.T) {
	in := `--- a/x
+++ b/x
@@ -1,3 +1,4 @@
 ctx-1
+added-a
 ctx-2
 ctx-3
`
	r := Parse(in, in, "")
	baseline := make([]string, len(r.Lines))
	for i := range r.Lines {
		baseline[i] = gutterFor(t, r, i)
	}
	r.SetFlaggedHunks([]int{0})
	r.SetFlaggedHunks(nil)
	for i := range r.Lines {
		if got := gutterFor(t, r, i); got != baseline[i] {
			t.Errorf("Lines[%d] gutter after flag/unflag = %q, want %q (baseline)", i, got, baseline[i])
		}
	}
}

// TestFormatLine_FlaggedMixedHunkZeroNetShift: a flagged hunk with
// unequal adds and dels still contributes zero net shift after revert,
// so later context's adjusted NewNum reflects the pre-image position.
func TestFormatLine_FlaggedMixedHunkZeroNetShift(t *testing.T) {
	in := `--- a/x
+++ b/x
@@ -1,3 +1,4 @@
 ctx-1
-del-A
+add-A
+add-B
 ctx-2
`
	r := Parse(in, in, "")
	// Unflagged: ctx-2 Old=3, NewNum=4 (shift +1). Adjusted = 4 → shown.
	if got := gutterNewNumStr(t, r, 4); got != "4" {
		t.Errorf("unflagged ctx-2: NewNum field = %q, want %q", got, "4")
	}
	r.SetFlaggedHunks([]int{0})
	// Flagged: zero net shift, ctx-2 adj = Old = 3 → hidden.
	if got := gutterNewNumStr(t, r, 4); got != "" {
		t.Errorf("flagged ctx-2: NewNum field = %q, want empty (post-revert == OldNum)", got)
	}
}

// TestFormatLine_FlaggedDelShowsOnlyWhenShifted: a flagged Del with no
// prior unflagged shift has its adjusted NewNum equal to OldNum (hidden);
// a flagged Del placed after a prior unflagged Add hunk has a shifted
// adjusted NewNum (shown).
func TestFormatLine_FlaggedDelShowsOnlyWhenShifted(t *testing.T) {
	// Case (a): flagged pure-Del hunk in isolation.
	inA := `--- a/x
+++ b/x
@@ -1,3 +1,1 @@
 ctx-1
-del-A
-del-B
`
	rA := Parse(inA, inA, "")
	rA.SetFlaggedHunks([]int{0})
	// del-A Old=2 adj=2 → hide; del-B Old=3 adj=3 → hide.
	for _, i := range []int{1, 2} {
		if got := gutterNewNumStr(t, rA, i); got != "" {
			t.Errorf("case (a) flagged Del Lines[%d]: NewNum field = %q, want empty", i, got)
		}
	}

	// Case (b): unflagged Add hunk, then flagged pure-Del hunk.
	inB := `--- a/x
+++ b/x
@@ -1,1 +1,2 @@
 ctx-1
+add-A
@@ -10,2 +11,1 @@
 ctx-x
-del-Z
`
	rB := Parse(inB, inB, "")
	if len(rB.HunkStarts) != 2 {
		t.Fatalf("case (b): expected 2 hunks, got %d", len(rB.HunkStarts))
	}
	rB.SetFlaggedHunks([]int{1})
	// Lines: [ctx-1(1,1), +add-A(0,2), ctx-x(10,11), -del-Z(11,0)]
	// idx     0           1            2             3
	// At hunk-1 seed: nextOld=10 nextNew=11.
	// ctx-x: adj=11. del-Z flagged: adj=12.
	// del-Z Old=11, adj=12 → show "12".
	if got := gutterNewNumStr(t, rB, 3); got != "12" {
		t.Errorf("case (b) flagged Del with prior shift: NewNum field = %q, want %q", got, "12")
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

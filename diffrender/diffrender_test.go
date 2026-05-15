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
	// hScroll=4 should drop the first 4 chars of "abcdefghij" → "efghij".
	if !strings.Contains(out, "efghij") {
		t.Errorf("expected 'efghij' after hScroll=4: %q", out)
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
	r := Parse(tsDiff, "a.ts")
	for i := range r.Lines {
		out := r.FormatLine(i, 120, 0)
		if strings.ContainsAny(out, "\n\r") {
			t.Errorf("FormatLine(%d) contains a newline or CR — diff row will spill into the next terminal row: %q", i, out)
		}
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
	r := Parse(sampleDiff, "")
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
	r := Parse(sampleDiff, "")
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
	r := Parse(sampleDiff, "")
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
	r := Parse(sampleDiff, "")
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
	r := Parse(sampleDiff, "")
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
	r := Parse(sampleDiff, "")
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
	r := Parse(goDiff, "x.go")
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

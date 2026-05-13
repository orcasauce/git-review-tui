// Package diffrender parses unified-diff text into structured rows and
// formats them as ANSI-colored lines for the diff panel.
//
// Input is a raw unified diff (typically from `git show -U99999` or
// `git diff -U99999`) for a single file. Output is a [Result] holding
// the in-hunk lines (context / add / delete) along with their old and
// new line numbers, plus an index marking the start of each underlying
// hunk so the diff panel can jump between changes. The "@@" hunk header
// lines and the "diff --git", "index", "---", "+++" preamble lines are
// dropped from display.
//
// When Parse is called with a non-empty filename whose extension chroma
// recognizes, each Line gets a slice of styled [segment]s so the diff
// panel can render syntax-highlighted code. The "+" / "-" marker keeps
// its diff color; the body content uses chroma's token colors. For
// unknown extensions (or an empty filename) the body falls back to the
// plain diff coloring used in earlier slices.
package diffrender

import (
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"
)

// Kind classifies a single rendered diff line.
type Kind int

const (
	Context Kind = iota
	Add
	Del
)

// segment is one styled run of text within a Line's body. Internal:
// the diff panel renders via [Result.FormatLine] and does not inspect
// segments directly.
type segment struct {
	style lipgloss.Style
	text  string
}

// Line is one rendered row of the diff.
//
// OldNum is the line number in the pre-image and is zero for added
// lines; NewNum is the line number in the post-image and is zero for
// deleted lines. Text is the raw line content with the leading
// `+`/`-`/` ` marker stripped.
type Line struct {
	Kind   Kind
	OldNum int
	NewNum int
	Text   string
	segs   []segment
}

// Result is the parsed diff.
type Result struct {
	Lines []Line
	// HunkStarts holds, for each "@@" hunk header in the input, the
	// index into Lines at which that hunk's first content line lives.
	// Used by the diff panel to jump to next / previous change.
	HunkStarts []int
	// OldW / NewW are the column widths needed to render the gutter for
	// the largest line numbers seen.
	OldW int
	NewW int
	// highlighted is true when chroma identified a lexer for the
	// filename passed to Parse and produced styled segments.
	highlighted bool
}

// Parse parses a raw unified diff into a Result. The filename is used
// only as a hint for chroma's language detection (typically the path
// the diff applies to); an empty string disables syntax highlighting.
func Parse(raw, filename string) Result {
	var lines []Line
	var hunks []int
	var oldN, newN int
	inHunk := false
	for _, ln := range strings.Split(raw, "\n") {
		if strings.HasPrefix(ln, "@@") {
			oldN, newN = parseHunkHeader(ln)
			hunks = append(hunks, len(lines))
			inHunk = true
			continue
		}
		if !inHunk {
			continue
		}
		if ln == "" {
			continue
		}
		if ln[0] == '\\' {
			continue
		}
		text := ln[1:]
		switch ln[0] {
		case '+':
			lines = append(lines, Line{Kind: Add, NewNum: newN, Text: text})
			newN++
		case '-':
			lines = append(lines, Line{Kind: Del, OldNum: oldN, Text: text})
			oldN++
		case ' ':
			lines = append(lines, Line{Kind: Context, OldNum: oldN, NewNum: newN, Text: text})
			oldN++
			newN++
		}
	}
	r := Result{Lines: lines, HunkStarts: hunks}
	for _, l := range lines {
		if l.OldNum > r.OldW {
			r.OldW = l.OldNum
		}
		if l.NewNum > r.NewW {
			r.NewW = l.NewNum
		}
	}
	r.OldW = numWidth(r.OldW)
	r.NewW = numWidth(r.NewW)

	if lex, style, ok := chromaFor(filename); ok {
		r.highlighted = true
		for i := range r.Lines {
			r.Lines[i].segs = highlightLine(lex, style, r.Lines[i].Text)
		}
	}
	return r
}

func parseHunkHeader(s string) (oldN, newN int) {
	oldN, newN = 1, 1
	for _, p := range strings.Fields(s) {
		if len(p) < 2 {
			continue
		}
		switch p[0] {
		case '-':
			oldN = headerStart(p[1:])
		case '+':
			newN = headerStart(p[1:])
		}
	}
	return
}

func headerStart(s string) int {
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[:i]
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 1
	}
	return n
}

func numWidth(n int) int {
	if n <= 0 {
		return 1
	}
	w := 0
	for n > 0 {
		n /= 10
		w++
	}
	return w
}

var (
	addStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	delStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	gutterStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	// markerAddStyle and markerDelStyle color just the leading +/- in
	// highlighted mode; the body uses chroma colors.
	markerAddStyle = addStyle
	markerDelStyle = delStyle
)

// chromaStyle is the chroma color theme used for syntax highlighting.
// Picked once at package init so terminal output stays consistent.
var chromaStyle = func() *chroma.Style {
	if s := styles.Get("native"); s != nil {
		return s
	}
	return styles.Fallback
}()

// chromaFor returns the lexer and style to use for filename, or
// ok=false when chroma should be skipped (empty filename or unknown
// extension).
func chromaFor(filename string) (chroma.Lexer, *chroma.Style, bool) {
	if filename == "" {
		return nil, nil, false
	}
	lex := lexers.Match(filename)
	if lex == nil || lex == lexers.Fallback {
		return nil, nil, false
	}
	return lex, chromaStyle, true
}

// highlightLine tokenizes a single line of text and converts each
// token to a styled segment. Lexer state does not persist across
// lines, which keeps the implementation simple at the cost of some
// inaccuracy inside multi-line strings or comments.
func highlightLine(lex chroma.Lexer, style *chroma.Style, text string) []segment {
	it, err := lex.Tokenise(nil, text)
	if err != nil {
		return nil
	}
	var segs []segment
	for _, tok := range it.Tokens() {
		if tok.Value == "" {
			continue
		}
		s := styleFor(style, tok.Type)
		segs = append(segs, segment{style: s, text: tok.Value})
	}
	return segs
}

// styleCache memoizes the lipgloss.Style for each chroma TokenType so
// FormatLine doesn't rebuild styles per token.
var styleCache = map[chroma.TokenType]lipgloss.Style{}

func styleFor(style *chroma.Style, tt chroma.TokenType) lipgloss.Style {
	if s, ok := styleCache[tt]; ok {
		return s
	}
	entry := style.Get(tt)
	s := lipgloss.NewStyle()
	if entry.Colour.IsSet() {
		s = s.Foreground(lipgloss.Color(entry.Colour.String()))
	}
	if entry.Bold == chroma.Yes {
		s = s.Bold(true)
	}
	if entry.Italic == chroma.Yes {
		s = s.Italic(true)
	}
	styleCache[tt] = s
	return s
}

// GutterRenderWidth returns the cell-count width of the gutter as
// produced by FormatLine: "<oldW> <newW> ".
func (r Result) GutterRenderWidth() int {
	return r.OldW + 1 + r.NewW + 1
}

// FormatLine renders Lines[idx] to a single ANSI-styled string of
// approximately `width` cells. hScroll skips that many leading cells of
// the text content (not the gutter). If width is smaller than the
// gutter, only the gutter is returned.
func (r Result) FormatLine(idx, width, hScroll int) string {
	if idx < 0 || idx >= len(r.Lines) {
		return ""
	}
	l := r.Lines[idx]
	gutter := r.formatGutter(l)
	gutterCells := r.GutterRenderWidth()
	textW := width - gutterCells
	if textW < 0 {
		textW = 0
	}
	if l.segs != nil {
		return gutter + formatHighlightedBody(l, textW, hScroll)
	}
	return gutter + formatPlainBody(l, textW, hScroll)
}

func formatPlainBody(l Line, textW, hScroll int) string {
	marker := " "
	switch l.Kind {
	case Add:
		marker = "+"
	case Del:
		marker = "-"
	}
	text := l.Text
	if hScroll > 0 {
		runes := []rune(text)
		if hScroll >= len(runes) {
			text = ""
		} else {
			text = string(runes[hScroll:])
		}
	}
	body := marker + text
	if len(body) > textW {
		body = body[:textW]
	} else if textW > len(body) {
		body += strings.Repeat(" ", textW-len(body))
	}
	switch l.Kind {
	case Add:
		body = addStyle.Render(body)
	case Del:
		body = delStyle.Render(body)
	}
	return body
}

// formatHighlightedBody renders the marker (in diff color) followed by
// the chroma-tokenized body. hScroll skips cells of the content; width
// truncates the full marker+content to textW cells.
func formatHighlightedBody(l Line, textW, hScroll int) string {
	if textW == 0 {
		return ""
	}
	var marker string
	switch l.Kind {
	case Add:
		marker = markerAddStyle.Render("+")
	case Del:
		marker = markerDelStyle.Render("-")
	default:
		marker = " "
	}
	// remaining cells available for the body content after the marker.
	bodyW := textW - 1
	if bodyW < 0 {
		bodyW = 0
	}

	var b strings.Builder
	b.WriteString(marker)
	cells := 0      // cells emitted into body
	skipped := 0    // text cells skipped due to hScroll
	for _, seg := range l.segs {
		if cells >= bodyW {
			break
		}
		runes := []rune(seg.text)
		// skip leading runes for hScroll
		if skipped < hScroll {
			drop := hScroll - skipped
			if drop >= len(runes) {
				skipped += len(runes)
				continue
			}
			runes = runes[drop:]
			skipped += drop
		}
		if len(runes) == 0 {
			continue
		}
		// truncate to remaining width
		if len(runes) > bodyW-cells {
			runes = runes[:bodyW-cells]
		}
		b.WriteString(seg.style.Render(string(runes)))
		cells += len(runes)
	}
	// pad to textW (1 for marker + bodyW).
	if cells < bodyW {
		b.WriteString(strings.Repeat(" ", bodyW-cells))
	}
	return b.String()
}

func (r Result) formatGutter(l Line) string {
	oldS := ""
	if l.OldNum > 0 {
		oldS = strconv.Itoa(l.OldNum)
	}
	newS := ""
	if l.NewNum > 0 {
		newS = strconv.Itoa(l.NewNum)
	}
	return gutterStyle.Render(padLeft(oldS, r.OldW) + " " + padLeft(newS, r.NewW) + " ")
}

func padLeft(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return strings.Repeat(" ", w-len(s)) + s
}

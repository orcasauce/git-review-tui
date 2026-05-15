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
// panel can render syntax-highlighted code. The body content uses
// chroma's token colors. For unknown extensions (or an empty filename)
// the body falls back to the plain diff coloring used in earlier slices.
package diffrender

import (
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
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

	if lex, ok := chromaFor(filename); ok {
		r.highlighted = true
		for i := range r.Lines {
			r.Lines[i].segs = highlightLine(lex, r.Lines[i].Text)
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

// Base16-twilight palette entries used for the add/del row banding.
// addGutterBg / delGutterBg are base0B / base08 (saturated). addBodyBg /
// delBodyBg are those hues darkened ~60% to match nvim's DiffAdd /
// DiffDelete construction. invertedFg is base00, the terminal-background
// colour, used as foreground on the saturated gutter band.
const (
	addGutterBg = "#8f9d6a"
	delGutterBg = "#cf6a4c"
	addBodyBg   = "#393e2a"
	delBodyBg   = "#522a1e"
	invertedFg  = "#1e1e1e"
)

var (
	gutterStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	addGutterBandStyle = lipgloss.NewStyle().
				Background(lipgloss.Color(addGutterBg)).
				Foreground(lipgloss.Color(invertedFg))
	delGutterBandStyle = lipgloss.NewStyle().
				Background(lipgloss.Color(delGutterBg)).
				Foreground(lipgloss.Color(invertedFg))
	addBodyBgStyle = lipgloss.NewStyle().Background(lipgloss.Color(addBodyBg))
	delBodyBgStyle = lipgloss.NewStyle().Background(lipgloss.Color(delBodyBg))
)

// chromaFor returns the lexer to use for filename, or ok=false when
// chroma should be skipped (empty filename or unknown extension).
func chromaFor(filename string) (chroma.Lexer, bool) {
	if filename == "" {
		return nil, false
	}
	lex := lexers.Match(filename)
	if lex == nil || lex == lexers.Fallback {
		return nil, false
	}
	return lex, true
}

// highlightLine tokenizes a single line of text and converts each
// token to a styled segment. Lexer state does not persist across
// lines, which keeps the implementation simple at the cost of some
// inaccuracy inside multi-line strings or comments.
//
// Token values are stripped of newlines and carriage returns: some
// chroma lexers (notably the JavaScript/TypeScript family) include the
// trailing "\n" in single-line-comment tokens because line comments
// are defined to extend through the LF. Letting that newline reach the
// renderer turns one diff row into two terminal rows, overshoots the
// panel height, and ultimately causes bubbletea to drop the top row
// of the screen.
func highlightLine(lex chroma.Lexer, text string) []segment {
	it, err := lex.Tokenise(nil, text)
	if err != nil {
		return nil
	}
	var segs []segment
	for _, tok := range it.Tokens() {
		v := stripLineBreaks(tok.Value)
		if v == "" {
			continue
		}
		s := styleFor(tok.Type)
		segs = append(segs, segment{style: s, text: v})
	}
	return segs
}

func stripLineBreaks(s string) string {
	if !strings.ContainsAny(s, "\n\r") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// tokenColors maps chroma token categories to ANSI 16-palette indices.
// The terminal theme decides the actual colour each index renders as, so
// highlighted code inherits whatever palette the user has configured at
// the terminal level (e.g. base16-twilight) rather than baking in chroma's
// own hex values. Token types not present here render with no foreground
// (terminal default) — that covers operators, punctuation, and anything
// else not on the contract list.
var tokenColors = map[chroma.TokenType]string{
	chroma.Comment:       "8",
	chroma.Keyword:       "4",
	chroma.LiteralString: "2",
	chroma.LiteralNumber: "3",
	chroma.NameClass:     "6",
	chroma.KeywordType:   "6",
	chroma.NameFunction:  "5",
	chroma.NameBuiltin:   "1",
	chroma.NameConstant:  "1",
}

// styleCache memoizes the lipgloss.Style for each chroma TokenType so
// FormatLine doesn't rebuild styles per token.
var styleCache = map[chroma.TokenType]lipgloss.Style{}

// styleFor resolves a chroma TokenType to its lipgloss.Style by walking
// the chroma category chain (exact → subcategory → category), mirroring
// chroma's own style-resolution rule. Bold/italic are intentionally
// dropped: the palette colour alone carries the cue.
func styleFor(tt chroma.TokenType) lipgloss.Style {
	if s, ok := styleCache[tt]; ok {
		return s
	}
	s := lipgloss.NewStyle()
	if c, ok := tokenColors[tt]; ok {
		s = s.Foreground(lipgloss.Color(c))
	} else if c, ok := tokenColors[tt.SubCategory()]; ok {
		s = s.Foreground(lipgloss.Color(c))
	} else if c, ok := tokenColors[tt.Category()]; ok {
		s = s.Foreground(lipgloss.Color(c))
	}
	styleCache[tt] = s
	return s
}

// GutterRenderWidth returns the cell-count width of the gutter as
// produced by FormatLine: "<oldW> <newW> ". The trailing space keeps
// the row's leading colour block visually separated from the body on
// add/del rows.
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
	bodyW := width - r.GutterRenderWidth()
	if bodyW < 0 {
		bodyW = 0
	}
	if l.segs != nil {
		return gutter + formatHighlightedBody(l, bodyW, hScroll)
	}
	return gutter + formatPlainBody(l, bodyW, hScroll)
}

func formatPlainBody(l Line, bodyW, hScroll int) string {
	text := l.Text
	if hScroll > 0 {
		runes := []rune(text)
		if hScroll >= len(runes) {
			text = ""
		} else {
			text = string(runes[hScroll:])
		}
	}
	body := text
	if len(body) > bodyW {
		body = body[:bodyW]
	} else if bodyW > len(body) {
		body += strings.Repeat(" ", bodyW-len(body))
	}
	switch l.Kind {
	case Add:
		return addBodyBgStyle.Render(body)
	case Del:
		return delBodyBgStyle.Render(body)
	}
	return body
}

// formatHighlightedBody renders the chroma-tokenized body. hScroll skips
// cells of the content; bodyW truncates to that many cells. On add/del
// rows the body tint is composed onto each segment's chroma style so the
// per-token foreground renders over the tint.
func formatHighlightedBody(l Line, bodyW, hScroll int) string {
	if bodyW == 0 {
		return ""
	}
	bodyBg := ""
	switch l.Kind {
	case Add:
		bodyBg = addBodyBg
	case Del:
		bodyBg = delBodyBg
	}

	var b strings.Builder
	cells := 0
	skipped := 0
	for _, seg := range l.segs {
		if cells >= bodyW {
			break
		}
		runes := []rune(seg.text)
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
		if len(runes) > bodyW-cells {
			runes = runes[:bodyW-cells]
		}
		s := seg.style
		if bodyBg != "" {
			s = s.Background(lipgloss.Color(bodyBg))
		}
		b.WriteString(s.Render(string(runes)))
		cells += len(runes)
	}
	if cells < bodyW {
		pad := strings.Repeat(" ", bodyW-cells)
		switch l.Kind {
		case Add:
			b.WriteString(addBodyBgStyle.Render(pad))
		case Del:
			b.WriteString(delBodyBgStyle.Render(pad))
		default:
			b.WriteString(pad)
		}
	}
	return b.String()
}

// formatGutter renders the gutter band: two line-number slots with a
// trailing separator space. On add/del rows the whole band carries a
// saturated background and an inverted foreground so the digits read
// as one coloured tag — the row's banded body background carries the
// add/del signal, so no `+`/`-` glyph is needed. On context rows the
// band keeps the muted gutterStyle foreground over the default
// background.
func (r Result) formatGutter(l Line) string {
	oldS := ""
	if l.OldNum > 0 {
		oldS = strconv.Itoa(l.OldNum)
	}
	newS := ""
	if l.NewNum > 0 {
		newS = strconv.Itoa(l.NewNum)
	}
	nums := padLeft(oldS, r.OldW) + " " + padLeft(newS, r.NewW) + " "
	switch l.Kind {
	case Add:
		return addGutterBandStyle.Render(nums)
	case Del:
		return delGutterBandStyle.Render(nums)
	default:
		return gutterStyle.Render(nums)
	}
}

func padLeft(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return strings.Repeat(" ", w-len(s)) + s
}

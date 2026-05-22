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

// visRow is one entry in the visible-row view of a Result. A row is
// either a reference to a Lines index (lineIdx >= 0) or a synthetic
// placeholder standing in for the collapsed Add block of a flagged
// pure-Add hunk (lineIdx < 0, hunkIdx names the hunk, collapsedAdds is
// the number of Add lines hidden by the placeholder).
type visRow struct {
	lineIdx       int
	hunkIdx       int
	collapsedAdds int
}

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
	// HunkStarts and HunkEnds hold, for each real change-region in the
	// diff, the indices into Lines of the first and last changed (+/-)
	// line that belongs to that hunk. Hunks are identified using git's
	// default-context diff output (passed as hunksDiff to Parse), so
	// boundaries match what `git diff` would naturally show — Lines
	// itself is built from a full-file `-U99999` diff for rendering.
	// Used by the diff panel to jump to next / previous change and to
	// frame the active hunk in the viewport.
	HunkStarts []int
	HunkEnds   []int
	// OldW / NewW are the column widths needed to render the gutter for
	// the largest line numbers seen.
	OldW int
	NewW int
	// highlighted is true when chroma identified a lexer for the
	// filename passed to Parse and produced styled segments.
	highlighted bool
	// flagged is parallel to Lines: flagged[i]=true means line i is
	// part of a hunk flagged for revert and should render as a grey
	// tombstone. Populated via SetFlaggedHunks; nil disables flagging.
	flagged []bool
	// visibleRows is the row-indexed view of Lines. Each entry is
	// either a reference to a Lines index, or a synthetic placeholder
	// row standing in for a collapsed Add block in a flagged pure-Add
	// hunk. Rows whose underlying line is collapsed (Add lines inside a
	// flagged mixed hunk) are absent. Populated by Parse and recomputed
	// whenever SetFlaggedHunks is called.
	visibleRows []visRow
	// hunkVisStart and hunkVisEnd parallel HunkStarts / HunkEnds and
	// hold the first and last visible-row indices of each hunk's
	// changed (+/-) lines. A value of -1 means the hunk has no visible
	// changed rows (currently only possible for a flagged pure-Add
	// hunk, whose Adds are all collapsed).
	hunkVisStart []int
	hunkVisEnd   []int
	// adjustedNewNum is parallel to Lines and holds, for each line, its
	// position in the post-revert new file (the file the user gets if
	// the currently flagged hunks are reverted). Zero means the line
	// does not exist in the post-revert file (an unflagged Add or a
	// flagged Del placeholder that has nothing to anchor to). Rebuilt
	// by recomputeVisibleRows whenever flagging changes.
	adjustedNewNum []int
	// rawHunkStarts / rawHunkOldN / rawHunkNewN record the line index
	// of each `@@` hunk's first content line in Lines, plus the parsed
	// pre-image / post-image starting positions from that hunk header.
	// Used to re-seed the adjustedNewNum walk at every @@ boundary so
	// the recompute survives multi-hunk diffs.
	rawHunkStarts []int
	rawHunkOldN   []int
	rawHunkNewN   []int
}

// SetFlaggedHunks marks every line within the given hunk indices as
// flagged for revert. Subsequent FormatLine/FormatLineActive calls
// render those lines as a single grey "tombstone" block, distinct
// from the add (green) and del (red) styling. Out-of-range indices
// are silently ignored. Passing nil or an empty slice clears all
// flagging. Also rebuilds the visible-row view so Add lines in
// flagged hunks drop out of the diff body.
func (r *Result) SetFlaggedHunks(indices []int) {
	if len(indices) == 0 {
		r.flagged = nil
	} else {
		r.flagged = make([]bool, len(r.Lines))
		for _, idx := range indices {
			if idx < 0 || idx >= len(r.HunkStarts) {
				continue
			}
			first := r.HunkStarts[idx]
			last := first
			if idx < len(r.HunkEnds) {
				last = r.HunkEnds[idx]
			}
			if last < first {
				last = first
			}
			for i := first; i <= last && i < len(r.flagged); i++ {
				r.flagged[i] = true
			}
		}
	}
	r.recomputeVisibleRows()
}

// recomputeVisibleRows rebuilds the visible-row view from Lines and the
// current flagged state. Add lines inside a flagged hunk are dropped
// from the sequence; everything else is included 1:1. A flagged hunk
// with zero Del lines gets a single placeholder row in place of its
// collapsed Adds, so the flagged signal stays visible. Per-hunk first /
// last visible *changed* row indices are recorded in parallel for the
// active-hunk frame to anchor against.
func (r *Result) recomputeVisibleRows() {
	if cap(r.visibleRows) < len(r.Lines) {
		r.visibleRows = make([]visRow, 0, len(r.Lines))
	} else {
		r.visibleRows = r.visibleRows[:0]
	}
	if len(r.hunkVisStart) != len(r.HunkStarts) {
		r.hunkVisStart = make([]int, len(r.HunkStarts))
		r.hunkVisEnd = make([]int, len(r.HunkStarts))
	}
	for i := range r.hunkVisStart {
		r.hunkVisStart[i] = -1
		r.hunkVisEnd[i] = -1
	}

	r.recomputeAdjustedNewNum()

	// Build a per-line hunk index so each line knows which hunk (if any)
	// it belongs to, then precompute per-hunk Del presence and Add
	// counts so flagged pure-Add hunks can be detected on the fly.
	lineHunk := make([]int, len(r.Lines))
	for i := range lineHunk {
		lineHunk[i] = -1
	}
	hunkHasDel := make([]bool, len(r.HunkStarts))
	hunkAddCount := make([]int, len(r.HunkStarts))
	for h := range r.HunkStarts {
		s := r.HunkStarts[h]
		e := s
		if h < len(r.HunkEnds) {
			e = r.HunkEnds[h]
		}
		if e < s {
			e = s
		}
		for i := s; i <= e && i < len(r.Lines); i++ {
			lineHunk[i] = h
			switch r.Lines[i].Kind {
			case Del:
				hunkHasDel[h] = true
			case Add:
				hunkAddCount[h]++
			}
		}
	}

	placeholderEmitted := make([]bool, len(r.HunkStarts))
	for i, ln := range r.Lines {
		flagged := r.isFlagged(i)
		h := lineHunk[i]
		if flagged && ln.Kind == Add {
			if h >= 0 && !hunkHasDel[h] && !placeholderEmitted[h] {
				rowIdx := len(r.visibleRows)
				r.visibleRows = append(r.visibleRows, visRow{
					lineIdx:       -1,
					hunkIdx:       h,
					collapsedAdds: hunkAddCount[h],
				})
				placeholderEmitted[h] = true
				r.hunkVisStart[h] = rowIdx
				r.hunkVisEnd[h] = rowIdx
			}
			continue
		}
		rowIdx := len(r.visibleRows)
		r.visibleRows = append(r.visibleRows, visRow{lineIdx: i})
		if h >= 0 && (ln.Kind == Add || ln.Kind == Del) {
			if r.hunkVisStart[h] < 0 {
				r.hunkVisStart[h] = rowIdx
			}
			r.hunkVisEnd[h] = rowIdx
		}
	}
}

// recomputeAdjustedNewNum walks Lines once and populates adjustedNewNum
// with each line's position in the post-revert new file. A flagged hunk
// contributes zero net shift to the new-file cursor: its Adds don't
// advance nextNew (they vanish post-revert) and its Dels do advance
// nextNew (they're restored). An unflagged hunk's contribution is the
// usual adds − dels. Counters are seeded per `@@`-hunk from the parsed
// header so multi-hunk diffs work.
func (r *Result) recomputeAdjustedNewNum() {
	if cap(r.adjustedNewNum) < len(r.Lines) {
		r.adjustedNewNum = make([]int, len(r.Lines))
	} else {
		r.adjustedNewNum = r.adjustedNewNum[:len(r.Lines)]
		for i := range r.adjustedNewNum {
			r.adjustedNewNum[i] = 0
		}
	}
	var nextOld, nextNew int
	hunkPtr := 0
	for i, ln := range r.Lines {
		for hunkPtr < len(r.rawHunkStarts) && r.rawHunkStarts[hunkPtr] == i {
			nextOld = r.rawHunkOldN[hunkPtr]
			nextNew = r.rawHunkNewN[hunkPtr]
			hunkPtr++
		}
		flagged := r.isFlagged(i)
		switch ln.Kind {
		case Context:
			r.adjustedNewNum[i] = nextNew
			nextOld++
			nextNew++
		case Add:
			if flagged {
				r.adjustedNewNum[i] = 0
			} else {
				r.adjustedNewNum[i] = nextNew
				nextNew++
			}
		case Del:
			if flagged {
				r.adjustedNewNum[i] = nextNew
				nextOld++
				nextNew++
			} else {
				r.adjustedNewNum[i] = 0
				nextOld++
			}
		}
	}
}

func (r Result) isFlagged(idx int) bool {
	if idx < 0 || idx >= len(r.flagged) {
		return false
	}
	return r.flagged[idx]
}

// Parse parses a raw unified diff into a Result. raw is the full-file
// diff (typically `git show -U99999`) used to build the Lines slice;
// hunksDiff is the same change at git's default context width, used
// only to identify real hunk boundaries inside Lines. When hunksDiff is
// empty, or when the diff is a combined merge diff (`--cc`, headers
// prefixed with `@@@`), hunk boundaries fall back to the `@@` markers
// in raw. The filename is used only as a hint for chroma's language
// detection; an empty string disables syntax highlighting.
func Parse(raw, hunksDiff, filename string) Result {
	var lines []Line
	var oldN, newN int
	inHunk := false
	hasCombined := false
	rawHunkStarts := []int{}
	var rawHunkOldN, rawHunkNewN []int
	for _, ln := range strings.Split(raw, "\n") {
		if strings.HasPrefix(ln, "@@@") {
			hasCombined = true
		}
		if strings.HasPrefix(ln, "@@") {
			oldN, newN = parseHunkHeader(ln)
			rawHunkStarts = append(rawHunkStarts, len(lines))
			rawHunkOldN = append(rawHunkOldN, oldN)
			rawHunkNewN = append(rawHunkNewN, newN)
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
	var hunkStarts, hunkEnds []int
	if hasCombined || hunksDiff == "" {
		// Combined-diff path: raw is already a small-context --cc diff,
		// so each `@@@` header marks a real hunk. Take the header's
		// position in Lines as both start and end; combined-diff hunk
		// extents aren't precisely modelled here, but framing on the
		// hunk header is still a useful anchor.
		hunkStarts = rawHunkStarts
		hunkEnds = append([]int(nil), rawHunkStarts...)
	} else {
		hunkStarts, hunkEnds = extractHunkBounds(lines, hunksDiff)
	}
	r := Result{
		Lines:         lines,
		HunkStarts:    hunkStarts,
		HunkEnds:      hunkEnds,
		rawHunkStarts: rawHunkStarts,
		rawHunkOldN:   rawHunkOldN,
		rawHunkNewN:   rawHunkNewN,
	}
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
	r.recomputeVisibleRows()
	return r
}

// extractHunkBounds walks a default-context unified diff (hunksDiff) to
// find each hunk's first and last changed (+/-) lines, then maps those
// to indices in lines (the full-file Lines slice built from raw). The
// result is two parallel slices: starts[i] and ends[i] bound the i-th
// real hunk inside lines. Hunks with no changed lines (rare but
// possible — e.g., context-only) are skipped.
func extractHunkBounds(lines []Line, hunksDiff string) (starts, ends []int) {
	type anchor struct {
		isAdd  bool
		oldNum int
		newNum int
	}
	var firsts, lasts []anchor
	var oldN, newN int
	inHunk := false
	haveFirst := false
	var first, last anchor
	flush := func() {
		if haveFirst {
			firsts = append(firsts, first)
			lasts = append(lasts, last)
			haveFirst = false
		}
	}
	for _, ln := range strings.Split(hunksDiff, "\n") {
		if strings.HasPrefix(ln, "@@") {
			flush()
			oldN, newN = parseHunkHeader(ln)
			inHunk = true
			continue
		}
		if !inHunk || ln == "" {
			continue
		}
		if ln[0] == '\\' {
			continue
		}
		switch ln[0] {
		case '+':
			a := anchor{isAdd: true, newNum: newN}
			if !haveFirst {
				first = a
				haveFirst = true
			}
			last = a
			newN++
		case '-':
			a := anchor{isAdd: false, oldNum: oldN}
			if !haveFirst {
				first = a
				haveFirst = true
			}
			last = a
			oldN++
		case ' ':
			oldN++
			newN++
		}
	}
	flush()

	find := func(from int, a anchor) int {
		for i := from; i < len(lines); i++ {
			if a.isAdd {
				if lines[i].Kind == Add && lines[i].NewNum == a.newNum {
					return i
				}
			} else {
				if lines[i].Kind == Del && lines[i].OldNum == a.oldNum {
					return i
				}
			}
		}
		return -1
	}
	cursor := 0
	for i := range firsts {
		s := find(cursor, firsts[i])
		if s < 0 {
			continue
		}
		e := find(s, lasts[i])
		if e < 0 {
			e = s
		}
		starts = append(starts, s)
		ends = append(ends, e)
		cursor = e
	}
	return starts, ends
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
//
// flagGutterBg / flagBodyBg / flagFg are the grey "tombstone" tones used
// to render hunks flagged for revert (see prd-hunk-revert.md). The
// grey gutter sits at roughly the saturation level of add/del gutters;
// the body bg matches the darkness of add/del body bgs; the body fg is
// applied to chroma-highlighted lines too so syntax colour does not
// re-introduce a non-grey signal inside a tombstone block.
const (
	addGutterBg  = "#8f9d6a"
	delGutterBg  = "#cf6a4c"
	addBodyBg    = "#393e2a"
	delBodyBg    = "#522a1e"
	flagGutterBg = "#6c6c6c"
	flagBodyBg   = "#2e2e2e"
	flagFg       = "#9e9e9e"
	invertedFg   = "#1e1e1e"
)

var (
	gutterStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	addGutterBandStyle = lipgloss.NewStyle().
				Background(lipgloss.Color(addGutterBg)).
				Foreground(lipgloss.Color(invertedFg))
	delGutterBandStyle = lipgloss.NewStyle().
				Background(lipgloss.Color(delGutterBg)).
				Foreground(lipgloss.Color(invertedFg))
	flagGutterBandStyle = lipgloss.NewStyle().
				Background(lipgloss.Color(flagGutterBg)).
				Foreground(lipgloss.Color(invertedFg))
	addBodyBgStyle  = lipgloss.NewStyle().Background(lipgloss.Color(addBodyBg))
	delBodyBgStyle  = lipgloss.NewStyle().Background(lipgloss.Color(delBodyBg))
	flagBodyStyle   = lipgloss.NewStyle().Background(lipgloss.Color(flagBodyBg)).Foreground(lipgloss.Color(flagFg))
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

// HunkRange returns the first and last line indices of the hunk at
// position activeHunk — the first and last changed (+/-) lines of that
// hunk inside Lines. Returns ok=false when activeHunk is out of range.
func (r Result) HunkRange(activeHunk int) (first, last int, ok bool) {
	if activeHunk < 0 || activeHunk >= len(r.HunkStarts) {
		return 0, 0, false
	}
	first = r.HunkStarts[activeHunk]
	if activeHunk < len(r.HunkEnds) {
		last = r.HunkEnds[activeHunk]
	} else {
		last = first
	}
	if last < first {
		last = first
	}
	return first, last, true
}

// FormatLineActive renders Lines[idx] like FormatLine and additionally
// wraps the result with an SGR overline when idx is the first line of
// activeHunk and/or an SGR underline when it is the last. The boundary
// color (SGR 58) reflects whether the line is flagged for revert and,
// if not, its Kind: flagged→flagGutterBg, add→addGutterBg, del→
// delGutterBg, context→default (no SGR 58 emitted). activeHunk < 0
// disables the decoration.
func (r Result) FormatLineActive(idx, width, hScroll, activeHunk int) string {
	s := r.FormatLine(idx, width, hScroll)
	first, last, ok := r.HunkRange(activeHunk)
	if !ok {
		return s
	}
	isFirst := idx == first
	isLast := idx == last
	if !isFirst && !isLast {
		return s
	}
	if idx < 0 || idx >= len(r.Lines) {
		return s
	}
	return wrapActiveSGR(s, activeBorderSGR(isFirst, isLast, r.Lines[idx].Kind, r.isFlagged(idx)))
}

// activeBorderSGR builds the SGR prefix that frames an active-hunk
// anchor row: overline on the first row, underline on the last, and
// SGR 58 (underline colour) chosen by flagged / Kind.
func activeBorderSGR(isFirst, isLast bool, kind Kind, flagged bool) string {
	var b strings.Builder
	if isFirst {
		b.WriteString("\x1b[53m")
	}
	if isLast {
		b.WriteString("\x1b[4m")
	}
	switch {
	case flagged:
		b.WriteString("\x1b[58;2;108;108;108m")
	case kind == Add:
		b.WriteString("\x1b[58;2;143;157;106m")
	case kind == Del:
		b.WriteString("\x1b[58;2;207;106;76m")
	}
	return b.String()
}

// wrapActiveSGR wraps s with the given active-frame SGR prefix and an
// SGR reset suffix, and re-emits the prefix after every embedded reset
// so the overline / underline survive lipgloss's per-segment "\x1b[0m"
// that would otherwise clear them mid-line. Returns s unchanged when
// the prefix is empty.
func wrapActiveSGR(s, sgr string) string {
	if sgr == "" {
		return s
	}
	s = strings.ReplaceAll(s, "\x1b[0m", "\x1b[0m"+sgr)
	return sgr + s + "\x1b[0m"
}

// RowCount returns the number of rows in the visible-row view of Lines.
// Add lines inside flagged hunks are absent; every other line is
// present 1:1. When no hunks are flagged, RowCount() equals len(Lines).
func (r Result) RowCount() int {
	return len(r.visibleRows)
}

// RowLineIndex resolves a visible-row index back to its underlying
// Lines index. Returns ok=false when rowIdx is out of range or refers
// to a synthetic placeholder row (which has no underlying Lines entry).
func (r Result) RowLineIndex(rowIdx int) (int, bool) {
	if rowIdx < 0 || rowIdx >= len(r.visibleRows) {
		return 0, false
	}
	vr := r.visibleRows[rowIdx]
	if vr.lineIdx < 0 {
		return 0, false
	}
	return vr.lineIdx, true
}

// HunkVisibleRange returns the first and last visible-row indices of
// the hunk at position hunkIdx — anchored on the first and last
// visible changed (+/-) line of that hunk. For unflagged hunks this
// matches HunkRange in row space. For a flagged mixed hunk it spans
// the first and last visible Del line. Returns ok=false when the
// hunk has no visible changed rows (a flagged pure-Add hunk under
// the issue-15 row model) or when hunkIdx is out of range.
func (r Result) HunkVisibleRange(hunkIdx int) (firstRow, lastRow int, ok bool) {
	if hunkIdx < 0 || hunkIdx >= len(r.hunkVisStart) {
		return 0, 0, false
	}
	if r.hunkVisStart[hunkIdx] < 0 {
		return 0, 0, false
	}
	return r.hunkVisStart[hunkIdx], r.hunkVisEnd[hunkIdx], true
}

// FormatRow renders the visible-row at rowIdx — the row-indexed
// equivalent of FormatLineActive. The active-hunk frame anchors on
// HunkVisibleRange(activeHunk), so the overline / underline land on
// the first and last *visible* rows of the active hunk. Placeholder
// rows for collapsed Add blocks render as a full-width grey bar with
// centered "── N lines reverted ──" text.
func (r Result) FormatRow(rowIdx, width, hScroll, activeHunk int) string {
	if rowIdx < 0 || rowIdx >= len(r.visibleRows) {
		return ""
	}
	vr := r.visibleRows[rowIdx]
	if vr.lineIdx < 0 {
		s := formatPlaceholder(vr.collapsedAdds, width)
		if activeHunk != vr.hunkIdx {
			return s
		}
		firstRow, lastRow, ok := r.HunkVisibleRange(activeHunk)
		if !ok {
			return s
		}
		isFirst := rowIdx == firstRow
		isLast := rowIdx == lastRow
		if !isFirst && !isLast {
			return s
		}
		return wrapActiveSGR(s, activeBorderSGR(isFirst, isLast, Context, true))
	}
	li := vr.lineIdx
	s := r.FormatLine(li, width, hScroll)
	firstRow, lastRow, ok := r.HunkVisibleRange(activeHunk)
	if !ok {
		return s
	}
	isFirst := rowIdx == firstRow
	isLast := rowIdx == lastRow
	if !isFirst && !isLast {
		return s
	}
	return wrapActiveSGR(s, activeBorderSGR(isFirst, isLast, r.Lines[li].Kind, r.isFlagged(li)))
}

// formatPlaceholder renders the synthetic placeholder row for a
// flagged pure-Add hunk: a full-width grey bar carrying the centered
// "── N lines reverted ──" message. The bg/fg match the flagged
// tombstone palette so the row reads as part of the flagged region.
func formatPlaceholder(collapsed, width int) string {
	if width <= 0 {
		return ""
	}
	msg := "── " + strconv.Itoa(collapsed) + " lines reverted ──"
	msgRunes := []rune(msg)
	var body string
	if len(msgRunes) >= width {
		body = string(msgRunes[:width])
	} else {
		leftPad := (width - len(msgRunes)) / 2
		rightPad := width - len(msgRunes) - leftPad
		body = strings.Repeat(" ", leftPad) + msg + strings.Repeat(" ", rightPad)
	}
	return flagBodyStyle.Render(body)
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
	flagged := r.isFlagged(idx)
	adjNew := 0
	if idx < len(r.adjustedNewNum) {
		adjNew = r.adjustedNewNum[idx]
	}
	gutter := r.formatGutter(l, flagged, adjNew)
	bodyW := width - r.GutterRenderWidth()
	if bodyW < 0 {
		bodyW = 0
	}
	if l.segs != nil {
		return gutter + formatHighlightedBody(l, bodyW, hScroll, flagged)
	}
	return gutter + formatPlainBody(l, bodyW, hScroll, flagged)
}

func formatPlainBody(l Line, bodyW, hScroll int, flagged bool) string {
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
	if flagged {
		return flagBodyStyle.Render(body)
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
func formatHighlightedBody(l Line, bodyW, hScroll int, flagged bool) string {
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
	if flagged {
		bodyBg = flagBodyBg
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
		if flagged {
			// Override per-token chroma colour with the tombstone grey
			// foreground so the block reads as one neutral region.
			s = lipgloss.NewStyle().Foreground(lipgloss.Color(flagFg)).Background(lipgloss.Color(flagBodyBg))
		} else if bodyBg != "" {
			s = s.Background(lipgloss.Color(bodyBg))
		}
		b.WriteString(s.Render(string(runes)))
		cells += len(runes)
	}
	if cells < bodyW {
		pad := strings.Repeat(" ", bodyW-cells)
		switch {
		case flagged:
			b.WriteString(flagBodyStyle.Render(pad))
		case l.Kind == Add:
			b.WriteString(addBodyBgStyle.Render(pad))
		case l.Kind == Del:
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
// background. The right-side NewNum is taken from the adjusted
// (post-revert) value and is shown only when positive and != OldNum,
// so the column adds no information when it would duplicate OldNum.
func (r Result) formatGutter(l Line, flagged bool, adjNewNum int) string {
	oldS := ""
	if l.OldNum > 0 {
		oldS = strconv.Itoa(l.OldNum)
	}
	newS := ""
	if adjNewNum > 0 && adjNewNum != l.OldNum {
		newS = strconv.Itoa(adjNewNum)
	}
	nums := padLeft(oldS, r.OldW) + " " + padLeft(newS, r.NewW) + " "
	if flagged {
		return flagGutterBandStyle.Render(nums)
	}
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

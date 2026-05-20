// Package filefilter parses and evaluates file-path filter expressions
// used by the file list. An Expr produced by Parse can be matched
// against a file's new and (optional) old path to decide whether the
// file should be visible.
//
// Slice 3 adds regex terms (`/.../`) alongside the existing glob,
// quoted-glob, multi-term, and negation grammar from earlier slices. A
// regex term is case-sensitive by default, evaluated as an unanchored
// substring match (Go regexp.MatchString semantics, which match
// anywhere in the input). A `/foo` token with no closing `/` falls
// back to being parsed as a glob.
//
// Slice 4 adds ParsePartial / ExprFromTokens for callers that want to
// keep filtering live as the user types: invalid-regex tokens are
// reported via Token.Valid=false (rather than aborting the parse) so
// the model layer can substitute a prior valid version of the token
// while the user finishes editing.
package filefilter

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Expr is a parsed filter expression. The zero value is empty
// (matches every file).
type Expr struct {
	raw   string
	terms []term
}

type termKind int

const (
	kindGlob termKind = iota
	kindRegex
)

type term struct {
	negate  bool
	kind    termKind
	pattern string
	re      *regexp.Regexp
}

type rawToken struct {
	negate  bool
	kind    termKind
	pattern string
	start   int // byte offset in source where this token begins (incl. `!`)
	end     int // exclusive byte offset where this token ends
}

// Parse parses input into an Expr. Leading and trailing whitespace are
// trimmed. An empty input (or whitespace-only) yields an empty Expr
// that matches every path.
//
// Grammar:
//   - Top-level commas split terms. Commas inside `"..."` or inside a
//     valid `/.../` regex region are literal.
//   - Whitespace around unquoted terms is trimmed.
//   - A leading `!` on a term negates it.
//   - A `"..."` quoted region protects commas inside; glob
//     metacharacters still apply.
//   - A term that begins with `/` and contains a closing `/` (followed
//     by whitespace then a comma or end-of-input) is a regex term.
//     Otherwise a leading `/` is treated as the start of a glob.
//   - Empty terms (trailing comma, double comma, pure whitespace) are
//     silently dropped.
//
// Returns an error for catastrophic input: an unclosed quote, trailing
// non-whitespace text after a regex's closing `/`, or an invalid regex.
func Parse(input string) (Expr, error) {
	t := strings.TrimSpace(input)
	if t == "" {
		return Expr{}, nil
	}
	tokens, err := tokenize(input)
	if err != nil {
		return Expr{}, err
	}
	var terms []term
	for _, tok := range tokens {
		if tok.pattern == "" && !tok.negate {
			continue
		}
		switch tok.kind {
		case kindRegex:
			re, err := regexp.Compile(tok.pattern)
			if err != nil {
				return Expr{}, fmt.Errorf("filefilter: invalid regex %q: %w", tok.pattern, err)
			}
			terms = append(terms, term{negate: tok.negate, kind: kindRegex, pattern: tok.pattern, re: re})
		default:
			terms = append(terms, term{negate: tok.negate, kind: kindGlob, pattern: tok.pattern})
		}
	}
	if len(terms) == 0 {
		return Expr{}, nil
	}
	return Expr{raw: t, terms: terms}, nil
}

// TermKind identifies whether a parsed term is a glob or a regex term.
// It is the exported counterpart of the package-private termKind.
type TermKind int

const (
	// KindGlob is a glob-style term (default; also covers `"..."` quoted
	// literal-glob terms).
	KindGlob TermKind = 0
	// KindRegex is a `/.../` regex term.
	KindRegex TermKind = 1
)

// Token describes a single top-level term as parsed by ParsePartial.
// Glob tokens always have Valid=true (globs do not fail to compile).
// Regex tokens have Valid=true when the regex source compiled
// successfully and Valid=false otherwise. Callers should treat Token
// as a value object — pass instances unchanged into ExprFromTokens to
// reuse a previously-compiled regex.
type Token struct {
	Negate  bool
	Kind    TermKind
	Pattern string
	Valid   bool
	// Start and End are byte offsets in the source passed to
	// ParsePartial that bound this token's raw text (including any
	// leading `!`). They allow callers to style spans of the original
	// input without re-tokenizing.
	Start int
	End   int

	// re is the compiled regex for valid KindRegex tokens; nil for
	// glob tokens and invalid regex tokens. Unexported on purpose so
	// callers cannot fabricate Tokens that bypass ParsePartial's
	// validation.
	re *regexp.Regexp
}

// Matches reports whether this token's underlying pattern matches
// path (or oldPath when non-empty), IGNORING the Negate flag. It is
// the predicate behind orange/green token styling — "does this token's
// pattern select any file" — independent of how the term composes
// inside a full Expr. Invalid regex tokens never match.
func (t Token) Matches(path, oldPath string) bool {
	if t.Kind == KindRegex {
		if !t.Valid || t.re == nil {
			return false
		}
		if t.re.MatchString(path) {
			return true
		}
		return oldPath != "" && t.re.MatchString(oldPath)
	}
	if matchGlob(t.Pattern, path) {
		return true
	}
	return oldPath != "" && matchGlob(t.Pattern, oldPath)
}

// ParsePartial is the tolerant variant of Parse: a regex token whose
// source fails to compile is reported in the returned []Token with
// Valid=false and is omitted from Expr, instead of aborting the parse.
// Unclosed quotes still return an error.
//
// The returned Expr contains only valid terms and can be used as-is
// with Match. The Tokens slice contains every non-empty top-level term
// from the input in source order, so callers can drive live debounce
// or display without re-tokenizing.
func ParsePartial(input string) (Expr, []Token, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return Expr{}, nil, nil
	}
	rawTokens, err := tokenize(input)
	if err != nil {
		return Expr{}, nil, err
	}
	var (
		tokens     []Token
		validTerms []term
	)
	for _, rt := range rawTokens {
		if rt.pattern == "" && !rt.negate {
			continue
		}
		switch rt.kind {
		case kindRegex:
			re, cerr := regexp.Compile(rt.pattern)
			tok := Token{
				Negate:  rt.negate,
				Kind:    KindRegex,
				Pattern: rt.pattern,
				Valid:   cerr == nil,
				Start:   rt.start,
				End:     rt.end,
				re:      re,
			}
			tokens = append(tokens, tok)
			if cerr == nil {
				validTerms = append(validTerms, term{
					negate:  rt.negate,
					kind:    kindRegex,
					pattern: rt.pattern,
					re:      re,
				})
			}
		default:
			tokens = append(tokens, Token{
				Negate:  rt.negate,
				Kind:    KindGlob,
				Pattern: rt.pattern,
				Valid:   true,
				Start:   rt.start,
				End:     rt.end,
			})
			validTerms = append(validTerms, term{
				negate:  rt.negate,
				kind:    kindGlob,
				pattern: rt.pattern,
			})
		}
	}
	if len(validTerms) == 0 {
		return Expr{raw: raw}, tokens, nil
	}
	return Expr{raw: raw, terms: validTerms}, tokens, nil
}

// ExprFromTokens builds an Expr from a slice of Tokens, using raw as
// the canonical expression string for String(). Tokens with Valid=false
// are silently omitted (a Token produced by ParsePartial carries its
// already-compiled regex when Valid=true, so this can reuse a prior
// successful compile without re-parsing).
//
// raw is stored verbatim (modulo TrimSpace) so callers that want the
// header to reflect the user's actual input — including the
// intermediate invalid-regex state — can pass the input as raw.
func ExprFromTokens(raw string, tokens []Token) Expr {
	r := strings.TrimSpace(raw)
	var terms []term
	for _, tk := range tokens {
		if !tk.Valid {
			continue
		}
		switch tk.Kind {
		case KindRegex:
			if tk.re == nil {
				continue
			}
			terms = append(terms, term{
				negate:  tk.Negate,
				kind:    kindRegex,
				pattern: tk.Pattern,
				re:      tk.re,
			})
		default:
			terms = append(terms, term{
				negate:  tk.Negate,
				kind:    kindGlob,
				pattern: tk.Pattern,
			})
		}
	}
	return Expr{raw: r, terms: terms}
}

// IsEmpty reports whether the expression contains no terms.
func (e Expr) IsEmpty() bool { return len(e.terms) == 0 }

// String returns the canonical expression text.
func (e Expr) String() string { return e.raw }

// Match reports whether a file with new path `path` and old path
// `oldPath` (empty when not a rename) matches the expression. An empty
// expression matches every file.
//
// A file is included iff:
//
//	(no positive terms OR at least one positive term matches)
//	AND no negative term matches
//
// Matching is evaluated against both `path` and `oldPath` (when
// non-empty): a positive term matches if either side matches; a
// negative term excludes if either side matches.
//
// Per-term semantics:
//
//   - Glob (default): case-insensitive. A pattern with no glob
//     metacharacters (`*`, `?`, `[`) is a plain substring match:
//     against the basename when the pattern has no `/`, against the
//     full path otherwise. A pattern with glob metacharacters follows
//     gitignore-style anchoring (no `/` ⇒ basename match; `/` ⇒
//     full-path with `*` not crossing `/`); `**` supported.
//   - Regex (`/.../`): case-sensitive by default (use `(?i)` to opt
//     in to case-insensitivity); unanchored substring match against
//     the candidate path (Go's RE2 MatchString semantics).
func (e Expr) Match(path, oldPath string) bool {
	if e.IsEmpty() {
		return true
	}
	hasPositive := false
	positiveMatched := false
	for _, t := range e.terms {
		m := termMatch(t, path)
		if !m && oldPath != "" {
			m = termMatch(t, oldPath)
		}
		if t.negate {
			if m {
				return false
			}
			continue
		}
		hasPositive = true
		if m {
			positiveMatched = true
		}
	}
	if !hasPositive {
		return true
	}
	return positiveMatched
}

func termMatch(t term, p string) bool {
	if t.kind == kindRegex {
		return t.re.MatchString(p)
	}
	return matchGlob(t.pattern, p)
}

func matchGlob(pattern, p string) bool {
	pl := strings.ToLower(pattern)
	pp := strings.ToLower(p)
	hasWild := strings.ContainsAny(pl, "*?[")
	if strings.Contains(pl, "/") {
		if !hasWild {
			return strings.Contains(pp, pl)
		}
		ok, _ := doublestar.PathMatch(pl, pp)
		return ok
	}
	base := pp
	if i := strings.LastIndex(pp, "/"); i >= 0 {
		base = pp[i+1:]
	}
	if !hasWild {
		return strings.Contains(base, pl)
	}
	ok, _ := doublestar.PathMatch(pl, base)
	return ok
}

// tokenize splits input on top-level commas into structured terms.
//
// Per-term: leading whitespace is skipped, an optional `!` is consumed
// as a negate flag, and the remainder is classified:
//
//   - Starts with `/` and has a closing `/` (followed by whitespace
//     then comma-or-EOF): regex term.
//   - Otherwise (including a `/foo` with no valid closing `/`):
//     glob term, which can contain `"..."` regions protecting commas
//     and other characters from the tokenizer.
//
// Inside `/.../`, characters (including `,` and `"`) are literal until
// the closing `/`. Inside `"..."`, characters are literal until the
// closing `"`.
func tokenize(input string) ([]rawToken, error) {
	var tokens []rawToken
	i := 0
	for i < len(input) {
		for i < len(input) && (input[i] == ' ' || input[i] == '\t') {
			i++
		}
		if i >= len(input) {
			break
		}
		if input[i] == ',' {
			i++
			continue
		}
		tokenStart := i
		negate := false
		if input[i] == '!' {
			negate = true
			i++
		}
		// Regex term attempt.
		if i < len(input) && input[i] == '/' {
			closing := findRegexClose(input, i)
			if closing > i {
				pat := input[i+1 : closing]
				next := closing + 1
				for next < len(input) && (input[next] == ' ' || input[next] == '\t') {
					next++
				}
				tokens = append(tokens, rawToken{
					negate: negate, kind: kindRegex, pattern: pat,
					start: tokenStart, end: closing + 1,
				})
				i = next
				if i < len(input) && input[i] == ',' {
					i++
				}
				continue
			}
			// Fall through: leading `/` becomes part of a glob term.
		}
		// Glob term.
		pat, advance, err := readGlobTerm(input, i)
		if err != nil {
			return nil, err
		}
		i = advance
		tokenEnd := i
		for tokenEnd > tokenStart && (input[tokenEnd-1] == ' ' || input[tokenEnd-1] == '\t') {
			tokenEnd--
		}
		if pat != "" || negate {
			tokens = append(tokens, rawToken{
				negate: negate, kind: kindGlob, pattern: pat,
				start: tokenStart, end: tokenEnd,
			})
		}
		if i < len(input) && input[i] == ',' {
			i++
		}
	}
	return tokens, nil
}

// findRegexClose locates the closing `/` for a regex term that begins
// at input[start] (input[start] must be '/'). The closing `/` is the
// first `/` at index > start such that the next non-whitespace
// character is either end-of-input or a top-level comma. Returns -1
// when no such closing `/` exists, so the caller can fall back to
// glob parsing.
func findRegexClose(input string, start int) int {
	for j := start + 1; j < len(input); j++ {
		if input[j] != '/' {
			continue
		}
		k := j + 1
		for k < len(input) && (input[k] == ' ' || input[k] == '\t') {
			k++
		}
		if k >= len(input) || input[k] == ',' {
			return j
		}
	}
	return -1
}

// readGlobTerm reads a glob-kind term starting at input[start] until
// the next top-level comma or end-of-input. `"..."` regions are
// stripped of their surrounding quotes but their contents are
// preserved literally. Unquoted leading/trailing whitespace within the
// term is trimmed; whitespace inside quotes is preserved.
func readGlobTerm(input string, start int) (string, int, error) {
	var b strings.Builder
	var pendingWS strings.Builder
	hasContent := false
	inQ := false
	i := start
	for i < len(input) {
		c := input[i]
		if inQ {
			if c == '"' {
				inQ = false
				i++
				continue
			}
			b.WriteByte(c)
			hasContent = true
			i++
			continue
		}
		if c == ',' {
			break
		}
		switch c {
		case '"':
			if hasContent {
				b.WriteString(pendingWS.String())
			}
			pendingWS.Reset()
			inQ = true
			hasContent = true
			i++
		case ' ', '\t':
			if hasContent {
				pendingWS.WriteByte(c)
			}
			i++
		default:
			if pendingWS.Len() > 0 {
				b.WriteString(pendingWS.String())
				pendingWS.Reset()
			}
			b.WriteByte(c)
			hasContent = true
			i++
		}
	}
	if inQ {
		return "", 0, errors.New("filefilter: unclosed quote")
	}
	return b.String(), i, nil
}

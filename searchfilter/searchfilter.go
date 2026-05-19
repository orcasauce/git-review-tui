// Package searchfilter is a pure fuzzy matcher. Given a query and a
// list of candidate strings it returns the matching indices ranked by
// score: higher score first, ties broken by earliest input position so
// the order is stable across runs.
//
// The match algorithm walks the query as a case-insensitive subsequence
// of the candidate. Each character either consumes the next candidate
// byte (consecutive bonus) or skips one or more bytes (gap penalty).
// A bonus also fires for matching at the start of the candidate or
// immediately after a word-boundary byte (space, `/`, `-`, `_`, `.`).
//
// The package has no dependencies on the rest of the codebase so it can
// be reused for both the commit search and the file-path search.
package searchfilter

import (
	"sort"
	"strings"
	"unicode"
)

// Match describes a single ranked hit.
type Match struct {
	// Index is the position of the matched item in the original input slice.
	Index int
	// Score is the match score; higher is better.
	Score int
}

// Score computes the fuzzy-match score of query against target. ok is
// false when the query characters do not appear in order as a (case-
// insensitive) subsequence of the target. An empty query matches with
// score 0.
func Score(query, target string) (score int, ok bool) {
	if query == "" {
		return 0, true
	}
	q := strings.ToLower(query)
	t := strings.ToLower(target)
	pos := 0
	lastIdx := -2
	firstIdx := -1
	for i := 0; i < len(q); i++ {
		c := q[i]
		idx := strings.IndexByte(t[pos:], c)
		if idx < 0 {
			return 0, false
		}
		idx += pos
		if firstIdx < 0 {
			firstIdx = idx
		}
		if idx == lastIdx+1 {
			score += 10
		} else {
			score -= (idx - lastIdx - 1)
		}
		// Word-boundary bonus: at start of target or right after a
		// separator byte the match is more visually meaningful.
		if idx == 0 || isBoundary(rune(t[idx-1])) {
			score += 5
		}
		lastIdx = idx
		pos = idx + 1
	}
	// Earlier first-match position is preferred when other factors tie.
	score -= firstIdx
	return score, true
}

func isBoundary(r rune) bool {
	switch r {
	case ' ', '\t', '/', '-', '_', '.':
		return true
	}
	return unicode.IsSpace(r)
}

// Rank returns one Match per candidate in items that matches query,
// sorted by descending score (ties broken by ascending Index). An empty
// query returns nil — callers that want "everything matches" should
// special-case the empty input themselves.
func Rank(query string, items []string) []Match {
	if query == "" {
		return nil
	}
	out := make([]Match, 0, len(items))
	for i, it := range items {
		s, ok := Score(query, it)
		if !ok {
			continue
		}
		out = append(out, Match{Index: i, Score: s})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Index < out[j].Index
	})
	return out
}

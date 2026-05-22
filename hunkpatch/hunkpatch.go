// Package hunkpatch provides pure functions for working with the
// individual hunks of a unified diff: canonicalising hunk text into a
// stable form, hashing it for use as a content fingerprint, extracting
// a hunk by index from a multi-hunk diff, and recombining a set of
// hunks into a valid per-file patch suitable for `git apply -R --3way`.
//
// The package is TUI-agnostic and does no IO. It is the patch-shaping
// layer that sits between revertstate (which owns the marks) and gitcmd
// (which feeds the result to git).
package hunkpatch

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Canonical normalises a hunk's text so that two hunks expressing the
// same change get the same canonical form regardless of incidental
// differences in line numbers or trailing whitespace.
//
// Specifically: the leading `@@ -X,Y +A,B @@` header line is dropped
// (its line numbers shift whenever surrounding context shifts), every
// `+`/`-`/` `/`\` body line is kept in order, and the trailing newline
// is stripped. A hunk that lacks a header is treated as already
// header-less. The empty string is returned unchanged.
func Canonical(hunk string) string {
	if hunk == "" {
		return ""
	}
	lines := strings.Split(hunk, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if strings.HasPrefix(ln, "@@") {
			continue
		}
		if ln == "" {
			continue
		}
		switch ln[0] {
		case '+', '-', ' ', '\\':
			out = append(out, ln)
		}
	}
	return strings.Join(out, "\n")
}

// Hash returns a short content fingerprint for an already-canonical
// hunk. The result is 16 lowercase hex characters of a sha256 digest:
// long enough that collisions in a single review session are not a
// practical concern, short enough to keep mark snapshots compact.
// Hash is deterministic; identical input yields identical output.
func Hash(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:8])
}

// ExtractByIndex returns the index-th hunk (0-based) of a unified diff
// for a single file. The returned text starts at the `@@` header line
// and runs through the last body line of that hunk, with no trailing
// newline. Any preamble (`diff --git`, `index`, `---`, `+++`) and any
// trailing content past the requested hunk is discarded. If the diff
// has fewer than index+1 hunks, the empty string is returned.
//
// The diff is expected to be a regular two-way unified diff (the kind
// produced by `git diff` with default context); combined `@@@` headers
// from merge diffs are not recognised as hunk boundaries.
func ExtractByIndex(diff string, index int) string {
	if index < 0 || diff == "" {
		return ""
	}
	lines := strings.Split(diff, "\n")
	starts := []int{}
	for i, ln := range lines {
		if strings.HasPrefix(ln, "@@") && !strings.HasPrefix(ln, "@@@") {
			starts = append(starts, i)
		}
	}
	if index >= len(starts) {
		return ""
	}
	from := starts[index]
	to := len(lines)
	if index+1 < len(starts) {
		to = starts[index+1]
	}
	out := strings.Join(lines[from:to], "\n")
	return strings.TrimRight(out, "\n")
}

// CombineForFile assembles a list of hunk bodies into a single
// per-file patch with a minimal git-style header. Each hunk in hunks
// must start with its `@@` header line. The returned patch ends with
// a trailing newline, which `git apply` expects.
//
// The header uses the conventional `a/`+path / `b/`+path prefixes.
// New-file and deleted-file modes are not synthesised here; callers
// that need those must build their own header. CombineForFile returns
// the empty string when path is empty or hunks is empty.
func CombineForFile(path string, hunks []string) string {
	if path == "" || len(hunks) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("diff --git a/")
	b.WriteString(path)
	b.WriteString(" b/")
	b.WriteString(path)
	b.WriteByte('\n')
	b.WriteString("--- a/")
	b.WriteString(path)
	b.WriteByte('\n')
	b.WriteString("+++ b/")
	b.WriteString(path)
	b.WriteByte('\n')
	for _, h := range hunks {
		b.WriteString(strings.TrimRight(h, "\n"))
		b.WriteByte('\n')
	}
	return b.String()
}

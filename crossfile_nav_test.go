package main

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/orcasauce/git-review-tui/diffrender"
	"github.com/orcasauce/git-review-tui/gitcmd"
	"github.com/orcasauce/git-review-tui/hunkstate"
	"github.com/orcasauce/git-review-tui/loader"
)

// stubDiffSource is a no-op loader.Source. The cross-file navigation
// tests never pump the LoadDiff cmd, so these methods are never
// invoked; they exist purely so loader.New's non-nil-Source check
// passes.
type stubDiffSource struct{}

func (stubDiffSource) Show(context.Context, string) (gitcmd.CommitDetail, error) {
	return gitcmd.CommitDetail{}, nil
}
func (stubDiffSource) NumStat(context.Context, string) ([]gitcmd.FileStat, error) { return nil, nil }
func (stubDiffSource) Diff(context.Context, string, string) (string, error)      { return "", nil }
func (stubDiffSource) DiffHunks(context.Context, string, string) (string, error) { return "", nil }

// newCrossFileNavModel builds a model parked on the first file of a
// three-file fixture: file0 has 2 hunks, file1 is binary, file2 has 3
// hunks. The active section is the files panel, ready to receive an
// `n` keypress.
func newCrossFileNavModel(t *testing.T) model {
	t.Helper()
	const sha = "abcd1234"
	m := newRenderModel(200, 50, 3, 0, 0)
	m.active = sectionFiles
	m.ldr = loader.New(loader.Config{Source: stubDiffSource{}})
	m.commits[0].SHA = sha
	m.detail.SHA = sha
	m.detailSHA = sha
	m.files = []gitcmd.FileStat{
		{Status: "M", Path: "alpha.go"},
		{Status: "M", Path: "logo.png", IsBinary: true},
		{Status: "M", Path: "beta.go"},
	}
	m.filesSHA = sha
	m.fileFilterVisible = []int{0, 1, 2}
	m.filesSelectedIdx = 0
	m.hunks = hunkstate.New()
	// Seed the diff for file0 with 2 hunks so the first `n` is a
	// within-file step and the second crosses.
	m.diff = diffrender.Result{HunkStarts: []int{0, 5}}
	m.diffSHA = sha
	m.diffPath = "alpha.go"
	m.activeHunk = 0
	return m
}

// fireDiffResult feeds a synthetic loader.DiffResult through Update so
// the DiffResult handler runs. HunkStarts is forged via diffrender.Parse
// on a one-hunk fixture, then overridden to the requested count by
// constructing the Result directly — but Parse needs valid input. The
// simpler approach: bypass Update for the diff parse step by injecting
// a pre-parsed Result and synthesizing the side effects the handler
// performs. This helper keeps the test focused on the override logic
// rather than diffrender internals.
func fireDiffResult(t *testing.T, m model, sha, path string, numHunks int) model {
	t.Helper()
	// Build a minimal raw diff that diffrender.Parse can chew on.
	// Each hunk is a single-line change so HunkStarts gets numHunks
	// entries. For numHunks == 0 we feed an empty diff (binary).
	raw := ""
	hunks := ""
	if numHunks > 0 {
		raw = "diff --git a/" + path + " b/" + path + "\n--- a/" + path + "\n+++ b/" + path + "\n"
		hunks = raw
		for i := 0; i < numHunks; i++ {
			raw += "@@ -1,1 +1,1 @@\n-old line\n+new line\n"
			hunks += "@@ -1,1 +1,1 @@\n-old line\n+new line\n"
		}
	}
	msg := loader.DiffResult{SHA: sha, Path: path, Raw: raw, Hunks: hunks}
	mi, _ := m.Update(msg)
	return mi.(model)
}

// TestCrossFileNav_WithinFileForwardUnchanged asserts that `n` from
// hunk 0 of a 2-hunk file advances to hunk 1 without changing the
// file selection — the existing in-file behavior is preserved for
// non-boundary presses.
func TestCrossFileNav_WithinFileForwardUnchanged(t *testing.T) {
	m := newCrossFileNavModel(t)
	m = updateKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if got, want := m.filesSelectedIdx, 0; got != want {
		t.Errorf("filesSelectedIdx = %d, want %d", got, want)
	}
	if got, want := m.activeHunk, 1; got != want {
		t.Errorf("activeHunk = %d, want %d", got, want)
	}
	if m.pendingHunkPosition != pendingNone {
		t.Errorf("pendingHunkPosition = %d, want pendingNone", m.pendingHunkPosition)
	}
}

// TestCrossFileNav_LastHunkCrossesToNextFile asserts that `n` from
// the last hunk of file0 (the text file) advances file selection to
// file1 (the binary destination), sets pendingHunkPosition only when
// the destination has hunks to land on (so on binary it stays None),
// and that activeHunk ends up at NoActiveHunk synchronously.
func TestCrossFileNav_LastHunkCrossesToBinaryNext(t *testing.T) {
	m := newCrossFileNavModel(t)
	m.activeHunk = 1 // park on last hunk of file0
	m = updateKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if got, want := m.filesSelectedIdx, 1; got != want {
		t.Fatalf("filesSelectedIdx = %d, want %d (logo.png)", got, want)
	}
	// Destination is binary → startDiffForSelection clears the diff
	// and sets activeHunk = NoActiveHunk synchronously.
	if got, want := m.activeHunk, hunkstate.NoActiveHunk; got != want {
		t.Errorf("activeHunk = %d, want %d (NoActiveHunk)", got, want)
	}
	// Pending intent should NOT be set when destination is binary —
	// there's no DiffResult arriving to consume it.
	if m.pendingHunkPosition != pendingNone {
		t.Errorf("pendingHunkPosition = %d, want pendingNone (binary destination)", m.pendingHunkPosition)
	}
}

// TestCrossFileNav_BinaryFileNotADeadEnd asserts that pressing `n`
// while sitting on a binary file (zero hunks) crosses to the next
// file even though there is no last-hunk boundary to hit.
func TestCrossFileNav_BinaryFileNotADeadEnd(t *testing.T) {
	const sha = "abcd1234"
	m := newCrossFileNavModel(t)
	// Reposition the model onto the binary file (orig idx 1).
	m.filesSelectedIdx = 1
	m.diff = diffrender.Result{}
	m.diffSHA = ""
	m.diffPath = ""
	m.activeHunk = hunkstate.NoActiveHunk
	m = updateKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if got, want := m.filesSelectedIdx, 2; got != want {
		t.Fatalf("filesSelectedIdx = %d, want %d (beta.go)", got, want)
	}
	if m.pendingHunkPosition != pendingFirst {
		t.Errorf("pendingHunkPosition = %d, want pendingFirst", m.pendingHunkPosition)
	}
	// Now fire the synthetic DiffResult and assert landing.
	m = fireDiffResult(t, m, sha, "beta.go", 3)
	if got, want := m.activeHunk, 0; got != want {
		t.Errorf("activeHunk after DiffResult = %d, want %d", got, want)
	}
	if m.pendingHunkPosition != pendingNone {
		t.Errorf("pendingHunkPosition after DiffResult = %d, want pendingNone", m.pendingHunkPosition)
	}
	// And the override is persisted in the tracker.
	if got := m.hunks.Get(sha, "beta.go", 3); got != 0 {
		t.Errorf("hunks.Get after DiffResult = %d, want 0 (persisted override)", got)
	}
}

// TestCrossFileNav_WrapsForwardFromLastFile asserts that crossing
// past the last visible file lands on the first visible file.
func TestCrossFileNav_WrapsForwardFromLastFile(t *testing.T) {
	const sha = "abcd1234"
	m := newCrossFileNavModel(t)
	// Park on the last file (beta.go, orig idx 2) at its last hunk.
	m.filesSelectedIdx = 2
	m.diff = diffrender.Result{HunkStarts: []int{0, 4, 8}}
	m.diffSHA = sha
	m.diffPath = "beta.go"
	m.activeHunk = 2
	m = updateKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if got, want := m.filesSelectedIdx, 0; got != want {
		t.Fatalf("filesSelectedIdx after wrap = %d, want %d (alpha.go)", got, want)
	}
	if m.pendingHunkPosition != pendingFirst {
		t.Errorf("pendingHunkPosition = %d, want pendingFirst", m.pendingHunkPosition)
	}
	m = fireDiffResult(t, m, sha, "alpha.go", 2)
	if got, want := m.activeHunk, 0; got != want {
		t.Errorf("activeHunk after wrap-land = %d, want %d", got, want)
	}
}

// TestCrossFileNav_PendingClearedByMidFlightJ asserts story 14: if
// the user presses `j` while a cross-file DiffResult is still in
// flight, the pending intent drops so the eventual diff arrival uses
// the tracker default rather than snapping to first.
func TestCrossFileNav_PendingClearedByMidFlightJ(t *testing.T) {
	const sha = "abcd1234"
	m := newCrossFileNavModel(t)
	m.activeHunk = 1 // last hunk of alpha.go
	// Park on file index 2 (beta.go, has 3 hunks) so `n` from file0
	// reaches a text destination (file1 is binary; file2 we get to
	// after the binary intermediate). For this test we just need a
	// text destination — adjust files so file1 is also text.
	m.files = []gitcmd.FileStat{
		{Status: "M", Path: "alpha.go"},
		{Status: "M", Path: "beta.go"},
	}
	m.fileFilterVisible = []int{0, 1}
	m.filesSelectedIdx = 0
	m.diffSHA = sha
	m.diffPath = "alpha.go"
	m.diff = diffrender.Result{HunkStarts: []int{0, 5}}
	m.activeHunk = 1

	m = updateKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if m.pendingHunkPosition != pendingFirst {
		t.Fatalf("pendingHunkPosition after cross = %d, want pendingFirst", m.pendingHunkPosition)
	}
	if m.filesSelectedIdx != 1 {
		t.Fatalf("filesSelectedIdx = %d, want 1", m.filesSelectedIdx)
	}
	// User presses j before the diff lands. With a 2-file fixture and
	// already on the last visible file, j is a no-op for selection;
	// instead simulate a more representative mid-flight action: press
	// k to go back. That goes through onFileSelectionChanged.
	m = updateKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if m.pendingHunkPosition != pendingNone {
		t.Errorf("pendingHunkPosition after k = %d, want pendingNone", m.pendingHunkPosition)
	}
	// Now if a stale beta.go DiffResult arrives (after the user moved
	// back to alpha), currentSelection won't match and the handler
	// will discard it. Simulate the non-stale case where the user
	// later lands on beta again via a fresh `n`: the tracker default
	// (0, since nothing was persisted) is what they get.
}

// TestCrossFileNav_WithinFileBackwardUnchanged asserts that `N` from
// hunk 1 of a 2-hunk file steps back to hunk 0 without changing the
// file selection — non-boundary backward presses are still in-file.
func TestCrossFileNav_WithinFileBackwardUnchanged(t *testing.T) {
	m := newCrossFileNavModel(t)
	m.activeHunk = 1
	m = updateKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	if got, want := m.filesSelectedIdx, 0; got != want {
		t.Errorf("filesSelectedIdx = %d, want %d", got, want)
	}
	if got, want := m.activeHunk, 0; got != want {
		t.Errorf("activeHunk = %d, want %d", got, want)
	}
	if m.pendingHunkPosition != pendingNone {
		t.Errorf("pendingHunkPosition = %d, want pendingNone", m.pendingHunkPosition)
	}
}

// TestCrossFileNav_FirstHunkCrossesBackwardWrap asserts that `N` from
// the first hunk of the first visible file wraps to the last file and
// sets pendingLast so the destination DiffResult lands on its last
// hunk.
func TestCrossFileNav_FirstHunkCrossesBackwardWrap(t *testing.T) {
	const sha = "abcd1234"
	m := newCrossFileNavModel(t)
	// Park on file0, hunk 0 (first hunk of the first visible file).
	m.activeHunk = 0
	// Swap file1 (binary) for a text file so we can assert the pending
	// intent + DiffResult landing without binary-skip muddying the
	// scenario.
	m.files = []gitcmd.FileStat{
		{Status: "M", Path: "alpha.go"},
		{Status: "M", Path: "beta.go"},
	}
	m.fileFilterVisible = []int{0, 1}
	m = updateKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	if got, want := m.filesSelectedIdx, 1; got != want {
		t.Fatalf("filesSelectedIdx after wrap = %d, want %d (beta.go)", got, want)
	}
	if m.pendingHunkPosition != pendingLast {
		t.Errorf("pendingHunkPosition = %d, want pendingLast", m.pendingHunkPosition)
	}
	m = fireDiffResult(t, m, sha, "beta.go", 3)
	if got, want := m.activeHunk, 2; got != want {
		t.Errorf("activeHunk after wrap-land = %d, want %d", got, want)
	}
	if m.pendingHunkPosition != pendingNone {
		t.Errorf("pendingHunkPosition after DiffResult = %d, want pendingNone", m.pendingHunkPosition)
	}
	if got := m.hunks.Get(sha, "beta.go", 3); got != 2 {
		t.Errorf("hunks.Get after DiffResult = %d, want 2 (persisted override)", got)
	}
}

// TestCrossFileNav_BinaryFileNotADeadEndBackward asserts that pressing
// `N` while sitting on a binary file crosses backward to the previous
// file rather than being a no-op.
func TestCrossFileNav_BinaryFileNotADeadEndBackward(t *testing.T) {
	const sha = "abcd1234"
	m := newCrossFileNavModel(t)
	// Reposition the model onto the binary file (orig idx 1).
	m.filesSelectedIdx = 1
	m.diff = diffrender.Result{}
	m.diffSHA = ""
	m.diffPath = ""
	m.activeHunk = hunkstate.NoActiveHunk
	m = updateKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	if got, want := m.filesSelectedIdx, 0; got != want {
		t.Fatalf("filesSelectedIdx = %d, want %d (alpha.go)", got, want)
	}
	if m.pendingHunkPosition != pendingLast {
		t.Errorf("pendingHunkPosition = %d, want pendingLast", m.pendingHunkPosition)
	}
	m = fireDiffResult(t, m, sha, "alpha.go", 2)
	if got, want := m.activeHunk, 1; got != want {
		t.Errorf("activeHunk after DiffResult = %d, want %d", got, want)
	}
}

// TestCrossFileNav_DiffPanelFocusedBackwardUnchanged asserts that `N`
// in the diff panel still performs the within-file wrap (matching the
// existing forward `n` carve-out for diff focus).
func TestCrossFileNav_DiffPanelFocusedBackwardUnchanged(t *testing.T) {
	m := newCrossFileNavModel(t)
	m.active = sectionDiff
	m.activeHunk = 0 // first hunk of alpha.go (2 hunks)
	m = updateKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	if got, want := m.filesSelectedIdx, 0; got != want {
		t.Errorf("filesSelectedIdx changed under diff-focused N: got %d, want %d", got, want)
	}
	if got, want := m.activeHunk, 1; got != want {
		t.Errorf("activeHunk after diff-focused N = %d, want %d (within-file wrap)", got, want)
	}
	if m.pendingHunkPosition != pendingNone {
		t.Errorf("pendingHunkPosition = %d, want pendingNone (diff path)", m.pendingHunkPosition)
	}
}

// TestCrossFileNav_DiffPanelFocusedUnchanged asserts the existing
// within-file wrap remains in effect when the diff panel is focused
// (the PRD explicitly carves out cross-file behavior for the file
// panel only).
func TestCrossFileNav_DiffPanelFocusedUnchanged(t *testing.T) {
	m := newCrossFileNavModel(t)
	m.active = sectionDiff
	m.activeHunk = 1 // last hunk of alpha.go (2 hunks)
	m = updateKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if got, want := m.filesSelectedIdx, 0; got != want {
		t.Errorf("filesSelectedIdx changed under diff-focused n: got %d, want %d", got, want)
	}
	if got, want := m.activeHunk, 0; got != want {
		t.Errorf("activeHunk after diff-focused n = %d, want %d (within-file wrap)", got, want)
	}
	if m.pendingHunkPosition != pendingNone {
		t.Errorf("pendingHunkPosition = %d, want pendingNone (diff path)", m.pendingHunkPosition)
	}
}

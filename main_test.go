package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/orcasauce/git-review-tui/gitcmd"
	"github.com/orcasauce/git-review-tui/layout"
)

// fakeCommit builds a minimal Commit good enough for log-panel rendering.
func fakeCommit(i int) gitcmd.Commit {
	return gitcmd.Commit{
		SHA:           "0000000000000000000000000000000000000000",
		ShortSHA:      "abc1234",
		Author:        "Whitney Beck",
		Email:         "wb@example.com",
		AuthorDateISO: "2026-05-13T10:42:11-07:00",
		RelDate:       "2 days ago",
		Subject:       "fix: subject text",
	}
}

func newRenderModel(w, h, ncommits, selectedIdx, viewportTop int) model {
	commits := make([]gitcmd.Commit, ncommits)
	for i := range commits {
		commits[i] = fakeCommit(i)
	}
	files := []gitcmd.FileStat{
		{Status: "M", Path: "main.go"},
		{Status: "M", Path: "metadata/metadata.go"},
		{Status: "A", Path: "newfile.go"},
	}
	m := model{
		w: w, h: h,
		active:         sectionTop,
		commits:        commits,
		selectedIdx:    selectedIdx,
		viewportTop:    viewportTop,
		branch:         "main",
		pendingActions: map[string]ActionKind{},
		files:          files,
		filesSHA:       commits[selectedIdx].SHA,
		detail: gitcmd.CommitDetail{
			SHA:           commits[selectedIdx].SHA,
			ShortSHA:      commits[selectedIdx].ShortSHA,
			AuthorName:    commits[selectedIdx].Author,
			AuthorEmail:   commits[selectedIdx].Email,
			AuthorDateISO: commits[selectedIdx].AuthorDateISO,
			AuthorDateRel: commits[selectedIdx].RelDate,
			Body:          "fix: subject text",
		},
		detailSHA: commits[selectedIdx].SHA,
	}
	return m
}

// TestMessagePanelSHALineStaysVisibleWhileScrolled asserts that the
// short-sha line at the top of the message-panel metadata block is
// always rendered, even when msgScroll has been advanced past it. The
// metadata header (sha, author, tags, blank separator) is conceptually
// a sticky panel header — losing it leaves the reviewer with no
// indication of which commit they are reading.
func TestMessagePanelSHALineStaysVisibleWhileScrolled(t *testing.T) {
	const w, h = 120, 40
	m := newRenderModel(w, h, 12, 5, 0)
	// Give the body enough lines that scrolling is meaningful.
	m.detail.Body = strings.Repeat("body line\n", 30)
	// Simulate the scroll position that strips the SHA off the top
	// (one wheel-tick / one stray scroll event, etc).
	m.msgScroll = 1

	lo := layout.Compute(w, h, len(m.files))
	panel := renderMessagePanel(m, lo.TopRight.W, lo.TopRight.H, true, false)
	lines := strings.Split(panel, "\n")

	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines in message panel, got %d", len(lines))
	}
	// Row 0 is the title row. Row 1 is the first body row of the panel.
	// It must contain the short SHA — otherwise the panel header has
	// silently scrolled off-screen and the reviewer cannot tell which
	// commit's message they are looking at.
	firstBodyRow := lines[1]
	if !strings.Contains(firstBodyRow, m.detail.ShortSHA) {
		t.Errorf("first body row of message panel does not contain short SHA %q (panel header scrolled off)\nrow content: %q", m.detail.ShortSHA, firstBodyRow)
	}
}

// TestViewHeightMatchesTerminalHeight verifies View() returns exactly h
// lines for every selection position. If any panel emits an extra (or
// missing) row, View()'s line count drifts from m.h and the terminal
// will scroll, eating the title row off the top of the screen.
func TestViewHeightMatchesTerminalHeight(t *testing.T) {
	sizes := [][2]int{{120, 40}, {100, 30}, {80, 24}, {200, 50}, {90, 26}}
	loadingStates := []struct{ detail, files, diff bool }{
		{false, false, false},
		{true, true, true},
		{true, false, false},
	}
	const ncommits = 12

	for _, sz := range sizes {
		w, h := sz[0], sz[1]
		for _, ls := range loadingStates {
			for sel := 0; sel < ncommits; sel++ {
				t.Run("", func(t *testing.T) {
					m := newRenderModel(w, h, ncommits, sel, 0)
					m.detailLoading = ls.detail
					m.filesLoading = ls.files
					m.diffLoading = ls.diff
					// Simulate "detail not yet loaded" — empty detail, empty files.
					if ls.detail {
						m.detail = gitcmd.CommitDetail{}
						m.detailSHA = ""
						m.files = nil
						m.filesSHA = ""
					}
					out := m.View()
					gotLines := strings.Count(out, "\n") + 1
					if gotLines != h {
						t.Errorf("w=%d h=%d loading=%v selectedIdx=%d: View() produced %d lines, want %d", w, h, ls, sel, gotLines, h)
					}
					for i, ln := range strings.Split(out, "\n") {
						if gotW := lipgloss.Width(ln); gotW != w {
							t.Errorf("w=%d h=%d loading=%v selectedIdx=%d: row %d width = %d, want %d", w, h, ls, sel, i, gotW, w)
							break
						}
					}
				})
			}
		}
	}
}

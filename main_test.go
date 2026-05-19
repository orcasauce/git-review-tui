package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
		active:         sectionLog,
		middleTab:      tabFiles,
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

// TestMessagePanelRendersBodyOnly asserts the message panel renders
// only the commit body — the sha/author/tags header lives in the
// dedicated metadata panel and must not appear here. The first body
// row starts at msgScroll, so scrolling moves body content directly
// (no sticky header to skip past).
func TestMessagePanelRendersBodyOnly(t *testing.T) {
	const w, h = 120, 40
	m := newRenderModel(w, h, 12, 5, 0)
	// Body must be long enough that scrolling moves something — the
	// panel's body height is layout.MiddleContentRows in full mode.
	bodyLines := []string{"subj line"}
	for i := 0; i < 40; i++ {
		bodyLines = append(bodyLines, "body line "+strings.Repeat("x", 1))
	}
	m.detail.Body = strings.Join(bodyLines, "\n")
	m.msgScroll = 0

	lo := layout.Compute(w, h)
	panel := renderMessagePanel(m, lo.Message.W, lo.Message.H, true, false)
	lines := strings.Split(panel, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines, got %d", len(lines))
	}
	// Row 0 is the title row; rows 1..N are the body.
	if strings.Contains(lines[1], m.detail.ShortSHA) {
		t.Errorf("row 1 should not contain short SHA (metadata panel owns the header now): %q", lines[1])
	}
	if !strings.Contains(lines[1], "subj line") {
		t.Errorf("row 1 should be the first body line, got %q", lines[1])
	}

	// With msgScroll == 1, the visible window slides up by one — row 1
	// of the panel is now the *second* body line.
	m.msgScroll = 1
	panel = renderMessagePanel(m, lo.Message.W, lo.Message.H, true, false)
	lines = strings.Split(panel, "\n")
	if strings.Contains(lines[1], "subj line") {
		t.Errorf("with msgScroll=1 row 1 should have advanced past 'subj line', got %q", lines[1])
	}
	if !strings.Contains(lines[1], "body line") {
		t.Errorf("with msgScroll=1 row 1 should be a body line, got %q", lines[1])
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

// TestMetadataPanelRendersSummaryRows asserts the dedicated metadata
// panel writes the three metadata.Summary rows (short sha, author/date,
// tags) into its rect — replacing the slice-3 blank placeholder.
func TestMetadataPanelRendersSummaryRows(t *testing.T) {
	const w, h = 120, 40
	m := newRenderModel(w, h, 5, 0, 0)
	m.detail.Tags = []gitcmd.TagInfo{{Name: "v1.2.3"}}

	lo := layout.Compute(w, h)
	panel := renderMetadataPanel(m, lo.Metadata.W, lo.Metadata.H)
	lines := strings.Split(panel, "\n")

	if got := len(lines); got != layout.MetadataContentRows {
		t.Fatalf("metadata panel produced %d rows, want %d", got, layout.MetadataContentRows)
	}
	if !strings.Contains(lines[0], m.detail.ShortSHA) {
		t.Errorf("row 0 missing short sha %q: %q", m.detail.ShortSHA, lines[0])
	}
	if !strings.Contains(lines[1], m.detail.AuthorName) {
		t.Errorf("row 1 missing author %q: %q", m.detail.AuthorName, lines[1])
	}
	if !strings.Contains(lines[2], "Tags:") || !strings.Contains(lines[2], "v1.2.3") {
		t.Errorf("row 2 missing tags summary: %q", lines[2])
	}
	for i, ln := range lines {
		if gotW := lipgloss.Width(ln); gotW != lo.Metadata.W {
			t.Errorf("row %d width = %d, want %d", i, gotW, lo.Metadata.W)
		}
	}
}

// TestMetadataPanelEmptyDetailRendersBlank asserts that when the detail
// has not loaded yet (SHA==""), the metadata panel produces h blank
// rows of width w — no "loading…" placeholder, no panic.
func TestMetadataPanelEmptyDetailRendersBlank(t *testing.T) {
	const w, h = 120, 40
	m := newRenderModel(w, h, 5, 0, 0)
	m.detail = gitcmd.CommitDetail{}

	lo := layout.Compute(w, h)
	panel := renderMetadataPanel(m, lo.Metadata.W, lo.Metadata.H)
	lines := strings.Split(panel, "\n")
	if got := len(lines); got != layout.MetadataContentRows {
		t.Fatalf("got %d rows, want %d", got, layout.MetadataContentRows)
	}
	for i, ln := range lines {
		if strings.TrimSpace(ln) != "" {
			t.Errorf("row %d not blank: %q", i, ln)
		}
		if gotW := lipgloss.Width(ln); gotW != lo.Metadata.W {
			t.Errorf("row %d width = %d, want %d", i, gotW, lo.Metadata.W)
		}
	}
}

// updateKey feeds a single tea.KeyMsg through the model's Update method
// and returns the resulting model. Keeps the navigation tests terse —
// the caller only cares about which section / middleTab the model
// landed on, not the returned tea.Cmd.
func updateKey(m model, msg tea.KeyMsg) model {
	mi, _ := m.Update(msg)
	return mi.(model)
}

// TestTabCycleFullMode asserts Tab cycles m.active through the four
// scrollable panels in order: log → files → message → diff → log.
// Shift+Tab walks the cycle in reverse. The terminal size is well
// above SmallModeMinCols so we exercise the full-mode branch.
func TestTabCycleFullMode(t *testing.T) {
	const w, h = 200, 50
	if layout.Compute(w, h).SmallMode {
		t.Fatalf("expected full mode at %dx%d", w, h)
	}
	m := newRenderModel(w, h, 5, 0, 0)
	want := []section{sectionLog, sectionFiles, sectionMessage, sectionDiff, sectionLog}
	for i, exp := range want[1:] {
		m = updateKey(m, tea.KeyMsg{Type: tea.KeyTab})
		if m.active != exp {
			t.Errorf("Tab step %d: m.active = %d, want %d", i+1, m.active, exp)
		}
	}
	// Shift+Tab walks back: log → diff → message → files → log.
	wantRev := []section{sectionDiff, sectionMessage, sectionFiles, sectionLog}
	for i, exp := range wantRev {
		m = updateKey(m, tea.KeyMsg{Type: tea.KeyShiftTab})
		if m.active != exp {
			t.Errorf("Shift+Tab step %d: m.active = %d, want %d", i+1, m.active, exp)
		}
	}
}

// TestTabCycleFullModeSkipsMiddleTab asserts that Ctrl+h / Ctrl+l are
// a no-op outside small mode — the middleTab field exists but is
// ignored when the full-mode layout is showing all three middle
// panels simultaneously, so rotating it shouldn't change focus or
// scroll any panel.
func TestTabCycleFullModeSkipsMiddleTab(t *testing.T) {
	const w, h = 200, 50
	if layout.Compute(w, h).SmallMode {
		t.Fatalf("expected full mode at %dx%d", w, h)
	}
	m := newRenderModel(w, h, 5, 0, 0)
	m.active = sectionMessage
	m.middleTab = tabFiles
	m2 := updateKey(m, tea.KeyMsg{Type: tea.KeyCtrlL})
	if m2.active != sectionMessage {
		t.Errorf("Ctrl+l in full mode changed active: %d", m2.active)
	}
	if m2.middleTab != tabFiles {
		t.Errorf("Ctrl+l in full mode rotated middleTab: %d", m2.middleTab)
	}
	m3 := updateKey(m, tea.KeyMsg{Type: tea.KeyCtrlH})
	if m3.active != sectionMessage || m3.middleTab != tabFiles {
		t.Errorf("Ctrl+h in full mode mutated state: active=%d middleTab=%d", m3.active, m3.middleTab)
	}
}

// TestTabCycleSmallMode asserts Tab cycles m.active across the three
// regions (log → middle → diff → log) in small mode. The "middle"
// region's section is derived from middleTab — tabFiles → sectionFiles
// and tabMessage → sectionMessage.
func TestTabCycleSmallMode(t *testing.T) {
	// Pick dimensions below SmallModeMinCols but above MinRows so the
	// layout reports SmallMode without falling through to TooSmall.
	w, h := layout.SmallModeMinCols-1, layout.MinRows+5
	lo := layout.Compute(w, h)
	if !lo.SmallMode || lo.TooSmall {
		t.Fatalf("expected small-mode-but-renderable layout at %dx%d, got SmallMode=%v TooSmall=%v",
			w, h, lo.SmallMode, lo.TooSmall)
	}
	m := newRenderModel(w, h, 5, 0, 0)
	// middleTab defaults to tabFiles → Tab from log enters middle as files.
	m = updateKey(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.active != sectionFiles {
		t.Errorf("small-mode Tab from log: active = %d, want sectionFiles", m.active)
	}
	m = updateKey(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.active != sectionDiff {
		t.Errorf("small-mode Tab from middle: active = %d, want sectionDiff", m.active)
	}
	m = updateKey(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.active != sectionLog {
		t.Errorf("small-mode Tab from diff: active = %d, want sectionLog", m.active)
	}
	// Now flip the middle tab to message and re-enter — focus should
	// land on sectionMessage instead of sectionFiles.
	m.middleTab = tabMessage
	m = updateKey(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.active != sectionMessage {
		t.Errorf("small-mode Tab from log w/ middleTab=tabMessage: active = %d, want sectionMessage", m.active)
	}
}

// TestCtrlHLRotatesMiddleTabSmallMode asserts Ctrl+h / Ctrl+l rotate
// middleTab through metadata → files → message when focus is in the
// middle region, and that m.active is updated to match focusable tabs
// (sectionFiles / sectionMessage). When the rotation lands on
// tabMetadata, m.active is left alone — metadata has no scroll state,
// so keystrokes continue to target the last focused middle panel.
func TestCtrlHLRotatesMiddleTabSmallMode(t *testing.T) {
	w, h := layout.SmallModeMinCols-1, layout.MinRows+5
	if !layout.Compute(w, h).SmallMode {
		t.Fatalf("expected small mode at %dx%d", w, h)
	}
	m := newRenderModel(w, h, 5, 0, 0)
	m.active = sectionFiles // focus is in the middle region
	m.middleTab = tabFiles

	// Ctrl+l: tabFiles → tabMessage (also moves focus to sectionMessage).
	m = updateKey(m, tea.KeyMsg{Type: tea.KeyCtrlL})
	if m.middleTab != tabMessage {
		t.Errorf("Ctrl+l: middleTab = %d, want tabMessage", m.middleTab)
	}
	if m.active != sectionMessage {
		t.Errorf("Ctrl+l: active = %d, want sectionMessage", m.active)
	}
	// Ctrl+l: tabMessage → tabMetadata (active stays at sectionMessage).
	prevActive := m.active
	m = updateKey(m, tea.KeyMsg{Type: tea.KeyCtrlL})
	if m.middleTab != tabMetadata {
		t.Errorf("Ctrl+l onto metadata: middleTab = %d, want tabMetadata", m.middleTab)
	}
	if m.active != prevActive {
		t.Errorf("Ctrl+l onto metadata: active should be unchanged, got %d (was %d)", m.active, prevActive)
	}
	// Ctrl+l: tabMetadata → tabFiles (active jumps to sectionFiles).
	m = updateKey(m, tea.KeyMsg{Type: tea.KeyCtrlL})
	if m.middleTab != tabFiles {
		t.Errorf("Ctrl+l wrapping to files: middleTab = %d, want tabFiles", m.middleTab)
	}
	if m.active != sectionFiles {
		t.Errorf("Ctrl+l wrapping to files: active = %d, want sectionFiles", m.active)
	}
	// Ctrl+h walks backward: tabFiles → tabMetadata.
	m = updateKey(m, tea.KeyMsg{Type: tea.KeyCtrlH})
	if m.middleTab != tabMetadata {
		t.Errorf("Ctrl+h: middleTab = %d, want tabMetadata", m.middleTab)
	}
}

// TestMiddleTabStripLabelsAndActiveMarker asserts renderMiddleTabStrip
// returns a single row containing all three labels (metadata / files /
// message) and that the "▸ " marker precedes the active label while
// the other two are prefixed with two spaces.
func TestMiddleTabStripLabelsAndActiveMarker(t *testing.T) {
	cases := []struct {
		active middleTab
		name   string
	}{
		{tabMetadata, "metadata"},
		{tabFiles, "files"},
		{tabMessage, "message"},
	}
	const w = 80
	for _, tc := range cases {
		strip := renderMiddleTabStrip(tc.active, w)
		if strings.Contains(strip, "\n") {
			t.Errorf("active=%s: strip should be a single row, got %q", tc.name, strip)
		}
		if !strings.Contains(strip, "metadata") || !strings.Contains(strip, "files") || !strings.Contains(strip, "message") {
			t.Errorf("active=%s: strip missing one of the labels: %q", tc.name, strip)
		}
		if !strings.Contains(strip, "▸ "+tc.name) {
			t.Errorf("active=%s: expected '▸ %s' marker in strip, got %q", tc.name, tc.name, strip)
		}
		// Visible width must match the requested width so the column
		// budget of the middle region is consumed exactly.
		if got := lipgloss.Width(strip); got != w {
			t.Errorf("active=%s: width = %d, want %d", tc.name, got, w)
		}
	}
}

// TestSmallModeViewRendersTabStripAboveMiddleBody asserts View() in
// small mode emits the middle tab strip on the row immediately after
// the log region (the row index equal to Log.H + 1 for the vGap), and
// that the row's content lists the three tab labels with "▸ " in front
// of the active one. The row below the strip must be the active tab's
// body — for tabFiles the first body row contains one of the loaded
// file paths.
func TestSmallModeViewRendersTabStripAboveMiddleBody(t *testing.T) {
	w, h := layout.SmallModeMinCols-1, layout.MinRows+5
	lo := layout.Compute(w, h)
	if !lo.SmallMode || lo.TooSmall {
		t.Fatalf("expected small-mode-but-renderable layout at %dx%d", w, h)
	}
	m := newRenderModel(w, h, 5, 0, 0)
	m.middleTab = tabFiles

	out := m.View()
	rows := strings.Split(out, "\n")
	stripRow := lo.Log.H + 1 // log rows + 1-row vGap
	if stripRow >= len(rows) {
		t.Fatalf("view produced %d rows, expected at least %d", len(rows), stripRow+1)
	}
	strip := rows[stripRow]
	for _, label := range []string{"metadata", "files", "message"} {
		if !strings.Contains(strip, label) {
			t.Errorf("tab strip row %d missing %q label: %q", stripRow, label, strip)
		}
	}
	if !strings.Contains(strip, "▸ files") {
		t.Errorf("tab strip row %d should mark 'files' as active: %q", stripRow, strip)
	}
	// The row below the strip is the first row of the active tab's
	// body — for tabFiles, the loaded file list starts there.
	bodyRow := rows[stripRow+1]
	if !strings.Contains(bodyRow, "main.go") {
		t.Errorf("expected body row %d (under strip) to contain a file path, got %q", stripRow+1, bodyRow)
	}

	// Flip to tabMessage and re-render — the strip's marker should
	// move, and the body row should contain message content rather
	// than a file path.
	m.middleTab = tabMessage
	out = m.View()
	rows = strings.Split(out, "\n")
	strip = rows[stripRow]
	if !strings.Contains(strip, "▸ message") {
		t.Errorf("after middleTab=tabMessage, strip should mark 'message' as active: %q", strip)
	}
	if strings.Contains(rows[stripRow+1], "main.go") {
		t.Errorf("after middleTab=tabMessage, body row should not show files: %q", rows[stripRow+1])
	}
}

// TestCtrlHLNoOpOutsideMiddleSmallMode asserts Ctrl+h / Ctrl+l do
// nothing when focus is not in the middle region — pressing them in
// sectionLog or sectionDiff must not rotate middleTab (which would
// silently swap what's rendered behind the focused panel).
func TestCtrlHLNoOpOutsideMiddleSmallMode(t *testing.T) {
	w, h := layout.SmallModeMinCols-1, layout.MinRows+5
	if !layout.Compute(w, h).SmallMode {
		t.Fatalf("expected small mode at %dx%d", w, h)
	}
	m := newRenderModel(w, h, 5, 0, 0)
	m.active = sectionLog
	m.middleTab = tabFiles

	m2 := updateKey(m, tea.KeyMsg{Type: tea.KeyCtrlL})
	if m2.middleTab != tabFiles || m2.active != sectionLog {
		t.Errorf("Ctrl+l on log: middleTab=%d active=%d, want tabFiles/sectionLog", m2.middleTab, m2.active)
	}

	m.active = sectionDiff
	m3 := updateKey(m, tea.KeyMsg{Type: tea.KeyCtrlH})
	if m3.middleTab != tabFiles || m3.active != sectionDiff {
		t.Errorf("Ctrl+h on diff: middleTab=%d active=%d, want tabFiles/sectionDiff", m3.middleTab, m3.active)
	}
}

// containsThumb reports whether s contains the scrollbar thumb glyph.
func containsThumb(s string) bool { return strings.Contains(s, "█") }

// TestLogPanelScrollbarWhenOverflowing asserts that when len(commits)
// exceeds the log panel's body height, a vertical scrollbar column is
// drawn on the right edge — at least one body row contains the thumb
// glyph. The panel's overall width must remain unchanged (the column
// is reclaimed from the body, not added beside it).
func TestLogPanelScrollbarWhenOverflowing(t *testing.T) {
	const w, h = 200, 50
	lo := layout.Compute(w, h)
	bodyH := lo.Log.H - 1
	// Make len(commits) clearly exceed the viewport.
	m := newRenderModel(w, h, bodyH*3, 0, 0)
	panel := renderLogPanel(m, lo.Log.W, lo.Log.H, true)
	rows := strings.Split(panel, "\n")
	if got := len(rows); got != lo.Log.H {
		t.Fatalf("log panel row count = %d, want %d", got, lo.Log.H)
	}
	for i, r := range rows {
		if gotW := lipgloss.Width(r); gotW != lo.Log.W {
			t.Errorf("row %d width = %d, want %d", i, gotW, lo.Log.W)
		}
	}
	bodyRows := rows[1:]
	thumbCount := 0
	for _, r := range bodyRows {
		if containsThumb(r) {
			thumbCount++
		}
	}
	if thumbCount == 0 {
		t.Errorf("expected at least one body row to contain the scrollbar thumb glyph, got none")
	}
}

// TestLogPanelNoScrollbarWhenContentFits asserts that when commits fit
// inside the viewport, no scrollbar column is drawn — body rows
// occupy the full panel width and contain no thumb glyph.
func TestLogPanelNoScrollbarWhenContentFits(t *testing.T) {
	const w, h = 200, 50
	lo := layout.Compute(w, h)
	bodyH := lo.Log.H - 1
	// Fewer commits than rows: content fits.
	m := newRenderModel(w, h, bodyH-1, 0, 0)
	panel := renderLogPanel(m, lo.Log.W, lo.Log.H, true)
	for i, r := range strings.Split(panel, "\n") {
		if containsThumb(r) {
			t.Errorf("row %d unexpectedly contains thumb glyph: %q", i, r)
		}
	}
}

// TestMessagePanelScrollbarWhenOverflowing asserts the message panel
// renders a scrollbar thumb on its body when the message body exceeds
// the panel's visible height.
func TestMessagePanelScrollbarWhenOverflowing(t *testing.T) {
	const w, h = 200, 50
	m := newRenderModel(w, h, 5, 0, 0)
	bodyLines := make([]string, 80)
	for i := range bodyLines {
		bodyLines[i] = "line"
	}
	m.detail.Body = strings.Join(bodyLines, "\n")

	lo := layout.Compute(w, h)
	panel := renderMessagePanel(m, lo.Message.W, lo.Message.H, true, false)
	rows := strings.Split(panel, "\n")
	if got := len(rows); got != lo.Message.H {
		t.Fatalf("message panel row count = %d, want %d", got, lo.Message.H)
	}
	thumbCount := 0
	for _, r := range rows[1:] {
		if containsThumb(r) {
			thumbCount++
		}
	}
	if thumbCount == 0 {
		t.Errorf("expected scrollbar thumb in message body, got none")
	}
}

// TestFilesPanelScrollbarWhenOverflowing asserts the files panel renders
// a scrollbar thumb when len(m.files) exceeds the panel's body height.
func TestFilesPanelScrollbarWhenOverflowing(t *testing.T) {
	const w, h = 200, 50
	m := newRenderModel(w, h, 5, 0, 0)
	lo := layout.Compute(w, h)
	bodyH := lo.Files.H - 1
	files := make([]gitcmd.FileStat, bodyH*3)
	for i := range files {
		files[i] = gitcmd.FileStat{Status: "M", Path: "path/to/file"}
	}
	m.files = files

	panel := renderFilesPanel(m, lo.Files.W, lo.Files.H, true, false)
	rows := strings.Split(panel, "\n")
	thumbCount := 0
	for _, r := range rows[1:] {
		if containsThumb(r) {
			thumbCount++
		}
	}
	if thumbCount == 0 {
		t.Errorf("expected scrollbar thumb in files body, got none")
	}
}

// TestDiffPanelNoScrollbarOnPlaceholder asserts the diff panel does not
// draw a scrollbar when it's showing a placeholder (e.g. "loading…"),
// even if a prior diff with many lines is still in memory (so long as
// the panel is not in stale-render mode).
func TestDiffPanelNoScrollbarOnPlaceholder(t *testing.T) {
	const w, h = 200, 50
	m := newRenderModel(w, h, 5, 0, 0)
	// No diff loaded for the currently-selected file — placeholder
	// branch fires.
	m.diffSHA = ""
	m.diff.Lines = nil

	lo := layout.Compute(w, h)
	panel := renderDiffPanel(m, lo.Diff.W, lo.Diff.H, true, false)
	for i, r := range strings.Split(panel, "\n") {
		if containsThumb(r) {
			t.Errorf("row %d unexpectedly contains thumb glyph on placeholder: %q", i, r)
		}
	}
}

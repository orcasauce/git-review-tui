package main

import (
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/orcasauce/git-review-tui/filefilter"
	"github.com/orcasauce/git-review-tui/fileid"
	"github.com/orcasauce/git-review-tui/gitcmd"
	"github.com/orcasauce/git-review-tui/layout"
	"github.com/orcasauce/git-review-tui/loader"
	"github.com/orcasauce/git-review-tui/revertstate"
)

func init() {
	// Force a 256-color profile so style.Render() emits ANSI escapes
	// even when tests run without a TTY — required for tests that
	// assert on the dim styling applied to non-matching commit rows.
	lipgloss.SetColorProfile(termenv.ANSI256)
}

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
	visible := make([]int, len(files))
	for i := range files {
		visible[i] = i
	}
	m := model{
		w: w, h: h,
		active:            sectionLog,
		middleTab:         tabFiles,
		commits:           commits,
		selectedIdx:       selectedIdx,
		viewportTop:       viewportTop,
		branch:            "main",
		pendingActions:    map[string]ActionKind{},
		reverts:           revertstate.New(),
		fileIDs:           fileid.New(),
		hunkTotals:        map[string]int{},
		filesByCommit:     map[string][]gitcmd.FileStat{},
		files:             files,
		fileFilterVisible: visible,
		filesSHA:          commits[selectedIdx].SHA,
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
// scrollable panels in order: log → files → message → diff → log
// when the commit message overflows the message panel. Shift+Tab walks
// the cycle in reverse. The terminal size is well above
// SmallModeMinCols so we exercise the full-mode branch.
func TestTabCycleFullMode(t *testing.T) {
	const w, h = 200, 50
	if layout.Compute(w, h).SmallMode {
		t.Fatalf("expected full mode at %dx%d", w, h)
	}
	m := newRenderModel(w, h, 5, 0, 0)
	// Make the message body long enough to overflow the message panel,
	// so it stays a tab stop in the cycle.
	body := make([]string, 0, h*3)
	for i := 0; i < h*3; i++ {
		body = append(body, "body line")
	}
	m.detail.Body = strings.Join(body, "\n")
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

// TestTabCycleFullModeSkipsMessageWhenFits asserts that Tab and
// Shift+Tab skip the message panel when its content fits fully in the
// viewport (no scrollbar drawn). The user has nothing to do on a
// non-scrollable message panel, so tab cycles past it.
func TestTabCycleFullModeSkipsMessageWhenFits(t *testing.T) {
	const w, h = 200, 50
	if layout.Compute(w, h).SmallMode {
		t.Fatalf("expected full mode at %dx%d", w, h)
	}
	m := newRenderModel(w, h, 5, 0, 0)
	// Default body in newRenderModel is one line — fits comfortably.
	want := []section{sectionFiles, sectionDiff, sectionLog, sectionFiles}
	for i, exp := range want {
		m = updateKey(m, tea.KeyMsg{Type: tea.KeyTab})
		if m.active != exp {
			t.Errorf("Tab step %d: m.active = %d, want %d", i+1, m.active, exp)
		}
	}
	// Reverse: log → diff → files → log → diff.
	wantRev := []section{sectionLog, sectionDiff, sectionFiles, sectionLog}
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
	visible := make([]int, len(files))
	for i := range files {
		files[i] = gitcmd.FileStat{Status: "M", Path: "path/to/file"}
		visible[i] = i
	}
	m.files = files
	m.fileFilterVisible = visible

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

// TestNextTabSection covers full-mode tab cycling with and without
// the "message panel fits" skip. The small-mode cycle is handled
// separately in advanceTab and is not exercised here.
func TestNextTabSection(t *testing.T) {
	cases := []struct {
		name    string
		cur     section
		dir     int
		msgFits bool
		want    section
	}{
		// Forward, message does not fit — full cycle.
		{"fwd/no-fit/log", sectionLog, 1, false, sectionFiles},
		{"fwd/no-fit/files", sectionFiles, 1, false, sectionMessage},
		{"fwd/no-fit/message", sectionMessage, 1, false, sectionDiff},
		{"fwd/no-fit/diff", sectionDiff, 1, false, sectionLog},
		// Forward, message fits — skip message.
		{"fwd/fits/log", sectionLog, 1, true, sectionFiles},
		{"fwd/fits/files", sectionFiles, 1, true, sectionDiff},
		{"fwd/fits/diff", sectionDiff, 1, true, sectionLog},
		// Reverse, message does not fit.
		{"rev/no-fit/log", sectionLog, -1, false, sectionDiff},
		{"rev/no-fit/files", sectionFiles, -1, false, sectionLog},
		{"rev/no-fit/message", sectionMessage, -1, false, sectionFiles},
		{"rev/no-fit/diff", sectionDiff, -1, false, sectionMessage},
		// Reverse, message fits — skip message.
		{"rev/fits/log", sectionLog, -1, true, sectionDiff},
		{"rev/fits/files", sectionFiles, -1, true, sectionLog},
		{"rev/fits/diff", sectionDiff, -1, true, sectionFiles},
		// Transitional: focus is on sectionMessage when msgFits flips
		// to true (e.g., commit detail shrank). One step in each
		// direction lands on a neighbor — message itself is not
		// re-entered.
		{"fwd/fits/from-message", sectionMessage, 1, true, sectionDiff},
		{"rev/fits/from-message", sectionMessage, -1, true, sectionFiles},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := nextTabSection(c.cur, c.dir, c.msgFits)
			if got != c.want {
				t.Errorf("nextTabSection(%d, %d, %v) = %d, want %d", c.cur, c.dir, c.msgFits, got, c.want)
			}
		})
	}
}

// TestNextTabSectionSymmetry asserts that reverse undoes forward when
// the "fits" flag is stable, for every starting section that the
// cycle can land on. This is the property the user feels as
// "shift+tab takes me back where I was."
func TestNextTabSectionSymmetry(t *testing.T) {
	for _, fits := range []bool{false, true} {
		starts := []section{sectionLog, sectionFiles, sectionDiff}
		if !fits {
			starts = append(starts, sectionMessage)
		}
		for _, s := range starts {
			fwd := nextTabSection(s, 1, fits)
			back := nextTabSection(fwd, -1, fits)
			if back != s {
				t.Errorf("fits=%v: from %d forward→%d, reverse→%d (expected %d)", fits, s, fwd, back, s)
			}
		}
	}
}

// TestNextTabSectionMessageNeverSkippedWhenOverflow asserts the
// invariant that sectionMessage is always reachable via tab when its
// content overflows the panel. Cycling forward from each starting
// section (with msgFits=false) must visit sectionMessage exactly once
// before returning to the start.
func TestNextTabSectionMessageNeverSkippedWhenOverflow(t *testing.T) {
	starts := []section{sectionLog, sectionFiles, sectionMessage, sectionDiff}
	for _, s := range starts {
		visited := map[section]int{}
		cur := s
		for i := 0; i < 4; i++ {
			cur = nextTabSection(cur, 1, false)
			visited[cur]++
		}
		if visited[sectionMessage] != 1 {
			t.Errorf("start=%d: message visited %d times in a full cycle (want 1)", s, visited[sectionMessage])
		}
		if cur != s {
			t.Errorf("start=%d: after 4 forward steps, ended on %d (want %d)", s, cur, s)
		}
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

// TestFileFilterIntegration_HidesNonMatchingRows asserts that a
// committed file-filter Expr is applied end-to-end: the files panel
// renders only matching paths, the title row carries the expression
// and a (visible/total) count, and the selection re-anchors to the
// nearest visible original index.
func TestFileFilterIntegration_HidesNonMatchingRows(t *testing.T) {
	const w, h = 200, 50
	m := newRenderModel(w, h, 5, 0, 0)
	m.files = []gitcmd.FileStat{
		{Status: "M", Path: "main.go"},
		{Status: "M", Path: "docs/intro.md"},
		{Status: "A", Path: "internal/util.go"},
		{Status: "M", Path: "README.md"},
	}
	// Start selection on docs/intro.md (orig idx 1) so we can observe
	// the nearest-visible clamp behavior.
	m.filesSelectedIdx = 1
	visible := make([]int, len(m.files))
	for i := range m.files {
		visible[i] = i
	}
	m.fileFilterVisible = visible

	expr, err := filefilter.Parse("*.go")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	m.fileFilterExpr = expr
	m.recomputeVisibleFiles(1)

	if got, want := len(m.fileFilterVisible), 2; got != want {
		t.Fatalf("visible count = %d, want %d (only .go files)", got, want)
	}
	if origIdx := m.fileFilterVisible[0]; origIdx != 0 {
		t.Errorf("first visible orig idx = %d, want 0 (main.go)", origIdx)
	}
	if origIdx := m.fileFilterVisible[1]; origIdx != 2 {
		t.Errorf("second visible orig idx = %d, want 2 (internal/util.go)", origIdx)
	}
	// docs/intro.md (orig idx 1) is hidden, so the nearest visible by
	// original index is internal/util.go (orig idx 2) → visible pos 1.
	if got, want := m.filesSelectedIdx, 1; got != want {
		t.Errorf("filesSelectedIdx after clamp = %d, want %d", got, want)
	}

	lo := layout.Compute(w, h)
	panel := renderFilesPanel(m, lo.Files.W, lo.Files.H, true, false)
	if !strings.Contains(panel, "main.go") {
		t.Errorf("panel should show main.go, got %q", panel)
	}
	if !strings.Contains(panel, "internal/util.go") {
		t.Errorf("panel should show internal/util.go, got %q", panel)
	}
	if strings.Contains(panel, "README.md") || strings.Contains(panel, "intro.md") {
		t.Errorf("panel should not show .md files, got %q", panel)
	}
	// The filter expression and (N/M) counter now live in the bottom
	// status panel, not the files header. The styled renderer paints
	// `*` and `.go` in different colors, so strip ANSI before asserting.
	status := stripANSI(m.renderStatus(lo.Status.W))
	if !strings.Contains(status, "*.go") {
		t.Errorf("status row should contain expression *.go, got %q", status)
	}
	if !strings.Contains(status, "(2/4)") {
		t.Errorf("status row should contain (2/4) count, got %q", status)
	}
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// TestFileFilterIntegration_EmptyFilteredSetRendersBlank asserts that
// when a filter is active and no files match, the body renders blank
// rows rather than the "No changes" placeholder.
func TestFileFilterIntegration_EmptyFilteredSetRendersBlank(t *testing.T) {
	const w, h = 200, 50
	m := newRenderModel(w, h, 5, 0, 0)
	m.files = []gitcmd.FileStat{
		{Status: "M", Path: "README.md"},
		{Status: "M", Path: "docs/intro.md"},
	}
	visible := []int{0, 1}
	m.fileFilterVisible = visible
	m.filesSelectedIdx = 0

	expr, err := filefilter.Parse("*.go")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	m.fileFilterExpr = expr
	m.recomputeVisibleFiles(-1)

	if got := len(m.fileFilterVisible); got != 0 {
		t.Fatalf("visible count = %d, want 0", got)
	}
	lo := layout.Compute(w, h)
	panel := renderFilesPanel(m, lo.Files.W, lo.Files.H, true, false)
	if strings.Contains(panel, "No changes") {
		t.Errorf("filter-empty pane should not show No changes placeholder, got %q", panel)
	}
	if strings.Contains(panel, "README.md") || strings.Contains(panel, "intro.md") {
		t.Errorf("filter-empty pane should not show file paths, got %q", panel)
	}
}

// TestCommitMatchesFileFilter covers the per-commit match helper: a
// commit matches iff at least one of its files matches the Expr,
// evaluated against both Path and OldPath so renames are honoured on
// either side.
func TestCommitMatchesFileFilter(t *testing.T) {
	exprGo, _ := filefilter.Parse("*.go")
	exprMd, _ := filefilter.Parse("*.md")
	exprRename, _ := filefilter.Parse("legacy/*.go")
	files := []gitcmd.FileStat{
		{Status: "M", Path: "main.go"},
		{Status: "M", Path: "README.md"},
	}
	if !commitMatchesFileFilter(files, exprGo) {
		t.Errorf("*.go should match a commit with main.go")
	}
	if !commitMatchesFileFilter(files, exprMd) {
		t.Errorf("*.md should match a commit with README.md")
	}
	if commitMatchesFileFilter(files, exprRename) {
		t.Errorf("legacy/*.go should not match a commit with no legacy/ files")
	}
	// Rename: new path doesn't match but old path does.
	renamed := []gitcmd.FileStat{
		{Status: "R", Path: "current/util.go", OldPath: "legacy/util.go"},
	}
	if !commitMatchesFileFilter(renamed, exprRename) {
		t.Errorf("legacy/*.go should match via OldPath on a rename")
	}
	// Empty file set never matches.
	if commitMatchesFileFilter(nil, exprGo) {
		t.Errorf("nil file set should never match")
	}
}

// TestIsCommitDimmed_EmptyFilterNeverDims asserts that with no active
// file filter, no commit is dimmed regardless of the cached match map.
func TestIsCommitDimmed_EmptyFilterNeverDims(t *testing.T) {
	m := newRenderModel(200, 50, 3, 0, 0)
	m.commitFilterMatch = map[string]bool{"abc": false}
	if m.isCommitDimmed("abc") {
		t.Errorf("with empty filter, no commit should be dimmed")
	}
}

// TestIsCommitDimmed_UnknownShaNotDimmed asserts that a commit whose
// numstat has not yet been evaluated is rendered bright (not dim), so
// commits brighten "in place" as their data arrives rather than
// starting dim and brightening when matches are found.
func TestIsCommitDimmed_UnknownShaNotDimmed(t *testing.T) {
	m := newRenderModel(200, 50, 3, 0, 0)
	expr, _ := filefilter.Parse("*.go")
	m.fileFilterExpr = expr
	m.commitFilterMatch = map[string]bool{}
	if m.isCommitDimmed("unseen-sha") {
		t.Errorf("unknown sha should not be dimmed (unevaluated, not no-match)")
	}
}

// TestIsCommitDimmed_NoMatchDims asserts a commit whose numstat has
// been evaluated and contains no matching files is dimmed, while one
// with at least one matching file is not.
func TestIsCommitDimmed_NoMatchDims(t *testing.T) {
	m := newRenderModel(200, 50, 3, 0, 0)
	expr, _ := filefilter.Parse("*.go")
	m.fileFilterExpr = expr
	m.commitFilterMatch = map[string]bool{
		"match-sha":   true,
		"nomatch-sha": false,
	}
	if m.isCommitDimmed("match-sha") {
		t.Errorf("commit with at least one match should not be dimmed")
	}
	if !m.isCommitDimmed("nomatch-sha") {
		t.Errorf("commit with zero matches should be dimmed")
	}
}

// TestLogPanel_DimNonMatchingCommit asserts that a commit recorded as
// non-matching renders the log row with the commit-dim color, while a
// matching commit at a different row does not. The two test rows are
// chosen so neither is the selected row (which always wins styling).
func TestLogPanel_DimNonMatchingCommit(t *testing.T) {
	const w, h = 200, 50
	m := newRenderModel(w, h, 4, 0, 0)
	// Give each commit a unique SHA so the dim map can distinguish them.
	for i := range m.commits {
		m.commits[i].SHA = strings.Repeat(string(rune('a'+i)), 40)
		m.commits[i].ShortSHA = m.commits[i].SHA[:7]
	}
	expr, _ := filefilter.Parse("*.go")
	m.fileFilterExpr = expr
	// Row 0 is the selected row; pick row 1 (match) and row 2 (no match).
	m.commitFilterMatch = map[string]bool{
		m.commits[1].SHA: true,
		m.commits[2].SHA: false,
	}

	lo := layout.Compute(w, h)
	panel := renderLogPanel(m, lo.Log.W, lo.Log.H, true)
	rows := strings.Split(panel, "\n")
	// Row 0 = title, then row 1/2/3 are body indices 0/1/2.
	const dimSeq = "38;5;240"
	matchRow := rows[2]
	dimRow := rows[3]
	if strings.Contains(matchRow, dimSeq) {
		t.Errorf("matching commit row should not contain dim escape %q: %q", dimSeq, matchRow)
	}
	if !strings.Contains(dimRow, dimSeq) {
		t.Errorf("non-matching commit row should contain dim escape %q: %q", dimSeq, dimRow)
	}
}

// TestNumStatResult_UpdatesCommitDimMap asserts that when a
// NumStatResult arrives for any commit (including ones the user has
// not selected), the commit-dim map is populated, satisfying story 27:
// commits dim as their numstat data arrives.
func TestNumStatResult_UpdatesCommitDimMap(t *testing.T) {
	m := newRenderModel(200, 50, 2, 0, 0)
	for i := range m.commits {
		m.commits[i].SHA = strings.Repeat(string(rune('a'+i)), 40)
		m.commits[i].ShortSHA = m.commits[i].SHA[:7]
	}
	expr, _ := filefilter.Parse("*.go")
	m.fileFilterExpr = expr
	// The selected commit (index 0) gets a numstat that does NOT match.
	selSHA := m.commits[0].SHA
	m.filesSHA = ""
	updated, _ := m.Update(loader.NumStatResult{
		SHA:   selSHA,
		Files: []gitcmd.FileStat{{Status: "M", Path: "README.md"}},
	})
	m = updated.(model)
	got, ok := m.commitFilterMatch[selSHA]
	if !ok {
		t.Fatalf("dim map missing entry for %s after NumStatResult", selSHA)
	}
	if got {
		t.Errorf("commit with no .go files should be recorded as non-matching")
	}
}

// TestFileFilterPrompt_PersistsAcrossSelectionAndRefresh asserts the
// committed file filter is preserved when fresh NumStatResults arrive
// (commit selection change, ctrl+r refresh) — the filter expression
// stays set and the visible list is recomputed against the new files.
func TestFileFilterPrompt_PersistsAcrossNumStatResults(t *testing.T) {
	m := newRenderModel(200, 50, 5, 0, 0)
	expr, _ := filefilter.Parse("*.go")
	m.fileFilterExpr = expr
	m.recomputeVisibleFiles(-1)

	// Simulate a brand-new file set arriving from a different commit.
	m.files = []gitcmd.FileStat{
		{Status: "A", Path: "cmd/tool/main.go"},
		{Status: "M", Path: "CHANGELOG.md"},
	}
	m.recomputeVisibleFiles(-1)
	if got := len(m.fileFilterVisible); got != 1 {
		t.Fatalf("after refresh, visible count = %d, want 1", got)
	}
	if m.fileFilterExpr.String() != "*.go" {
		t.Errorf("filter expr lost: %q", m.fileFilterExpr.String())
	}
}

// TestFileFilter_BugRepro_HEAD_25bdcec reproduces the user-reported
// filter anomaly against the captured fixture of the orcasauce/
// git-review-tui repo at HEAD commit 25bdcec, which touches exactly
// three files:
//
//	loader/loader.go
//	main.go
//	main_test.go
//
// Reported failures:
//
//  1. Filter `main` reported "5 matches" in the files-pane indicator —
//     impossible if the indicator's N is `len(fileFilterVisible)` over
//     a 3-file commit. Expected for a bare-basename glob: zero matches
//     (none of the basenames equal "main" exactly).
//  2. Filter `main.go` reported "no matches" despite `main.go` being
//     present in the commit. Expected: exactly one match, the `main.go`
//     entry (basename equals the pattern).
//
// This test drives recomputeVisibleFiles end-to-end against the real
// file slice for that commit and asserts the visible set. If the bug
// is in the matcher / parser, this test will fail. If the test passes,
// the matcher is innocent and the live failure must originate from
// model state interactions (e.g. stale `m.files` during the loader
// debounce window when commit selection changes).
func TestFileFilter_BugRepro_HEAD_25bdcec(t *testing.T) {
	const headSHA = "25bdcec19af6992ac0b5ff47e68df1303fa658a7"

	fx := loadRepoFixture(t, "testdata/fixtures/bug-25bdcec.json")
	if fx.Head != headSHA {
		t.Fatalf("fixture head = %s, want %s", fx.Head, headSHA)
	}
	files := numStatFor(t, fx, headSHA)
	if got := len(files); got != 3 {
		t.Fatalf("fixture HEAD has %d files, want 3 (loader/loader.go, main.go, main_test.go)", got)
	}
	wantPaths := map[string]bool{
		"loader/loader.go": true,
		"main.go":          true,
		"main_test.go":     true,
	}
	for _, f := range files {
		if !wantPaths[f.Path] {
			t.Fatalf("unexpected file in HEAD fixture: %q", f.Path)
		}
	}

	m := newRenderModel(200, 50, 5, 0, 0)
	m.files = files
	m.filesSHA = headSHA

	type expect struct {
		query       string
		visibleN    int
		visiblePath string // when visibleN == 1
	}
	cases := []expect{
		// `main` has no glob metacharacters, so it substring-matches
		// the basename: main.go and main_test.go both contain "main".
		// loader/loader.go does not.
		{query: "main", visibleN: 2},
		// `main.go` substring-matches the basename "main.go" only.
		{query: "main.go", visibleN: 1, visiblePath: "main.go"},
		// Sanity baseline: `*.go` is a basename glob matching every .go.
		{query: "*.go", visibleN: 3},
		// Negated plain string excludes anything whose basename
		// contains "main".
		{query: "!main", visibleN: 1, visiblePath: "loader/loader.go"},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			expr, err := filefilter.Parse(tc.query)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tc.query, err)
			}
			m.fileFilterExpr = expr
			m.recomputeVisibleFiles(-1)

			if got := len(m.fileFilterVisible); got != tc.visibleN {
				var visiblePaths []string
				for _, idx := range m.fileFilterVisible {
					visiblePaths = append(visiblePaths, m.files[idx].Path)
				}
				t.Fatalf("filter %q: visible = %d %v, want %d",
					tc.query, got, visiblePaths, tc.visibleN)
			}
			if tc.visibleN == 1 {
				gotPath := m.files[m.fileFilterVisible[0]].Path
				if gotPath != tc.visiblePath {
					t.Errorf("filter %q: visible path = %q, want %q",
						tc.query, gotPath, tc.visiblePath)
				}
			}
		})
	}
}


func TestDeleteLastFilterToken(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"single token", "foo", ""},
		{"single token with trailing whitespace", "foo  ", ""},
		{"two tokens", "foo, bar", "foo"},
		{"two tokens tight comma", "foo,bar", "foo"},
		// `foo,` parses to a single token "foo" at [0,3]; the function
		// then trims that span's leading whitespace/comma and ends up
		// at cut=0, clearing input. This is arguably surprising (the
		// user might expect to land on "foo") but it is the current
		// behavior — captured here so any future change is intentional.
		{"trailing comma clears all", "foo,", ""},
		{"trailing comma + space clears all", "foo, ", ""},
		{"three tokens", "foo, bar, baz", "foo, bar"},
		{"regex tail with comma inside", "foo, /a,b/", "foo"},
		{"regex tail preserves earlier regex", "/x,y/, bar", "/x,y/"},
		{"unclosed quote falls back to last literal comma", `foo, "bar`, "foo"},
		{"unclosed quote with no comma clears all", `"bar`, ""},
		{"negated token", "foo, !bar", "foo"},
		{"quoted comma-containing token", `foo, "a,b"`, "foo"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := deleteLastFilterToken(tc.input)
			if got != tc.want {
				t.Errorf("deleteLastFilterToken(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestAdvanceSelectedSkippingDimmed exercises the rule that
// keyboard navigation skips over consecutive dimmed (filter-known
// non-matching) commits, but never skips commits whose numstat hasn't
// loaded yet — those count as "unknown" and stop the cursor so it
// doesn't run away from the user while data is still streaming.
func TestAdvanceSelectedSkippingDimmed(t *testing.T) {
	mkCommits := func(shas ...string) []gitcmd.Commit {
		out := make([]gitcmd.Commit, len(shas))
		for i, s := range shas {
			out[i] = gitcmd.Commit{SHA: s, ShortSHA: s[:7]}
		}
		return out
	}
	mkExpr := func(t *testing.T, q string) filefilter.Expr {
		t.Helper()
		e, err := filefilter.Parse(q)
		if err != nil {
			t.Fatalf("Parse(%q): %v", q, err)
		}
		return e
	}

	type tc struct {
		name        string
		commits     []gitcmd.Commit
		match       map[string]bool
		startIdx    int
		dir         int
		wantMoved   bool
		wantNewIdx  int
	}
	commits := mkCommits(
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"cccccccccccccccccccccccccccccccccccccccc",
		"dddddddddddddddddddddddddddddddddddddddd",
		"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
	)
	cases := []tc{
		{
			name:    "skips run of dimmed forward",
			commits: commits,
			match: map[string]bool{
				commits[0].SHA: true,
				commits[1].SHA: false, // dim
				commits[2].SHA: false, // dim
				commits[3].SHA: true,
				commits[4].SHA: true,
			},
			startIdx: 0, dir: 1, wantMoved: true, wantNewIdx: 3,
		},
		{
			name:    "skips run of dimmed backward",
			commits: commits,
			match: map[string]bool{
				commits[0].SHA: true,
				commits[1].SHA: false,
				commits[2].SHA: false,
				commits[3].SHA: true,
			},
			startIdx: 3, dir: -1, wantMoved: true, wantNewIdx: 0,
		},
		{
			name:    "unknown commit stops the cursor (not skipped)",
			commits: commits,
			match: map[string]bool{
				commits[0].SHA: true,
				// commits[1] has no entry -> unknown, treated as not-dim.
				commits[2].SHA: true,
			},
			startIdx: 0, dir: 1, wantMoved: true, wantNewIdx: 1,
		},
		{
			name:    "no movement when at the edge with no eligible target",
			commits: commits,
			match: map[string]bool{
				commits[0].SHA: true,
				commits[1].SHA: false,
				commits[2].SHA: false,
				commits[3].SHA: false,
				commits[4].SHA: false,
			},
			startIdx: 0, dir: 1, wantMoved: false, wantNewIdx: 0,
		},
		{
			name: "no movement at start going up",
			commits: commits,
			match: map[string]bool{},
			startIdx: 0, dir: -1, wantMoved: false, wantNewIdx: 0,
		},
		{
			name: "bogus dir returns false",
			commits: commits,
			match: map[string]bool{},
			startIdx: 2, dir: 0, wantMoved: false, wantNewIdx: 2,
		},
		{
			name:    "empty commits returns false",
			commits: nil,
			match:   map[string]bool{},
			startIdx: 0, dir: 1, wantMoved: false, wantNewIdx: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := model{
				commits:           c.commits,
				selectedIdx:       c.startIdx,
				commitFilterMatch: c.match,
				fileFilterExpr:    mkExpr(t, "*.go"),
			}
			moved := m.advanceSelectedSkippingDimmed(c.dir)
			if moved != c.wantMoved {
				t.Errorf("moved = %v, want %v", moved, c.wantMoved)
			}
			if m.selectedIdx != c.wantNewIdx {
				t.Errorf("selectedIdx = %d, want %d", m.selectedIdx, c.wantNewIdx)
			}
		})
	}
}

// TestFileFilterPrompt_CtrlW_DeletesLastToken asserts that ctrl+w
// while the prompt is open drops the trailing comma-separated token
// from the in-progress input and re-parses, leaving the remaining
// tokens (and any prior debounced "last valid" expression) intact.
func TestFileFilterPrompt_CtrlW_DeletesLastToken(t *testing.T) {
	m := newRenderModel(200, 50, 5, 0, 0)
	m.openFileFilterPrompt()
	m.fileFilterPromptInput = "foo, bar"
	m.reparseFileFilterPrompt()

	updated, _ := m.updateFileFilterPrompt(tea.KeyMsg{Type: tea.KeyCtrlW})
	mm := updated.(model)
	if mm.fileFilterPromptInput != "foo" {
		t.Errorf("after ctrl+w, input = %q, want %q", mm.fileFilterPromptInput, "foo")
	}
	if !mm.fileFilterPromptActive {
		t.Error("ctrl+w should not close the prompt")
	}

	updated2, _ := mm.updateFileFilterPrompt(tea.KeyMsg{Type: tea.KeyCtrlW})
	mm2 := updated2.(model)
	if mm2.fileFilterPromptInput != "" {
		t.Errorf("after second ctrl+w, input = %q, want \"\"", mm2.fileFilterPromptInput)
	}

	// On empty input ctrl+w is a no-op (state unchanged, still open).
	updated3, _ := mm2.updateFileFilterPrompt(tea.KeyMsg{Type: tea.KeyCtrlW})
	mm3 := updated3.(model)
	if mm3.fileFilterPromptInput != "" {
		t.Errorf("ctrl+w on empty should be a no-op, got %q", mm3.fileFilterPromptInput)
	}
	if !mm3.fileFilterPromptActive {
		t.Error("ctrl+w on empty should leave prompt open")
	}
}

// TestFileFilterPrompt_CtrlK_ClearsInput asserts that ctrl+k while
// the prompt is open wipes the input and the debounce state so a
// stale "last valid" Expr from a prior keystroke can't leak through
// into the next reparse.
func TestFileFilterPrompt_CtrlK_ClearsInput(t *testing.T) {
	m := newRenderModel(200, 50, 5, 0, 0)
	m.openFileFilterPrompt()
	m.fileFilterPromptInput = "foo, bar, baz"
	m.reparseFileFilterPrompt()
	// Force the debounce state to be non-nil so we can verify ctrl+k
	// clears it. fileFilterLastValid is normally only populated when a
	// later edit invalidates a previously-valid expression.
	_, parsedTokens, _ := filefilter.ParsePartial("foo")
	if len(parsedTokens) == 0 {
		t.Fatal("setup: expected one parsed token")
	}
	t0 := parsedTokens[0]
	m.fileFilterLastValid = []*filefilter.Token{&t0}
	m.fileFilterInvalidSince = []time.Time{time.Now()}

	updated, _ := m.updateFileFilterPrompt(tea.KeyMsg{Type: tea.KeyCtrlU})
	// ctrl+u is not bound — make sure it does NOT clear (only ctrl+k does).
	mm := updated.(model)
	if mm.fileFilterPromptInput != "foo, bar, baz" {
		t.Errorf("ctrl+u should not be bound; input changed to %q", mm.fileFilterPromptInput)
	}

	updated2, _ := m.updateFileFilterPrompt(tea.KeyMsg{Type: tea.KeyCtrlK})
	mm2 := updated2.(model)
	if mm2.fileFilterPromptInput != "" {
		t.Errorf("after ctrl+k, input = %q, want \"\"", mm2.fileFilterPromptInput)
	}
	if mm2.fileFilterLastValid != nil {
		t.Error("ctrl+k should clear fileFilterLastValid")
	}
	if mm2.fileFilterInvalidSince != nil {
		t.Error("ctrl+k should clear fileFilterInvalidSince")
	}
	if !mm2.fileFilterPromptActive {
		t.Error("ctrl+k should not close the prompt")
	}

	// On empty input ctrl+k is a no-op.
	updated3, _ := mm2.updateFileFilterPrompt(tea.KeyMsg{Type: tea.KeyCtrlK})
	mm3 := updated3.(model)
	if mm3.fileFilterPromptInput != "" {
		t.Errorf("ctrl+k on empty should be a no-op, got %q", mm3.fileFilterPromptInput)
	}
}

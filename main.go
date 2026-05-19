// Command git-review-tui is a read-only TUI for browsing committed git
// history. v1 in progress: four titled panels, with a live commit list
// in the top-left fed by the gitcmd package.
package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/orcasauce/git-review-tui/diffrender"
	"github.com/orcasauce/git-review-tui/filelist"
	"github.com/orcasauce/git-review-tui/gitcmd"
	"github.com/orcasauce/git-review-tui/layout"
	"github.com/orcasauce/git-review-tui/loader"
	"github.com/orcasauce/git-review-tui/searchfilter"
)

type section int

const (
	sectionTop section = iota
	sectionBottom
)

// logPageSize is the number of commits fetched per call to gitcmd.Log.
// loadMoreThreshold is how close to the end of the loaded list the
// selection may get before we kick off another page.
const (
	logPageSize       = 500
	loadMoreThreshold = 50
)

type model struct {
	w, h   int
	active section

	git *gitcmd.Client
	ldr *loader.Loader

	// Loaded commits in reverse-chronological order. May be lazily
	// extended as the user scrolls toward the bottom.
	commits []gitcmd.Commit
	// loadedAll is set when a Log call returned fewer than logPageSize
	// commits, meaning we've reached the end of HEAD.
	loadedAll bool
	// loadingMore is true while a page request is in flight, to avoid
	// firing duplicate requests.
	loadingMore bool

	// Selection within `commits`. Only meaningful when active is top.
	selectedIdx int
	// viewportTop is the index of the first commit row rendered in
	// the panel body (i.e. the scroll offset).
	viewportTop int

	// detail is the loaded CommitDetail for detailSHA. May be empty
	// while a fetch is in flight or before the first load.
	detail    gitcmd.CommitDetail
	detailSHA string
	// msgScroll is the line offset (top-most visible body line) in
	// the message panel. Resets to 0 each time the selection changes.
	msgScroll int
	// pendingG is true after the user pressed `g` once and we are
	// waiting for a second `g` to complete the `gg` jump-to-top motion.
	// Cleared by any key other than the second `g`.
	pendingG bool

	// files holds the per-file change records for the currently-loaded
	// commit. filesSHA is the sha they were loaded for (so stale loads
	// can be discarded). filesSelectedIdx and filesViewportTop drive the
	// bottom-left panel.
	files            []gitcmd.FileStat
	filesSHA         string
	filesSelectedIdx int
	filesViewportTop int

	// diff is the parsed unified diff for the currently-selected
	// (commit, file) pair. diffSHA / diffPath identify which selection
	// the parsed result belongs to (so stale loads can be discarded).
	// diffScroll is the vertical line offset and diffHScroll is the
	// horizontal character offset into the diff panel.
	diff        diffrender.Result
	diffSHA     string
	diffPath    string
	diffScroll  int
	diffHScroll int
	// detailLoading / filesLoading / diffLoading track whether each
	// right/bottom panel is currently waiting on the loader. When true,
	// the panel title shows a spinner and the body (if any prior content
	// is present) is rendered dimmed to indicate it is stale.
	detailLoading bool
	filesLoading  bool
	diffLoading   bool

	// Binary file size info for the currently-selected binary file.
	// binSizeSHA / binSizePath identify which (commit, file) the sizes
	// belong to so stale loads are discarded. binHasOld / binHasNew flag
	// the pure-add and pure-delete cases where only one side exists.
	binOldSize     int64
	binNewSize     int64
	binHasOld      bool
	binHasNew      bool
	binSizeSHA     string
	binSizePath    string
	binSizeLoading bool

	// spinnerFrame indexes into the braille spinner glyphs.
	// spinnerScheduled is true while a spinnerTickMsg is in flight, so
	// the model never schedules duplicate ticks.
	spinnerFrame     int
	spinnerScheduled bool

	// errMsg is a transient error message rendered in the status row.
	// errMsgTime is when it was raised — used to render a timestamp
	// alongside the message and to discriminate stale auto-clear ticks.
	errMsg     string
	errMsgTime time.Time

	// branch is the short branch name of the current worktree, empty
	// when the worktree is detached or bare. worktreeLabel is the
	// basename of the current worktree's path, set only when more than
	// one worktree exists (so the log title can disambiguate which
	// checkout the user is reviewing).
	branch        string
	worktreeLabel string

	// Worktree picker modal state. When wtModalOpen is true the modal
	// owns all keyboard input until the user dismisses it. wtList is
	// freshly enumerated each time the modal opens so it reflects any
	// worktrees added since startup. wtModalIdx is the selected entry.
	wtModalOpen bool
	wtList      []gitcmd.Worktree
	wtModalIdx  int

	// Commit-list search state. searchActive is true while the user is
	// typing a query; once `enter` confirms (or `esc` cancels) it flips
	// back to false but the captured `searchMatches` stay around so
	// `n`/`N` can cycle through them. searchOriginIdx / searchOriginTop
	// remember the selection at the time the prompt opened so `esc`
	// restores it without disturbing the rest of the UI.
	searchActive    bool
	searchQuery     string
	searchMatches   []int
	searchMatchIdx  int
	searchOriginIdx int
	searchOriginTop int

	// Files-list search state. Mirrors the commit-list search state but
	// is scoped to the files panel for the currently-loaded commit.
	// fileSearchOriginIdx / fileSearchOriginTop capture the file
	// selection at the moment the prompt opened so `esc` can restore it.
	fileSearchActive    bool
	fileSearchQuery     string
	fileSearchMatches   []int
	fileSearchMatchIdx  int
	fileSearchOriginIdx int
	fileSearchOriginTop int

	// Small-mode swap state. When the layout is collapsed to a single
	// full-width column per section, each section shows its left panel
	// by default; `enter` swaps the active section to its right panel
	// (message for top, diff for bottom) and `q`/`esc` swaps back. The
	// flags persist across small-mode entry/exit so the user's chosen
	// view survives terminal resizes.
	topShowRight    bool
	bottomShowRight bool

	// Help overlay state. When helpModalOpen is true the modal owns all
	// keyboard input until the user dismisses it with `?`, `esc`, or `q`.
	helpModalOpen bool

	// statusMsg is a transient non-error message rendered in the status
	// row (e.g. "Refreshed"). Identical auto-clear semantics to errMsg
	// but rendered in a non-alarm color. errMsg takes precedence when
	// both are populated.
	statusMsg     string
	statusMsgTime time.Time

	// pendingRefreshSHA / pendingRefreshPath are set by `ctrl+r` so the
	// next logLoadedMsg / NumStatResult can restore the prior commit and
	// file selection by identity. If the previously-selected commit no
	// longer exists, the path is dropped as well — that context belongs
	// to a different commit now.
	pendingRefreshSHA  string
	pendingRefreshPath string
}

func initialModel(git *gitcmd.Client, branch, worktreeLabel string) model {
	ldr := loader.New(loader.Config{Source: git})
	return model{
		active:        sectionTop,
		git:           git,
		ldr:           ldr,
		branch:        branch,
		worktreeLabel: worktreeLabel,
	}
}

// logLoadedMsg delivers a Log response back to Update.
type logLoadedMsg struct {
	skip    int
	commits []gitcmd.Commit
	err     error
}

// binarySizeMsg delivers the byte sizes for a binary file at the
// selected commit and its parent. The two-sided lookup is collapsed
// into a single message so the diff panel can render the delta with
// one Update pass. hasOld / hasNew are false when one side of the
// change does not exist (pure add / pure delete).
type binarySizeMsg struct {
	sha     string
	path    string
	oldSize int64
	newSize int64
	hasOld  bool
	hasNew  bool
}

// spinnerTickMsg drives the braille spinner animation while any panel
// is loading. The model schedules a follow-up tick from the handler
// only when at least one panel is still waiting on data.
type spinnerTickMsg struct{}

// errClearMsg auto-clears a transient error after errAutoClear has
// passed. The handler ignores the message when a newer error has
// replaced the one this tick was scheduled for, identified by `at`.
type errClearMsg struct {
	at time.Time
}

const errAutoClear = 5 * time.Second

// raiseError stores err in the status row with the current time and
// returns a Cmd that auto-clears the message after errAutoClear unless
// a newer error has replaced it in the meantime.
func (m *model) raiseError(err error) tea.Cmd {
	m.errMsg = err.Error()
	m.errMsgTime = time.Now()
	at := m.errMsgTime
	return tea.Tick(errAutoClear, func(time.Time) tea.Msg {
		return errClearMsg{at: at}
	})
}

// clearError drops any in-flight transient error. Called from the
// success path of each loader result so a recovered command erases the
// last failure.
func (m *model) clearError() {
	m.errMsg = ""
	m.errMsgTime = time.Time{}
}

// statusClearMsg auto-clears a transient non-error status message after
// errAutoClear has passed, with the same staleness check as
// errClearMsg.
type statusClearMsg struct {
	at time.Time
}

// raiseStatus shows a transient non-error message in the status row
// (e.g. "Refreshed") for errAutoClear before auto-clearing.
func (m *model) raiseStatus(text string) tea.Cmd {
	m.statusMsg = text
	m.statusMsgTime = time.Now()
	at := m.statusMsgTime
	return tea.Tick(errAutoClear, func(time.Time) tea.Msg {
		return statusClearMsg{at: at}
	})
}

// clearStatus drops any in-flight transient status message.
func (m *model) clearStatus() {
	m.statusMsg = ""
	m.statusMsgTime = time.Time{}
}

const spinnerInterval = 100 * time.Millisecond

var spinnerFrames = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

func (m model) loadLogCmd(skip int) tea.Cmd {
	git := m.git
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cs, err := git.Log(ctx, skip, logPageSize)
		return logLoadedMsg{skip: skip, commits: cs, err: err}
	}
}

// loadBinarySizesCmd fires the two `git cat-file -s` lookups for a
// binary file's old and new sizes. Either lookup may be skipped (pure
// add has no parent side; pure delete has no child side). Errors on a
// single side are downgraded to hasOld/hasNew=false so the diff panel
// can still render the side that does exist.
func (m model) loadBinarySizesCmd(sha, parent, path, oldPath, status string) tea.Cmd {
	git := m.git
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		msg := binarySizeMsg{sha: sha, path: path}
		// New side exists for every status except pure delete ("D").
		if status != "D" {
			n, err := git.FileSize(ctx, sha, path)
			if err == nil {
				msg.newSize = n
				msg.hasNew = true
			}
		}
		// Old side exists for every status except pure add ("A"), and only
		// when the commit has a parent (the root commit has none).
		if status != "A" && parent != "" {
			src := path
			if oldPath != "" {
				src = oldPath
			}
			n, err := git.FileSize(ctx, parent, src)
			if err == nil {
				msg.oldSize = n
				msg.hasOld = true
			}
		}
		return msg
	}
}

func (m model) Init() tea.Cmd {
	return m.loadLogCmd(0)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.clampViewport()
		m.clampFilesViewport()
		m.clampDiffScroll()
		return m, nil

	case logLoadedMsg:
		m.loadingMore = false
		if msg.err != nil {
			return m, m.raiseError(msg.err)
		}
		m.clearError()
		firstLoad := len(m.commits) == 0
		if msg.skip == 0 {
			m.commits = msg.commits
		} else if msg.skip == len(m.commits) {
			m.commits = append(m.commits, msg.commits...)
		}
		// Anything else (overlapping or stale page) is dropped to avoid
		// duplicating rows; the loader will re-request the right offset.
		if len(msg.commits) < logPageSize {
			m.loadedAll = true
		}
		// Restore a refresh-preserved commit selection by sha if it
		// still exists in the new log. Only apply on the first page
		// (where m.commits was just replaced from scratch); a pending
		// pendingRefreshSHA at this point implies skip==0.
		if firstLoad && m.pendingRefreshSHA != "" {
			found := false
			for i, c := range m.commits {
				if c.SHA == m.pendingRefreshSHA {
					m.selectedIdx = i
					found = true
					break
				}
			}
			m.pendingRefreshSHA = ""
			if !found {
				// The commit is gone, so the captured file path
				// belongs to a different commit context now.
				m.pendingRefreshPath = ""
			}
		}
		m.clampViewport()
		// New page may contain additional matches for an active search;
		// re-rank and re-anchor searchMatchIdx onto the current selection.
		if m.searchQuery != "" {
			m.recomputeSearchMatches()
			m.searchMatchIdx = -1
			for i, idx := range m.searchMatches {
				if idx == m.selectedIdx {
					m.searchMatchIdx = i
					break
				}
			}
		}
		// On first load, kick off the detail + numstat fetches for the
		// initial selection so the message and files panels populate
		// without requiring any keystrokes.
		if firstLoad && len(m.commits) > 0 {
			sha := m.commits[m.selectedIdx].SHA
			m.detailLoading = true
			m.filesLoading = true
			return m, tea.Batch(m.ldr.LoadDetail(sha), m.ldr.LoadNumStat(sha), m.startSpinnerCmd())
		}
		return m, nil

	case loader.DetailResult:
		// Context-cancelled results are expected on rapid selection
		// changes; swallow them silently so they don't surface in the
		// error row.
		if errors.Is(msg.Err, context.Canceled) {
			return m, nil
		}
		if msg.Err != nil {
			m.detailLoading = false
			return m, m.raiseError(msg.Err)
		}
		// Discard a stale load whose sha no longer matches the current
		// selection — the user has already moved on.
		if len(m.commits) == 0 || msg.SHA != m.commits[m.selectedIdx].SHA {
			return m, nil
		}
		m.clearError()
		m.detail = msg.Detail
		m.detailSHA = msg.SHA
		m.detailLoading = false
		// Detail brings parent SHAs, which the binary-size lookup needs.
		// If the current selection is a binary file that was waiting on
		// detail, fire the lookup now.
		if cmd := m.maybeStartBinarySize(); cmd != nil {
			return m, tea.Batch(cmd, m.startSpinnerCmd())
		}
		return m, nil

	case loader.NumStatResult:
		if errors.Is(msg.Err, context.Canceled) {
			return m, nil
		}
		if msg.Err != nil {
			m.filesLoading = false
			return m, m.raiseError(msg.Err)
		}
		if len(m.commits) == 0 || msg.SHA != m.commits[m.selectedIdx].SHA {
			return m, nil
		}
		m.clearError()
		m.files = msg.Files
		m.filesSHA = msg.SHA
		m.filesSelectedIdx = 0
		m.filesViewportTop = 0
		m.filesLoading = false
		m.fileSearchActive = false
		m.fileSearchQuery = ""
		m.fileSearchMatches = nil
		m.fileSearchMatchIdx = -1
		m.fileSearchOriginIdx = 0
		m.fileSearchOriginTop = 0
		// Restore a refresh-preserved file selection by path if it
		// still exists in the new files list. Consumed once; subsequent
		// numstat results (e.g. after navigating to another commit)
		// behave normally.
		if m.pendingRefreshPath != "" {
			for i, f := range m.files {
				if f.Path == m.pendingRefreshPath {
					m.filesSelectedIdx = i
					m.clampFilesViewport()
					break
				}
			}
			m.pendingRefreshPath = ""
		}
		return m, m.startDiffForSelection()

	case loader.DiffResult:
		if errors.Is(msg.Err, context.Canceled) {
			return m, nil
		}
		if msg.Err != nil {
			m.diffLoading = false
			return m, m.raiseError(msg.Err)
		}
		// Discard stale loads whose (sha, path) no longer matches the
		// current selection.
		sha, path, ok := m.currentSelection()
		if !ok || msg.SHA != sha || msg.Path != path {
			return m, nil
		}
		m.clearError()
		m.diff = diffrender.Parse(msg.Raw, msg.Path)
		m.diffSHA = msg.SHA
		m.diffPath = msg.Path
		m.diffScroll = 0
		m.diffHScroll = 0
		m.diffLoading = false
		return m, nil

	case binarySizeMsg:
		// Discard stale loads whose (sha, path) no longer matches the
		// current selection (the user moved on while sizes were in
		// flight).
		sha, path, ok := m.currentSelection()
		if !ok || msg.sha != sha || msg.path != path {
			return m, nil
		}
		m.binOldSize = msg.oldSize
		m.binNewSize = msg.newSize
		m.binHasOld = msg.hasOld
		m.binHasNew = msg.hasNew
		m.binSizeSHA = msg.sha
		m.binSizePath = msg.path
		m.binSizeLoading = false
		return m, nil

	case spinnerTickMsg:
		m.spinnerScheduled = false
		m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
		if m.anyLoading() {
			return m, m.startSpinnerCmd()
		}
		return m, nil

	case errClearMsg:
		// Drop only the error this tick was scheduled for; a newer
		// error raised in the meantime keeps its own countdown.
		if !msg.at.IsZero() && msg.at.Equal(m.errMsgTime) {
			m.clearError()
		}
		return m, nil

	case statusClearMsg:
		if !msg.at.IsZero() && msg.at.Equal(m.statusMsgTime) {
			m.clearStatus()
		}
		return m, nil

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		keyStr := msg.String()
		// When the terminal is below the minimum size, suppress all input
		// except `q` / `ctrl+c` so a transient too-small interval can't
		// churn selection / scroll state. The normal four-panel UI
		// resumes from its prior state as soon as the terminal is resized
		// back above the threshold.
		if layout.Compute(m.w, m.h).TooSmall {
			switch keyStr {
			case "q", "ctrl+c":
				return m, tea.Quit
			}
			return m, nil
		}
		// The help overlay owns all input while open. Any other key
		// routing only kicks in when the overlay is closed.
		if m.helpModalOpen {
			return m.updateHelpModal(keyStr)
		}
		// The worktree picker modal owns all input while open. Any other
		// key routing only kicks in when the modal is closed.
		if m.wtModalOpen {
			return m.updateWorktreeModal(keyStr)
		}
		// The commit-search prompt likewise consumes every keystroke
		// while it is open. `n`/`N` are handled below (after confirmation)
		// alongside the rest of the navigation keys.
		if m.searchActive {
			return m.updateSearchPrompt(msg)
		}
		if m.fileSearchActive {
			return m.updateFileSearchPrompt(msg)
		}
		// Any key other than a follow-up `g` cancels a pending `gg`.
		pendingG := m.pendingG
		if keyStr != "g" {
			m.pendingG = false
		}
		switch keyStr {
		case "ctrl+c":
			return m, tea.Quit
		case "?":
			m.helpModalOpen = true
			m.pendingG = false
			return m, nil
		case "w":
			return m.openWorktreeModal()
		case "ctrl+r":
			return m.refresh()
		case "/":
			if m.active == sectionTop {
				m.openSearchPrompt()
				return m, nil
			}
			if m.active == sectionBottom {
				m.openFileSearchPrompt()
				return m, nil
			}
		case "n":
			if m.active == sectionTop && len(m.searchMatches) > 0 {
				m.cycleSearchMatch(1)
				return m, m.onSelectionChanged()
			}
			if m.active == sectionBottom && len(m.fileSearchMatches) > 0 {
				m.cycleFileSearchMatch(1)
				return m, m.onFileSelectionChanged()
			}
		case "N":
			if m.active == sectionTop && len(m.searchMatches) > 0 {
				m.cycleSearchMatch(-1)
				return m, m.onSelectionChanged()
			}
			if m.active == sectionBottom && len(m.fileSearchMatches) > 0 {
				m.cycleFileSearchMatch(-1)
				return m, m.onFileSelectionChanged()
			}
		case "q", "esc":
			// In small mode, the first "back" step un-swaps the active
			// section from its right panel to its left panel before
			// falling through to the usual bottom→top→quit ladder.
			if layout.Compute(m.w, m.h).SmallMode {
				if m.active == sectionTop && m.topShowRight {
					m.topShowRight = false
					return m, nil
				}
				if m.active == sectionBottom && m.bottomShowRight {
					m.bottomShowRight = false
					return m, nil
				}
			}
			// "Back" semantics: bottom → top; top → quit.
			if m.active == sectionBottom {
				m.active = sectionTop
				return m, nil
			}
			return m, tea.Quit
		case "enter":
			// In small mode `enter` swaps the active section to its
			// right panel; outside small mode it activates the bottom
			// (files) section from the top.
			if layout.Compute(m.w, m.h).SmallMode {
				if m.active == sectionTop {
					m.topShowRight = true
				} else {
					m.bottomShowRight = true
				}
				return m, nil
			}
			if m.active == sectionTop {
				m.active = sectionBottom
				return m, nil
			}
		case "tab":
			if m.active == sectionTop {
				m.active = sectionBottom
			} else {
				m.active = sectionTop
			}
			return m, nil
		case "ctrl+j":
			if m.active == sectionTop && len(m.commits) > 0 {
				if m.selectedIdx < len(m.commits)-1 {
					m.selectedIdx++
				}
				m.clampViewport()
				return m, m.onSelectionChanged()
			}
			if m.active == sectionBottom && len(m.files) > 0 {
				if m.filesSelectedIdx < len(m.files)-1 {
					m.filesSelectedIdx++
				}
				m.clampFilesViewport()
				return m, m.onFileSelectionChanged()
			}
		case "ctrl+k":
			if m.active == sectionTop && len(m.commits) > 0 {
				if m.selectedIdx > 0 {
					m.selectedIdx--
				}
				m.clampViewport()
				return m, m.onSelectionChanged()
			}
			if m.active == sectionBottom && len(m.files) > 0 {
				if m.filesSelectedIdx > 0 {
					m.filesSelectedIdx--
				}
				m.clampFilesViewport()
				return m, m.onFileSelectionChanged()
			}
		case "j":
			if m.active == sectionTop {
				m.scrollMessage(1)
				return m, nil
			}
			if m.active == sectionBottom {
				m.scrollDiff(1)
				return m, nil
			}
		case "k":
			if m.active == sectionTop {
				m.scrollMessage(-1)
				return m, nil
			}
			if m.active == sectionBottom {
				m.scrollDiff(-1)
				return m, nil
			}
		case "d":
			if m.active == sectionTop {
				m.pageMessage(1)
				return m, nil
			}
			if m.active == sectionBottom {
				m.pageDiff(1)
				return m, nil
			}
		case "u":
			if m.active == sectionTop {
				m.pageMessage(-1)
				return m, nil
			}
			if m.active == sectionBottom {
				m.pageDiff(-1)
				return m, nil
			}
		case "G":
			if m.active == sectionTop {
				m.jumpMessageBottom()
				return m, nil
			}
			if m.active == sectionBottom {
				m.jumpDiffBottom()
				return m, nil
			}
		case "g":
			if m.active == sectionTop {
				if pendingG {
					m.msgScroll = 0
					return m, nil
				}
				m.pendingG = true
				return m, nil
			}
			if m.active == sectionBottom {
				if pendingG {
					m.diffScroll = 0
					return m, nil
				}
				m.pendingG = true
				return m, nil
			}
		case "h":
			if m.active == sectionBottom {
				if m.diffHScroll > 0 {
					m.diffHScroll--
				}
				return m, nil
			}
		case "l":
			if m.active == sectionBottom {
				m.diffHScroll++
				return m, nil
			}
		case "ctrl+d":
			if m.active == sectionTop {
				m.pageMessage(1)
				return m, nil
			}
			if m.active == sectionBottom {
				m.jumpDiffNextHunk()
				return m, nil
			}
		case "ctrl+u":
			if m.active == sectionTop {
				m.pageMessage(-1)
				return m, nil
			}
			if m.active == sectionBottom {
				m.jumpDiffPrevHunk()
				return m, nil
			}
		}
	}
	return m, nil
}

// openSearchPrompt opens the commit-list fuzzy search prompt. The
// selection at this moment is captured so `esc` can restore it.
func (m *model) openSearchPrompt() {
	m.searchActive = true
	m.searchQuery = ""
	m.searchMatches = nil
	m.searchMatchIdx = -1
	m.searchOriginIdx = m.selectedIdx
	m.searchOriginTop = m.viewportTop
	m.pendingG = false
}

// recomputeSearchMatches rebuilds searchMatches against the currently
// loaded commits using the current searchQuery.
func (m *model) recomputeSearchMatches() {
	if m.searchQuery == "" || len(m.commits) == 0 {
		m.searchMatches = nil
		return
	}
	items := make([]string, len(m.commits))
	for i, c := range m.commits {
		items[i] = c.ShortSHA + " " + c.Subject + " " + c.Author
	}
	ms := searchfilter.Rank(m.searchQuery, items)
	out := make([]int, len(ms))
	for i, r := range ms {
		out[i] = r.Index
	}
	m.searchMatches = out
}

// updateSearchPrompt handles every keypress while the search prompt is
// open: `esc` cancels (restores prior selection), `enter` confirms
// (moves selection to the best match), backspace edits, printable runes
// extend the query, and any other key is absorbed so it doesn't leak to
// the rest of the panel.
func (m model) updateSearchPrompt(km tea.KeyMsg) (tea.Model, tea.Cmd) {
	keyStr := km.String()
	switch keyStr {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.searchActive = false
		m.searchQuery = ""
		m.searchMatches = nil
		m.searchMatchIdx = -1
		m.selectedIdx = m.searchOriginIdx
		m.viewportTop = m.searchOriginTop
		m.clampViewport()
		return m, nil
	case "enter":
		m.searchActive = false
		if len(m.searchMatches) == 0 {
			return m, nil
		}
		m.searchMatchIdx = 0
		m.selectedIdx = m.searchMatches[0]
		m.clampViewport()
		return m, m.onSelectionChanged()
	case "backspace", "ctrl+h":
		if len(m.searchQuery) > 0 {
			r := []rune(m.searchQuery)
			m.searchQuery = string(r[:len(r)-1])
			m.recomputeSearchMatches()
		}
		return m, nil
	}
	// Treat any printable rune (including space) as input. Bubble Tea
	// reports printable runes with msg.Type == tea.KeyRunes; everything
	// else (arrow keys, function keys, …) is absorbed silently.
	if km.Type == tea.KeyRunes || km.Type == tea.KeySpace {
		var r []rune
		if km.Type == tea.KeySpace {
			r = []rune{' '}
		} else {
			r = km.Runes
		}
		m.searchQuery += string(r)
		m.recomputeSearchMatches()
	}
	return m, nil
}

// cycleSearchMatch advances searchMatchIdx by delta (with wrap-around)
// and updates selectedIdx to the new match.
func (m *model) cycleSearchMatch(delta int) {
	n := len(m.searchMatches)
	if n == 0 {
		return
	}
	if m.searchMatchIdx < 0 {
		m.searchMatchIdx = 0
	} else {
		m.searchMatchIdx = ((m.searchMatchIdx+delta)%n + n) % n
	}
	m.selectedIdx = m.searchMatches[m.searchMatchIdx]
	m.clampViewport()
}

// isSearchMatch reports whether commit row `idx` is in the current
// match set. O(n) over a typically tiny match slice — fine for v1.
func (m *model) isSearchMatch(idx int) bool {
	for _, i := range m.searchMatches {
		if i == idx {
			return true
		}
	}
	return false
}

// openFileSearchPrompt opens the files-list fuzzy search prompt. The
// file selection at this moment is captured so `esc` can restore it.
func (m *model) openFileSearchPrompt() {
	m.fileSearchActive = true
	m.fileSearchQuery = ""
	m.fileSearchMatches = nil
	m.fileSearchMatchIdx = -1
	m.fileSearchOriginIdx = m.filesSelectedIdx
	m.fileSearchOriginTop = m.filesViewportTop
	m.pendingG = false
}

// recomputeFileSearchMatches rebuilds fileSearchMatches against the
// currently-loaded files using the current fileSearchQuery. Renames
// match against both the old and new paths so a search for the prior
// name still finds the row.
func (m *model) recomputeFileSearchMatches() {
	if m.fileSearchQuery == "" || len(m.files) == 0 {
		m.fileSearchMatches = nil
		return
	}
	items := make([]string, len(m.files))
	for i, f := range m.files {
		if f.OldPath != "" {
			items[i] = f.OldPath + " " + f.Path
		} else {
			items[i] = f.Path
		}
	}
	ms := searchfilter.Rank(m.fileSearchQuery, items)
	out := make([]int, len(ms))
	for i, r := range ms {
		out[i] = r.Index
	}
	m.fileSearchMatches = out
}

// updateFileSearchPrompt handles every keypress while the files-search
// prompt is open. Behavior mirrors updateSearchPrompt but is scoped to
// the file list.
func (m model) updateFileSearchPrompt(km tea.KeyMsg) (tea.Model, tea.Cmd) {
	keyStr := km.String()
	switch keyStr {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.fileSearchActive = false
		m.fileSearchQuery = ""
		m.fileSearchMatches = nil
		m.fileSearchMatchIdx = -1
		m.filesSelectedIdx = m.fileSearchOriginIdx
		m.filesViewportTop = m.fileSearchOriginTop
		m.clampFilesViewport()
		return m, nil
	case "enter":
		m.fileSearchActive = false
		if len(m.fileSearchMatches) == 0 {
			return m, nil
		}
		m.fileSearchMatchIdx = 0
		m.filesSelectedIdx = m.fileSearchMatches[0]
		m.clampFilesViewport()
		return m, m.onFileSelectionChanged()
	case "backspace", "ctrl+h":
		if len(m.fileSearchQuery) > 0 {
			r := []rune(m.fileSearchQuery)
			m.fileSearchQuery = string(r[:len(r)-1])
			m.recomputeFileSearchMatches()
		}
		return m, nil
	}
	if km.Type == tea.KeyRunes || km.Type == tea.KeySpace {
		var r []rune
		if km.Type == tea.KeySpace {
			r = []rune{' '}
		} else {
			r = km.Runes
		}
		m.fileSearchQuery += string(r)
		m.recomputeFileSearchMatches()
	}
	return m, nil
}

// cycleFileSearchMatch advances fileSearchMatchIdx by delta (with
// wrap-around) and updates filesSelectedIdx to the new match.
func (m *model) cycleFileSearchMatch(delta int) {
	n := len(m.fileSearchMatches)
	if n == 0 {
		return
	}
	if m.fileSearchMatchIdx < 0 {
		m.fileSearchMatchIdx = 0
	} else {
		m.fileSearchMatchIdx = ((m.fileSearchMatchIdx+delta)%n + n) % n
	}
	m.filesSelectedIdx = m.fileSearchMatches[m.fileSearchMatchIdx]
	m.clampFilesViewport()
}

// isFileSearchMatch reports whether file row `idx` is in the current
// match set.
func (m *model) isFileSearchMatch(idx int) bool {
	for _, i := range m.fileSearchMatches {
		if i == idx {
			return true
		}
	}
	return false
}

// updateHelpModal handles keys while the help overlay is open: `?`,
// `esc`, and `q` close the overlay; `ctrl+c` quits; anything else is
// absorbed so it doesn't leak to the panels behind the overlay.
func (m model) updateHelpModal(keyStr string) (tea.Model, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return m, tea.Quit
	case "?", "esc", "q":
		m.helpModalOpen = false
		return m, nil
	}
	return m, nil
}

// openWorktreeModal enumerates worktrees and opens the picker modal.
// Enumeration runs synchronously since `git worktree list` is fast and
// the user is actively waiting on a response to their keypress. A
// failure to enumerate surfaces in the status row and leaves the modal
// closed.
func (m model) openWorktreeModal() (tea.Model, tea.Cmd) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wts, err := m.git.Worktrees(ctx)
	if err != nil {
		return m, m.raiseError(err)
	}
	m.wtList = wts
	m.wtModalIdx = 0
	for i, w := range wts {
		if w.Current {
			m.wtModalIdx = i
			break
		}
	}
	m.wtModalOpen = true
	return m, nil
}

// updateWorktreeModal handles the keys consumed while the worktree
// picker is open: `j`/`k`/`ctrl+j`/`ctrl+k` move the highlight, `enter`
// selects (switching worktrees when the selection isn't already
// current), `esc`/`q` cancel without changing state, and `ctrl+c`
// quits the program. All other keys are absorbed.
func (m model) updateWorktreeModal(keyStr string) (tea.Model, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.wtModalOpen = false
		return m, nil
	case "j", "ctrl+j":
		if m.wtModalIdx < len(m.wtList)-1 {
			m.wtModalIdx++
		}
		return m, nil
	case "k", "ctrl+k":
		if m.wtModalIdx > 0 {
			m.wtModalIdx--
		}
		return m, nil
	case "enter":
		if m.wtModalIdx >= len(m.wtList) {
			m.wtModalOpen = false
			return m, nil
		}
		sel := m.wtList[m.wtModalIdx]
		m.wtModalOpen = false
		if sel.Current || sel.Path == "" {
			return m, nil
		}
		return m.switchWorktree(sel.Path)
	}
	return m, nil
}

// switchWorktree reconstructs the gitcmd Client at the new worktree
// path, rebuilds the loader (which invalidates every cache), resets
// every panel's selection and scroll state, and kicks off a fresh log
// load. No `chdir` — only the `git -C <path>` scope changes.
func (m model) switchWorktree(path string) (tea.Model, tea.Cmd) {
	// Cancel any in-flight loads on the old loader so their goroutines
	// unblock and don't deliver stale results into the new model state.
	if m.ldr != nil {
		m.ldr.CancelDetail()
		m.ldr.CancelNumStat()
		m.ldr.CancelDiff()
	}

	newGit := gitcmd.New(path)
	m.git = newGit
	m.ldr = loader.New(loader.Config{Source: newGit})

	m.commits = nil
	m.selectedIdx = 0
	m.viewportTop = 0
	m.loadedAll = false
	m.loadingMore = false

	m.detail = gitcmd.CommitDetail{}
	m.detailSHA = ""
	m.msgScroll = 0
	m.pendingG = false

	m.files = nil
	m.filesSHA = ""
	m.filesSelectedIdx = 0
	m.filesViewportTop = 0

	m.diff = diffrender.Result{}
	m.diffSHA = ""
	m.diffPath = ""
	m.diffScroll = 0
	m.diffHScroll = 0

	m.detailLoading = false
	m.filesLoading = false
	m.diffLoading = false
	m.resetBinarySize()

	m.active = sectionTop
	m.clearError()
	m.clearStatus()
	m.pendingRefreshSHA = ""
	m.pendingRefreshPath = ""

	m.searchActive = false
	m.searchQuery = ""
	m.searchMatches = nil
	m.searchMatchIdx = -1
	m.searchOriginIdx = 0
	m.searchOriginTop = 0

	m.fileSearchActive = false
	m.fileSearchQuery = ""
	m.fileSearchMatches = nil
	m.fileSearchMatchIdx = -1
	m.fileSearchOriginIdx = 0
	m.fileSearchOriginTop = 0

	m.topShowRight = false
	m.bottomShowRight = false

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	branch, label := resolveWorktreeLabel(ctx, newGit)
	m.branch = branch
	m.worktreeLabel = label

	return m, m.loadLogCmd(0)
}

// refresh rebuilds the loader (which invalidates every cache), cancels
// any in-flight loads, resets every panel's transient state, and kicks
// off a fresh log load. The currently-selected commit (by sha) and file
// (by path) are recorded on the model so the next logLoadedMsg /
// NumStatResult can restore the selection if the commit/file still
// exists; otherwise selection falls back to the top of the new list.
func (m model) refresh() (tea.Model, tea.Cmd) {
	if m.ldr != nil {
		m.ldr.CancelDetail()
		m.ldr.CancelNumStat()
		m.ldr.CancelDiff()
	}

	if m.selectedIdx >= 0 && m.selectedIdx < len(m.commits) {
		m.pendingRefreshSHA = m.commits[m.selectedIdx].SHA
	} else {
		m.pendingRefreshSHA = ""
	}
	if m.filesSelectedIdx >= 0 && m.filesSelectedIdx < len(m.files) {
		m.pendingRefreshPath = m.files[m.filesSelectedIdx].Path
	} else {
		m.pendingRefreshPath = ""
	}

	m.ldr = loader.New(loader.Config{Source: m.git})

	m.commits = nil
	m.selectedIdx = 0
	m.viewportTop = 0
	m.loadedAll = false
	m.loadingMore = false

	m.detail = gitcmd.CommitDetail{}
	m.detailSHA = ""
	m.msgScroll = 0
	m.pendingG = false

	m.files = nil
	m.filesSHA = ""
	m.filesSelectedIdx = 0
	m.filesViewportTop = 0

	m.diff = diffrender.Result{}
	m.diffSHA = ""
	m.diffPath = ""
	m.diffScroll = 0
	m.diffHScroll = 0

	m.detailLoading = false
	m.filesLoading = false
	m.diffLoading = false
	m.resetBinarySize()

	m.searchActive = false
	m.searchQuery = ""
	m.searchMatches = nil
	m.searchMatchIdx = -1
	m.searchOriginIdx = 0
	m.searchOriginTop = 0

	m.fileSearchActive = false
	m.fileSearchQuery = ""
	m.fileSearchMatches = nil
	m.fileSearchMatchIdx = -1
	m.fileSearchOriginIdx = 0
	m.fileSearchOriginTop = 0

	m.clearError()
	statusCmd := m.raiseStatus("Refreshed")

	return m, tea.Batch(m.loadLogCmd(0), statusCmd)
}

// onSelectionChanged is called after the commit selection moves: it
// resets the message scroll, flags the right/bottom panels as loading
// (so the spinner appears and existing content renders dimmed), and
// returns a tea.Cmd batching the fresh detail + numstat fetches with
// any needed lazy log page fetch. Prior content is intentionally NOT
// cleared so the user keeps something to look at until the new content
// arrives.
func (m *model) onSelectionChanged() tea.Cmd {
	m.msgScroll = 0
	m.detailLoading = true
	m.filesLoading = true
	// Cancel any in-flight diff load; we won't know the new file path
	// until numstat returns.
	m.ldr.CancelDiff()
	m.diffLoading = true
	cmds := []tea.Cmd{}
	if more := m.maybeLoadMoreCmd(); more != nil {
		cmds = append(cmds, more)
	}
	if len(m.commits) > 0 {
		sha := m.commits[m.selectedIdx].SHA
		cmds = append(cmds, m.ldr.LoadDetail(sha), m.ldr.LoadNumStat(sha))
	}
	if c := m.startSpinnerCmd(); c != nil {
		cmds = append(cmds, c)
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// onFileSelectionChanged is called after the file selection moves
// within the same commit: it resets the diff scroll and kicks off a
// fresh Diff fetch for the new (sha, path) pair. Prior diff content is
// kept on screen (dimmed) until the new diff lands.
func (m *model) onFileSelectionChanged() tea.Cmd {
	cmd := m.startDiffForSelection()
	if spin := m.startSpinnerCmd(); spin != nil {
		return tea.Batch(cmd, spin)
	}
	return cmd
}

// startDiffForSelection returns a tea.Cmd that fetches the diff for the
// currently-selected (commit, file). Returns nil when there is no
// current selection. For binary files, the text-diff fetch is replaced
// by a pair of byte-size lookups (see maybeStartBinarySize).
func (m *model) startDiffForSelection() tea.Cmd {
	sha, path, ok := m.currentSelection()
	if !ok {
		m.diffLoading = false
		return nil
	}
	if m.filesSelectedIdx < len(m.files) && m.files[m.filesSelectedIdx].IsBinary {
		// Binary files don't trigger a text-diff fetch; clear any prior
		// diff content so the binary-delta placeholder renders cleanly.
		m.ldr.CancelDiff()
		m.diff = diffrender.Result{}
		m.diffSHA = ""
		m.diffPath = ""
		m.diffScroll = 0
		m.diffHScroll = 0
		m.diffLoading = false
		return m.maybeStartBinarySize()
	}
	// Selection moved off a binary file — drop any stale binary state so
	// the next binary visit reloads cleanly.
	m.resetBinarySize()
	m.diffLoading = true
	return m.ldr.LoadDiff(sha, path)
}

// resetBinarySize clears the binary-size state. Called when the diff
// panel transitions away from a binary file.
func (m *model) resetBinarySize() {
	m.binOldSize = 0
	m.binNewSize = 0
	m.binHasOld = false
	m.binHasNew = false
	m.binSizeSHA = ""
	m.binSizePath = ""
	m.binSizeLoading = false
}

// maybeStartBinarySize returns a tea.Cmd to fetch a binary file's
// byte-size delta when the current selection is a binary file and the
// commit's parent SHA is known. Returns nil when there is no selection,
// the selection is not binary, the sizes for this exact (sha, path) are
// already loaded, or detail (and therefore parents) hasn't arrived yet.
// In the last case the DetailResult handler retries this call so the
// load fires as soon as parents become known.
func (m *model) maybeStartBinarySize() tea.Cmd {
	sha, path, ok := m.currentSelection()
	if !ok {
		return nil
	}
	f := m.files[m.filesSelectedIdx]
	if !f.IsBinary {
		return nil
	}
	if m.binSizeSHA == sha && m.binSizePath == path {
		return nil
	}
	if m.detail.SHA != sha {
		// Detail not loaded yet (or it's for a stale commit). The detail
		// handler will re-enter this function once detail arrives.
		return nil
	}
	parent := ""
	if len(m.detail.Parents) > 0 {
		parent = m.detail.Parents[0]
	}
	m.resetBinarySize()
	m.binSizeLoading = true
	return m.loadBinarySizesCmd(sha, parent, f.Path, f.OldPath, f.Status)
}

// anyLoading reports whether any panel is currently waiting on a load.
func (m *model) anyLoading() bool {
	return m.detailLoading || m.filesLoading || m.diffLoading || m.binSizeLoading
}

// startSpinnerCmd returns a tea.Cmd that schedules the next spinner
// tick, or nil if a tick is already scheduled. Idempotent.
func (m *model) startSpinnerCmd() tea.Cmd {
	if m.spinnerScheduled {
		return nil
	}
	if !m.anyLoading() {
		return nil
	}
	m.spinnerScheduled = true
	return tea.Tick(spinnerInterval, func(_ time.Time) tea.Msg {
		return spinnerTickMsg{}
	})
}

// currentSelection returns the (sha, path) of the currently-selected
// commit + file, plus ok=true. ok is false when either selection is
// missing.
func (m *model) currentSelection() (sha, path string, ok bool) {
	if len(m.commits) == 0 || len(m.files) == 0 {
		return "", "", false
	}
	if m.filesSelectedIdx >= len(m.files) {
		return "", "", false
	}
	return m.commits[m.selectedIdx].SHA, m.files[m.filesSelectedIdx].Path, true
}

// handleMouse routes a MouseMsg. v1 acts on two kinds of mouse input:
// (1) wheel-up / wheel-down over a right panel (message or diff)
// scrolls that panel without changing the active section; (2) a
// left-button press on a row in a left panel (log or files) selects
// that row and activates the corresponding section, firing the same
// downstream live-update chain as a keyboard move. Clicks on right
// panels are inert. Mouse events are suppressed entirely when the
// terminal is too small or any modal / search prompt is open so they
// can't bypass those gates.
func (m model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	lo := layout.Compute(m.w, m.h)
	if lo.TooSmall {
		return m, nil
	}
	if m.helpModalOpen || m.wtModalOpen || m.searchActive || m.fileSearchActive {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		delta := 1
		if msg.Button == tea.MouseButtonWheelUp {
			delta = -1
		}
		msgRect, msgVisible := messagePanelRect(lo, m.topShowRight)
		diffRect, diffVisible := diffPanelRect(lo, m.bottomShowRight)
		if msgVisible && rectContains(msgRect, msg.X, msg.Y) {
			m.scrollMessage(delta)
			return m, nil
		}
		if diffVisible && rectContains(diffRect, msg.X, msg.Y) {
			m.scrollDiff(delta)
			return m, nil
		}
		return m, nil
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		logRect, logVisible := logPanelRect(lo, m.topShowRight)
		if logVisible && rectContains(logRect, msg.X, msg.Y) {
			row := msg.Y - logRect.Y - 1 // -1 for the title row
			if row < 0 {
				return m, nil
			}
			idx := m.viewportTop + row
			if idx < 0 || idx >= len(m.commits) {
				return m, nil
			}
			sectionChanged := m.active != sectionTop
			selectionChanged := idx != m.selectedIdx
			m.active = sectionTop
			m.selectedIdx = idx
			m.clampViewport()
			if selectionChanged || sectionChanged {
				return m, m.onSelectionChanged()
			}
			return m, nil
		}
		filesRect, filesVisible := filesPanelRect(lo, m.bottomShowRight)
		if filesVisible && rectContains(filesRect, msg.X, msg.Y) {
			row := msg.Y - filesRect.Y - 1
			if row < 0 {
				return m, nil
			}
			idx := m.filesViewportTop + row
			if idx < 0 || idx >= len(m.files) {
				return m, nil
			}
			selectionChanged := idx != m.filesSelectedIdx
			m.active = sectionBottom
			m.filesSelectedIdx = idx
			m.clampFilesViewport()
			if selectionChanged {
				return m, m.onFileSelectionChanged()
			}
			return m, nil
		}
		return m, nil
	}
	return m, nil
}

// messagePanelRect returns the rectangle the message panel currently
// occupies and whether it is visible. In small mode the message panel
// is only on screen when the top section has been swapped to its right
// view; otherwise it occupies TopRight in full mode.
func messagePanelRect(lo layout.Layout, topShowRight bool) (layout.Rect, bool) {
	if lo.SmallMode {
		if topShowRight {
			return lo.TopLeft, true
		}
		return layout.Rect{}, false
	}
	return lo.TopRight, true
}

// diffPanelRect returns the rectangle the diff panel currently occupies
// and whether it is visible. Mirrors messagePanelRect for the bottom
// section.
func diffPanelRect(lo layout.Layout, bottomShowRight bool) (layout.Rect, bool) {
	if lo.SmallMode {
		if bottomShowRight {
			return lo.BottomLeft, true
		}
		return layout.Rect{}, false
	}
	return lo.BottomRight, true
}

// logPanelRect returns the rectangle the log panel currently occupies
// and whether it is visible. In full mode it is always TopLeft. In
// small mode the same rectangle is shared with the message panel, so
// the log is on screen only when the top section has not been swapped
// to its right view.
func logPanelRect(lo layout.Layout, topShowRight bool) (layout.Rect, bool) {
	if lo.SmallMode && topShowRight {
		return layout.Rect{}, false
	}
	return lo.TopLeft, true
}

// filesPanelRect mirrors logPanelRect for the bottom section.
func filesPanelRect(lo layout.Layout, bottomShowRight bool) (layout.Rect, bool) {
	if lo.SmallMode && bottomShowRight {
		return layout.Rect{}, false
	}
	return lo.BottomLeft, true
}

// rectContains reports whether (x, y) falls inside r (half-open on the
// far edge: x ∈ [r.X, r.X+r.W), y ∈ [r.Y, r.Y+r.H)).
func rectContains(r layout.Rect, x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// scrollDiff moves the diff panel viewport by `delta` lines.
func (m *model) scrollDiff(delta int) {
	m.diffScroll += delta
	m.clampDiffScroll()
}

// pageDiff scrolls the diff panel by `delta` page-heights (one page is
// the diff panel's visible body height).
func (m *model) pageDiff(delta int) {
	page := m.diffPanelBodyHeight()
	if page <= 0 {
		page = 1
	}
	m.scrollDiff(delta * page)
}

// jumpDiffBottom scrolls so the final diff line sits at the bottom of
// the visible body.
func (m *model) jumpDiffBottom() {
	m.diffScroll = len(m.diff.Lines)
	m.clampDiffScroll()
}

// jumpDiffNextHunk scrolls so the next hunk start (strictly after the
// current top line) becomes the top of the viewport. No-op when there
// is no later hunk.
func (m *model) jumpDiffNextHunk() {
	for _, h := range m.diff.HunkStarts {
		if h > m.diffScroll {
			m.diffScroll = h
			m.clampDiffScroll()
			return
		}
	}
}

// jumpDiffPrevHunk scrolls so the previous hunk start (strictly before
// the current top line) becomes the top of the viewport. No-op when
// there is no earlier hunk.
func (m *model) jumpDiffPrevHunk() {
	target := -1
	for _, h := range m.diff.HunkStarts {
		if h < m.diffScroll {
			target = h
			continue
		}
		break
	}
	if target < 0 {
		return
	}
	m.diffScroll = target
	m.clampDiffScroll()
}

// clampDiffScroll constrains diffScroll to [0, max(0, len(Lines)-bodyH)].
func (m *model) clampDiffScroll() {
	if m.diffScroll < 0 {
		m.diffScroll = 0
	}
	bodyH := m.diffPanelBodyHeight()
	max := len(m.diff.Lines) - bodyH
	if max < 0 {
		max = 0
	}
	if m.diffScroll > max {
		m.diffScroll = max
	}
}

// diffPanelBodyHeight returns the visible body height (excluding the
// title row) of the diff panel for the current terminal size. In
// small-mode the diff panel only occupies the bottom rectangle when
// the user has swapped that section to its right view; otherwise the
// panel isn't rendered and the body height is zero.
func (m *model) diffPanelBodyHeight() int {
	if m.w == 0 || m.h == 0 {
		return 0
	}
	lo := layout.Compute(m.w, m.h)
	if lo.TooSmall {
		return 0
	}
	if lo.SmallMode {
		if m.bottomShowRight {
			return lo.BottomLeft.H - 1
		}
		return 0
	}
	return lo.BottomRight.H - 1
}

// scrollMessage moves the message-panel viewport by `delta` lines,
// clamped to [0, maxMsgScroll]. The renderer also clamps for safety.
func (m *model) scrollMessage(delta int) {
	m.msgScroll += delta
	m.clampMsgScroll()
}

// pageMessage scrolls the message panel by `delta` page-heights, where
// one page is the message panel's visible body height. Used by `d`/`u`.
func (m *model) pageMessage(delta int) {
	page := m.msgPanelBodyHeight()
	if page <= 0 {
		page = 1
	}
	m.scrollMessage(delta * page)
}

// jumpMessageBottom scrolls the message panel so the last line of the
// body is visible at the bottom of the panel.
func (m *model) jumpMessageBottom() {
	m.msgScroll = messageLineCount(m.detail)
	m.clampMsgScroll()
}

// clampMsgScroll constrains msgScroll to [0, maxMsgScroll] where the
// max keeps the final body line on screen rather than scrolling past.
func (m *model) clampMsgScroll() {
	if m.msgScroll < 0 {
		m.msgScroll = 0
	}
	bodyH := m.msgPanelBodyHeight()
	max := messageLineCount(m.detail) - bodyH
	if max < 0 {
		max = 0
	}
	if m.msgScroll > max {
		m.msgScroll = max
	}
}

// msgPanelBodyHeight returns the visible body height (excluding the
// title row) of the message panel for the current terminal size. In
// small-mode the message panel only occupies the top rectangle when
// the user has swapped that section to its right view; otherwise the
// panel isn't rendered and the body height is zero.
func (m *model) msgPanelBodyHeight() int {
	if m.w == 0 || m.h == 0 {
		return 0
	}
	lo := layout.Compute(m.w, m.h)
	if lo.TooSmall {
		return 0
	}
	if lo.SmallMode {
		if m.topShowRight {
			return lo.TopLeft.H - 1
		}
		return 0
	}
	return lo.TopRight.H - 1
}

// maybeLoadMoreCmd returns a Log command if the selection is close
// enough to the end of the loaded commits to warrant pre-fetching.
func (m *model) maybeLoadMoreCmd() tea.Cmd {
	if m.loadedAll || m.loadingMore {
		return nil
	}
	if len(m.commits)-m.selectedIdx > loadMoreThreshold {
		return nil
	}
	m.loadingMore = true
	return m.loadLogCmd(len(m.commits))
}

// clampViewport adjusts viewportTop so selectedIdx is visible.
func (m *model) clampViewport() {
	bodyH := m.logBodyHeight()
	if bodyH <= 0 {
		m.viewportTop = 0
		return
	}
	if m.selectedIdx < m.viewportTop {
		m.viewportTop = m.selectedIdx
	}
	if m.selectedIdx >= m.viewportTop+bodyH {
		m.viewportTop = m.selectedIdx - bodyH + 1
	}
	if m.viewportTop < 0 {
		m.viewportTop = 0
	}
	maxTop := len(m.commits) - bodyH
	if maxTop < 0 {
		maxTop = 0
	}
	if m.viewportTop > maxTop {
		m.viewportTop = maxTop
	}
}

func (m *model) logBodyHeight() int {
	if m.w == 0 || m.h == 0 {
		return 0
	}
	lo := layout.Compute(m.w, m.h)
	if lo.TooSmall {
		return 0
	}
	return lo.TopLeft.H - 1 // minus title row
}

// clampFilesViewport adjusts filesViewportTop so filesSelectedIdx stays visible.
func (m *model) clampFilesViewport() {
	bodyH := m.filesBodyHeight()
	if bodyH <= 0 {
		m.filesViewportTop = 0
		return
	}
	if m.filesSelectedIdx < m.filesViewportTop {
		m.filesViewportTop = m.filesSelectedIdx
	}
	if m.filesSelectedIdx >= m.filesViewportTop+bodyH {
		m.filesViewportTop = m.filesSelectedIdx - bodyH + 1
	}
	if m.filesViewportTop < 0 {
		m.filesViewportTop = 0
	}
	maxTop := len(m.files) - bodyH
	if maxTop < 0 {
		maxTop = 0
	}
	if m.filesViewportTop > maxTop {
		m.filesViewportTop = maxTop
	}
}

func (m *model) filesBodyHeight() int {
	if m.w == 0 || m.h == 0 {
		return 0
	}
	lo := layout.Compute(m.w, m.h)
	if lo.TooSmall {
		return 0
	}
	return lo.BottomLeft.H - 1
}

var (
	activeTitleStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("12")).
				Bold(true)
	inactiveTitleStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("244"))

	activeSelectedRowStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("24")).
				Foreground(lipgloss.Color("231")).
				Bold(true)
	inactiveSelectedRowStyle = lipgloss.NewStyle().
					Background(lipgloss.Color("236")).
					Foreground(lipgloss.Color("250"))
	shortSHAStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	relDateStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	authorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("110"))

	// Ref-decoration colors on commit rows. HEAD is the brightest
	// (cyan + bold), local branches are green, remote-tracking
	// branches are red, and the surrounding parens / commas are dim.
	refHEADStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("159")).Bold(true)
	refLocalStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	refRemoteStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	refParenStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	errMsgStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	statusMsgStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))

	spinnerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	staleStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	// searchMatchRowStyle highlights commit rows that match the active
	// or just-confirmed search query (but are not the currently-selected
	// row). The focused match still gets the standard selection style.
	searchMatchRowStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("58")).
				Foreground(lipgloss.Color("230"))
	searchPromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))

	fileStatusStyles = map[string]lipgloss.Style{
		"A": lipgloss.NewStyle().Foreground(lipgloss.Color("114")), // green
		"M": lipgloss.NewStyle().Foreground(lipgloss.Color("214")), // yellow
		"D": lipgloss.NewStyle().Foreground(lipgloss.Color("203")), // red
		"R": lipgloss.NewStyle().Foreground(lipgloss.Color("75")),  // blue
		"C": lipgloss.NewStyle().Foreground(lipgloss.Color("141")), // magenta
		"T": lipgloss.NewStyle().Foreground(lipgloss.Color("110")), // cyan
	}
	filePlusStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	fileMinusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))

	// Worktree picker modal styles.
	modalBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("110")).
				Padding(1, 2)
	modalTitleStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	modalHintStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	modalSelectedStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("24")).
				Foreground(lipgloss.Color("231")).
				Bold(true)
	modalCurrentMarkerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	modalBranchStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	// Help overlay styles.
	helpGroupTitleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("110")).Bold(true)
	helpKeyStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))

	tooSmallHintStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
)

// renderTitleWithSpinner renders the panel title row, optionally
// followed by a small braille spinner glyph when the panel is loading.
func renderTitleWithSpinner(text string, width int, active, loading bool, spinFrame int) string {
	prefix := "  "
	titleStyle := inactiveTitleStyle
	if active {
		prefix = "▸ "
		titleStyle = activeTitleStyle
	}
	rawTitle := prefix + text
	var s string
	if loading {
		spin := string(spinnerFrames[spinFrame%len(spinnerFrames)])
		rawTitle += " " + spin
		s = titleStyle.Render(prefix+text) + " " + spinnerStyle.Render(spin)
	} else {
		s = titleStyle.Render(rawTitle)
	}
	w := lipgloss.Width(s)
	if w > width {
		if width <= 0 {
			return ""
		}
		if len(rawTitle) > width {
			rawTitle = rawTitle[:width]
		}
		return titleStyle.Render(rawTitle)
	}
	if w < width {
		s += strings.Repeat(" ", width-w)
	}
	return s
}

func renderTitle(text string, width int, active bool) string {
	return renderTitleWithSpinner(text, width, active, false, 0)
}

// logPanelTitle builds the top-left panel's title, surfacing the
// current branch and (when more than one worktree exists) a worktree
// label so the reviewer always knows which checkout they're reading.
func logPanelTitle(m model) string {
	const base = "log"
	if m.branch == "" && m.worktreeLabel == "" {
		return base
	}
	title := base
	if m.branch != "" {
		title += " — " + m.branch
	}
	if m.worktreeLabel != "" {
		title += " [" + m.worktreeLabel + "]"
	}
	return title
}

// padOrTruncate pads s with spaces on the right to width cells, or
// truncates with a trailing ellipsis when too long.
func padOrTruncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	w := lipgloss.Width(s)
	if w == width {
		return s
	}
	if w < width {
		return s + strings.Repeat(" ", width-w)
	}
	// Truncate with ellipsis. lipgloss.Width is rune/grapheme-aware but
	// for plain ASCII rows (subjects can contain anything though) a
	// byte-level slice is a reasonable v1 approach. We use the rune-
	// safe trim from the standard library to stay correct on UTF-8.
	if width == 1 {
		return "…"
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

// visibleRefs returns the ref decorations that should appear on a
// commit row — HEAD, local branches, and remote-tracking branches. Tag
// decorations are filtered out (they appear in the message panel
// instead, per the PRD).
func visibleRefs(refs []string) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		if gitcmd.ClassifyRef(r) == gitcmd.RefTag {
			continue
		}
		out = append(out, r)
	}
	return out
}

// nonTagRefs is the same filter as visibleRefs but for the message
// panel's "Refs:" line, which surfaces branches / HEAD / remote-
// tracking refs only. Tag refs render in the dedicated "Tags:" block
// (which also includes annotated-tag messages).
func nonTagRefs(refs []string) []string { return visibleRefs(refs) }

// formatRefs builds a "(ref1, ref2, ...)" decoration block. Returns
// the styled string (with per-kind colors) and the matching plain
// string. Both strings have identical character content so callers can
// rely on the plain string's lipgloss.Width when sizing.
func formatRefs(refs []string) (styled, plain string) {
	if len(refs) == 0 {
		return "", ""
	}
	var sb, pb strings.Builder
	sb.WriteString(refParenStyle.Render("("))
	pb.WriteByte('(')
	for i, r := range refs {
		if i > 0 {
			sb.WriteString(refParenStyle.Render(", "))
			pb.WriteString(", ")
		}
		var style lipgloss.Style
		switch gitcmd.ClassifyRef(r) {
		case gitcmd.RefHEAD:
			style = refHEADStyle
		case gitcmd.RefRemote:
			style = refRemoteStyle
		default:
			style = refLocalStyle
		}
		sb.WriteString(style.Render(r))
		pb.WriteString(r)
	}
	sb.WriteString(refParenStyle.Render(")"))
	pb.WriteByte(')')
	return sb.String(), pb.String()
}

// fitRefs renders as many refs as fit within `max` visible cells,
// dropping refs from the end until the block fits or there are none
// left. The returned plain string's lipgloss.Width is <= max.
func fitRefs(refs []string, max int) (styled, plain string) {
	for n := len(refs); n > 0; n-- {
		s, p := formatRefs(refs[:n])
		if lipgloss.Width(p) <= max {
			return s, p
		}
	}
	return "", ""
}

// commitRowColumns returns the per-column text of a commit row,
// already padded/truncated to fit `width`. The "rest" column packs the
// decoration block (when present) and the subject into the remaining
// space; restStyled carries colors, restPlain is identical character
// content with no escapes (used as the basis for the selection
// highlight).
func commitRowColumns(c gitcmd.Commit, width int) (short, date, author, restStyled, restPlain string, ok bool) {
	const shaW, dateW, authorW = 7, 14, 16
	const gap = "  "
	short = c.ShortSHA
	if len(short) > shaW {
		short = short[:shaW]
	} else if len(short) < shaW {
		short += strings.Repeat(" ", shaW-len(short))
	}
	date = padOrTruncate(c.RelDate, dateW)
	author = padOrTruncate(c.Author, authorW)
	fixedW := shaW + len(gap) + dateW + len(gap) + authorW + len(gap)
	restW := width - fixedW
	if restW < 1 {
		return short, "", "", "", "", false
	}

	refsStyled, refsPlain := fitRefs(visibleRefs(c.Refs), restW)
	rw := lipgloss.Width(refsPlain)
	if rw == 0 {
		subj := padOrTruncate(c.Subject, restW)
		return short, date, author, subj, subj, true
	}
	// One-space separator between refs and subject when both fit; if
	// there isn't room for separator + at least one subject char, pad
	// the refs block out to restW instead.
	if rw+2 > restW {
		pad := restW - rw
		restStyled = refsStyled + strings.Repeat(" ", pad)
		restPlain = refsPlain + strings.Repeat(" ", pad)
		return short, date, author, restStyled, restPlain, true
	}
	subjW := restW - rw - 1
	subj := padOrTruncate(c.Subject, subjW)
	restStyled = refsStyled + " " + subj
	restPlain = refsPlain + " " + subj
	return short, date, author, restStyled, restPlain, true
}

// renderLogRow formats one commit row to exactly `width` cells with
// per-column foreground colors (used for non-selected rows).
func renderLogRow(c gitcmd.Commit, width int) string {
	const gap = "  "
	short, date, author, restStyled, _, ok := commitRowColumns(c, width)
	if !ok {
		return padOrTruncate(short, width)
	}
	return shortSHAStyle.Render(short) + gap +
		relDateStyle.Render(date) + gap +
		authorStyle.Render(author) + gap +
		restStyled
}

// renderLogRowPlain returns the row content as plain text (no color
// escapes), suitable for wrapping in a row-level highlight style.
func renderLogRowPlain(c gitcmd.Commit, width int) string {
	const gap = "  "
	short, date, author, _, restPlain, ok := commitRowColumns(c, width)
	if !ok {
		return padOrTruncate(short, width)
	}
	return short + gap + date + gap + author + gap + restPlain
}

// renderLogPanel renders the log panel, including its title and the
// visible window of commits. selectionActive indicates whether the
// selected row should get the bright vs dim highlight.
func renderLogPanel(m model, w, h int, active bool) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	lines := make([]string, 0, h)
	lines = append(lines, renderTitle(logPanelTitle(m), w, active))

	bodyH := h - 1
	if bodyH <= 0 {
		return strings.Join(lines, "\n")
	}

	if len(m.commits) == 0 {
		// Show a single placeholder line, fill rest with blanks.
		msg := "loading…"
		lines = append(lines, padOrTruncate(msg, w))
		for i := 2; i < h; i++ {
			lines = append(lines, strings.Repeat(" ", w))
		}
		return strings.Join(lines, "\n")
	}

	for row := 0; row < bodyH; row++ {
		idx := m.viewportTop + row
		if idx >= len(m.commits) {
			lines = append(lines, strings.Repeat(" ", w))
			continue
		}
		var rendered string
		switch {
		case idx == m.selectedIdx:
			plain := renderLogRowPlain(m.commits[idx], w)
			if active {
				rendered = activeSelectedRowStyle.Render(plain)
			} else {
				rendered = inactiveSelectedRowStyle.Render(plain)
			}
		case m.isSearchMatch(idx):
			plain := renderLogRowPlain(m.commits[idx], w)
			rendered = searchMatchRowStyle.Render(plain)
		default:
			rendered = renderLogRow(m.commits[idx], w)
		}
		lines = append(lines, rendered)
	}
	return strings.Join(lines, "\n")
}

// messageLines builds the full ordered list of lines for the message
// panel — metadata block followed by a blank separator and the body —
// without applying any scroll offset.
func messageLines(d gitcmd.CommitDetail) []string {
	if d.SHA == "" {
		return nil
	}
	lines := []string{
		"commit " + d.SHA,
		"Author:     " + d.AuthorName + " <" + d.AuthorEmail + ">",
		"AuthorDate: " + d.AuthorDateISO + "  (" + d.AuthorDateRel + ")",
		"Commit:     " + d.CommitterName + " <" + d.CommitterEmail + ">",
		"CommitDate: " + d.CommitterDateISO + "  (" + d.CommitterDateRel + ")",
	}
	if len(d.Parents) > 0 {
		lines = append(lines, "Parents:    "+strings.Join(d.Parents, " "))
	}
	if nonTag := nonTagRefs(d.Refs); len(nonTag) > 0 {
		lines = append(lines, "Refs:       "+strings.Join(nonTag, ", "))
	}
	for i, t := range d.Tags {
		label := "Tags:       "
		if i > 0 {
			label = "            "
		}
		name := t.Name
		if t.Annotated {
			name += " (annotated)"
		}
		lines = append(lines, label+name)
		if t.Annotated && t.Message != "" {
			for _, ml := range strings.Split(t.Message, "\n") {
				lines = append(lines, "              "+ml)
			}
		}
	}
	lines = append(lines, "")
	body := d.Body
	if body != "" {
		lines = append(lines, strings.Split(body, "\n")...)
	}
	return lines
}

// messageLineCount returns the total renderable line count for the
// message panel (without considering wrap or visible window height).
func messageLineCount(d gitcmd.CommitDetail) int { return len(messageLines(d)) }

var (
	msgLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	msgSHAStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
)

// styleMessageLine applies dim coloring to header label prefixes
// ("Author:", "AuthorDate:", etc.) and accent coloring to the commit
// sha row, so metadata is visually separable from the message body.
func styleMessageLine(line string) string {
	if strings.HasPrefix(line, "commit ") {
		return msgLabelStyle.Render("commit ") + msgSHAStyle.Render(line[len("commit "):])
	}
	for _, label := range []string{"Author:", "AuthorDate:", "Commit:", "CommitDate:", "Parents:", "Refs:", "Tags:"} {
		prefix := label
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		// Header label rows are padded to a fixed column ("Label:     ")
		// — find the run of spaces after the label so the colored
		// prefix includes that padding.
		end := len(prefix)
		for end < len(line) && line[end] == ' ' {
			end++
		}
		return msgLabelStyle.Render(line[:end]) + line[end:]
	}
	return line
}

// renderMessagePanel renders the top-right message panel using the
// model's currently-loaded detail and msgScroll offset. When stale is
// true the body is rendered with a single dim color (the spinner in
// the title is the active loading affordance).
func renderMessagePanel(m model, w, h int, active, stale bool) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	lines := make([]string, 0, h)
	lines = append(lines, renderTitleWithSpinner("message", w, active, stale, m.spinnerFrame))
	bodyH := h - 1
	if bodyH <= 0 {
		return strings.Join(lines, "\n")
	}
	all := messageLines(m.detail)
	if len(all) == 0 {
		lines = append(lines, padOrTruncate("loading…", w))
		for i := 2; i < h; i++ {
			lines = append(lines, strings.Repeat(" ", w))
		}
		return strings.Join(lines, "\n")
	}
	start := m.msgScroll
	if start > len(all)-1 {
		start = len(all) - 1
	}
	if start < 0 {
		start = 0
	}
	for row := 0; row < bodyH; row++ {
		idx := start + row
		if idx >= len(all) {
			lines = append(lines, strings.Repeat(" ", w))
			continue
		}
		raw := all[idx]
		truncated := padOrTruncate(raw, w)
		if stale {
			lines = append(lines, staleStyle.Render(truncated))
		} else {
			lines = append(lines, styleMessageLine(truncated))
		}
	}
	return strings.Join(lines, "\n")
}

// fileRowPlain returns the formatted row content as plain text (no
// color escapes), padded to exactly `width` cells. Used as the basis
// for the active/inactive selection highlight where styling is applied
// at the row level instead of per-column.
func fileRowPlain(r filelist.Row) string {
	const gap = "  "
	parts := []string{r.Status, gap, r.Path}
	if r.Plus != "" {
		parts = append(parts, gap, r.Plus)
	}
	if r.Minus != "" {
		parts = append(parts, gap, r.Minus)
	}
	if r.Pluses+r.Minuses > 0 {
		parts = append(parts, gap, strings.Repeat("+", r.Pluses)+strings.Repeat("-", r.Minuses))
	}
	return strings.Join(parts, "")
}

// fileRowStyled returns the row content with per-column colors applied.
func fileRowStyled(r filelist.Row) string {
	const gap = "  "
	status := r.Status
	if st, ok := fileStatusStyles[r.Status]; ok {
		status = st.Render(r.Status)
	}
	parts := []string{status, gap, r.Path}
	if r.Plus != "" {
		parts = append(parts, gap, filePlusStyle.Render(r.Plus))
	}
	if r.Minus != "" {
		parts = append(parts, gap, fileMinusStyle.Render(r.Minus))
	}
	if r.Pluses+r.Minuses > 0 {
		bar := filePlusStyle.Render(strings.Repeat("+", r.Pluses)) +
			fileMinusStyle.Render(strings.Repeat("-", r.Minuses))
		parts = append(parts, gap, bar)
	}
	return strings.Join(parts, "")
}

// renderFilesPanel renders the bottom-left files panel for the
// currently-loaded numstat. When stale is true the rows are rendered
// in a single dim color.
func renderFilesPanel(m model, w, h int, active, stale bool) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	lines := make([]string, 0, h)
	lines = append(lines, renderTitleWithSpinner("files", w, active, stale, m.spinnerFrame))
	bodyH := h - 1
	if bodyH <= 0 {
		return strings.Join(lines, "\n")
	}
	if len(m.files) == 0 {
		var placeholder string
		if m.filesSHA == "" {
			placeholder = "loading…"
		} else if m.detail.SHA == m.filesSHA && len(m.detail.Parents) > 1 {
			placeholder = "Clean merge, no conflicts resolved."
		} else {
			placeholder = "No changes"
		}
		lines = append(lines, padOrTruncate(placeholder, w))
		for i := 2; i < h; i++ {
			lines = append(lines, strings.Repeat(" ", w))
		}
		return strings.Join(lines, "\n")
	}
	rows := filelist.Format(toFilelist(m.files), w)
	for row := 0; row < bodyH; row++ {
		idx := m.filesViewportTop + row
		if idx >= len(rows) {
			lines = append(lines, strings.Repeat(" ", w))
			continue
		}
		var rendered string
		if stale {
			rendered = staleStyle.Render(padOrTruncate(fileRowPlain(rows[idx]), w))
		} else if idx == m.filesSelectedIdx {
			plain := padOrTruncate(fileRowPlain(rows[idx]), w)
			if active {
				rendered = activeSelectedRowStyle.Render(plain)
			} else {
				rendered = inactiveSelectedRowStyle.Render(plain)
			}
		} else if m.isFileSearchMatch(idx) {
			plain := padOrTruncate(fileRowPlain(rows[idx]), w)
			rendered = searchMatchRowStyle.Render(plain)
		} else {
			rendered = padOrTruncate(fileRowStyled(rows[idx]), w)
		}
		lines = append(lines, rendered)
	}
	return strings.Join(lines, "\n")
}

func toFilelist(in []gitcmd.FileStat) []filelist.File {
	out := make([]filelist.File, len(in))
	for i, f := range in {
		out[i] = filelist.File{
			Status:   f.Status,
			Path:     f.Path,
			OldPath:  f.OldPath,
			Added:    f.Added,
			Deleted:  f.Deleted,
			IsBinary: f.IsBinary,
		}
	}
	return out
}

// renderDiffPanel renders the bottom-right diff panel for the
// currently-loaded diff. When no diff is loaded (because no file is
// selected, the selected file is binary, or a load is in flight) the
// panel shows an appropriate placeholder. When stale is true the
// previously-rendered diff body is shown in a single dim color.
func renderDiffPanel(m model, w, h int, active, stale bool) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	lines := make([]string, 0, h)
	lines = append(lines, renderTitleWithSpinner("diff", w, active, stale, m.spinnerFrame))
	bodyH := h - 1
	if bodyH <= 0 {
		return strings.Join(lines, "\n")
	}
	// If we have prior diff content and are loading the next one, fall
	// through to render the stale content dimmed instead of showing the
	// "loading…" placeholder over a blank panel.
	if placeholder, ok := diffPlaceholder(m); ok && !(stale && len(m.diff.Lines) > 0) {
		lines = append(lines, padOrTruncate(placeholder, w))
		for i := 2; i < h; i++ {
			lines = append(lines, strings.Repeat(" ", w))
		}
		return strings.Join(lines, "\n")
	}
	start := m.diffScroll
	if start < 0 {
		start = 0
	}
	for row := 0; row < bodyH; row++ {
		idx := start + row
		if idx >= len(m.diff.Lines) {
			lines = append(lines, strings.Repeat(" ", w))
			continue
		}
		if stale {
			// Render the raw text without diff/syntax colors, padded and
			// horizontally scrolled the same way FormatLine handles it.
			ln := m.diff.Lines[idx]
			text := ln.Text
			if m.diffHScroll > 0 {
				if m.diffHScroll >= len(text) {
					text = ""
				} else {
					text = text[m.diffHScroll:]
				}
			}
			marker := " "
			switch ln.Kind {
			case diffrender.Add:
				marker = "+"
			case diffrender.Del:
				marker = "-"
			}
			lines = append(lines, staleStyle.Render(padOrTruncate(marker+text, w)))
		} else {
			lines = append(lines, m.diff.FormatLine(idx, w, m.diffHScroll))
		}
	}
	return strings.Join(lines, "\n")
}

// diffPlaceholder returns the message to render in the diff panel body
// when there's nothing to show — and ok=true when no rendered diff
// should be drawn. ok=false means the parsed diff is ready and the
// panel should render rows.
func diffPlaceholder(m model) (string, bool) {
	if len(m.commits) == 0 {
		return "loading…", true
	}
	if len(m.files) == 0 {
		return "", true
	}
	if m.filesSelectedIdx < len(m.files) && m.files[m.filesSelectedIdx].IsBinary {
		sha, path, ok := m.currentSelection()
		if !ok || m.binSizeSHA != sha || m.binSizePath != path {
			return "Binary file (loading size…)", true
		}
		return formatBinaryDelta(m.binOldSize, m.binNewSize, m.binHasOld, m.binHasNew), true
	}
	if m.diffLoading || m.diffSHA == "" {
		return "loading…", true
	}
	if len(m.diff.Lines) == 0 {
		return "No textual changes", true
	}
	return "", false
}

// formatBinaryDelta builds the diff-panel placeholder line for a
// binary file given its old/new byte sizes. Pure-add and pure-delete
// cases show only the side that exists. Modifications include a signed
// percent-change rounded to the nearest whole percent; if the old size
// is zero (modification against a sentinel) the percent is omitted to
// avoid a divide-by-zero.
func formatBinaryDelta(oldSize, newSize int64, hasOld, hasNew bool) string {
	switch {
	case hasOld && hasNew:
		if oldSize == 0 {
			return fmt.Sprintf("Binary file changed: %d → %d bytes", oldSize, newSize)
		}
		pct := int(math.Round(float64(newSize-oldSize) / float64(oldSize) * 100.0))
		sign := "+"
		if pct < 0 {
			sign = "-"
			pct = -pct
		}
		return fmt.Sprintf("Binary file changed: %d → %d bytes (%s%d%%)",
			oldSize, newSize, sign, pct)
	case hasNew:
		return fmt.Sprintf("Binary file added: %d bytes", newSize)
	case hasOld:
		return fmt.Sprintf("Binary file deleted: %d bytes", oldSize)
	default:
		return "Binary file"
	}
}

func (m model) View() string {
	if m.w == 0 || m.h == 0 {
		return ""
	}
	lo := layout.Compute(m.w, m.h)
	if lo.TooSmall {
		msg := fmt.Sprintf("Terminal too small (need ≥%dx%d)", layout.MinCols, layout.MinRows)
		body := msg + "\n" + tooSmallHintStyle.Render("q to quit")
		return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, body)
	}

	topActive := m.active == sectionTop
	botActive := m.active == sectionBottom

	var base string
	if lo.SmallMode {
		var topPanel, bottomPanel string
		if m.topShowRight {
			topPanel = renderMessagePanel(m, lo.TopLeft.W, lo.TopLeft.H, topActive, m.detailLoading)
		} else {
			topPanel = renderLogPanel(m, lo.TopLeft.W, lo.TopLeft.H, topActive)
		}
		if m.bottomShowRight {
			bottomPanel = renderDiffPanel(m, lo.BottomLeft.W, lo.BottomLeft.H, botActive, m.diffLoading)
		} else {
			bottomPanel = renderFilesPanel(m, lo.BottomLeft.W, lo.BottomLeft.H, botActive, m.filesLoading)
		}
		base = topPanel + "\n" + bottomPanel + "\n" + m.renderStatus(lo.Status.W)
	} else {
		topLeft := renderLogPanel(m, lo.TopLeft.W, lo.TopLeft.H, topActive)
		bottomLeft := renderFilesPanel(m, lo.BottomLeft.W, lo.BottomLeft.H, botActive, m.filesLoading)
		topRight := renderMessagePanel(m, lo.TopRight.W, lo.TopRight.H, topActive, m.detailLoading)
		bottomRight := renderDiffPanel(m, lo.BottomRight.W, lo.BottomRight.H, botActive, m.diffLoading)
		topRow := lipgloss.JoinHorizontal(lipgloss.Top, topLeft, topRight)
		bottomRow := lipgloss.JoinHorizontal(lipgloss.Top, bottomLeft, bottomRight)
		base = strings.Join([]string{topRow, bottomRow, m.renderStatus(lo.Status.W)}, "\n")
	}

	if m.helpModalOpen {
		return overlayCentered(base, renderHelpModal(), m.w, m.h)
	}
	if m.wtModalOpen {
		return overlayCentered(base, renderWorktreeModal(m), m.w, m.h)
	}
	return base
}

// renderHelpModal builds the help overlay: a bordered box listing every
// keybinding grouped by area. The content is static so the modal is
// rebuilt cheaply on every frame.
func renderHelpModal() string {
	type binding struct {
		keys string
		desc string
	}
	type group struct {
		title    string
		bindings []binding
	}
	groups := []group{
		{title: "Global", bindings: []binding{
			{"?", "toggle this help"},
			{"q / esc", "back (files → commits → quit; closes overlays)"},
			{"ctrl+c", "quit"},
			{"tab", "toggle between top and bottom section"},
			{"enter", "activate bottom section (small mode: swap to right panel)"},
			{"w", "switch worktree"},
			{"ctrl+r", "refresh (re-read log, drop caches)"},
		}},
		{title: "Commits (top)", bindings: []binding{
			{"ctrl+j / ctrl+k", "move selection"},
			{"/", "fuzzy search sha / subject / author"},
			{"n / N", "next / previous search match"},
		}},
		{title: "Files (bottom)", bindings: []binding{
			{"ctrl+j / ctrl+k", "move selection"},
			{"/", "fuzzy search file paths"},
			{"n / N", "next / previous search match"},
		}},
		{title: "Message / diff (right panels)", bindings: []binding{
			{"j / k", "line scroll"},
			{"d / u", "page scroll"},
			{"gg / G", "jump to top / bottom"},
			{"ctrl+d / ctrl+u", "next / previous hunk (diff); page (message)"},
			{"h / l", "horizontal scroll (diff)"},
		}},
	}

	keyW := 0
	for _, g := range groups {
		for _, b := range g.bindings {
			if w := lipgloss.Width(b.keys); w > keyW {
				keyW = w
			}
		}
	}

	title := modalTitleStyle.Render("Help")
	hint := modalHintStyle.Render("? / esc / q to close")

	var lines []string
	lines = append(lines, title)
	for _, g := range groups {
		lines = append(lines, "")
		lines = append(lines, helpGroupTitleStyle.Render(g.title))
		for _, b := range g.bindings {
			pad := keyW - lipgloss.Width(b.keys)
			if pad < 0 {
				pad = 0
			}
			row := helpKeyStyle.Render(b.keys) + strings.Repeat(" ", pad) + "  " + b.desc
			lines = append(lines, row)
		}
	}
	lines = append(lines, "")
	lines = append(lines, hint)

	contentW := 0
	for _, l := range lines {
		if w := lipgloss.Width(l); w > contentW {
			contentW = w
		}
	}
	for i, l := range lines {
		pad := contentW - lipgloss.Width(l)
		if pad > 0 {
			lines[i] = l + strings.Repeat(" ", pad)
		}
	}
	return modalBorderStyle.Render(strings.Join(lines, "\n"))
}

// renderWorktreeModal builds the worktree picker overlay: a bordered
// box with a title, one row per worktree (with a `●` marker on the
// current entry and a dim branch / detached-HEAD suffix), and a short
// help line at the bottom.
func renderWorktreeModal(m model) string {
	const minRowW = 30
	rowsPlain := make([]string, len(m.wtList))
	for i, w := range m.wtList {
		marker := "  "
		if w.Current {
			marker = "● "
		}
		suffix := ""
		switch {
		case w.Branch != "":
			suffix = "  " + w.Branch
		case w.Detached:
			suffix = "  (detached)"
		case w.Bare:
			suffix = "  (bare)"
		}
		rowsPlain[i] = marker + w.Path + suffix
	}
	contentW := minRowW
	for _, r := range rowsPlain {
		if rw := lipgloss.Width(r); rw > contentW {
			contentW = rw
		}
	}
	title := modalTitleStyle.Render("Switch worktree")
	if tw := lipgloss.Width(title); tw > contentW {
		contentW = tw
	}
	hint := modalHintStyle.Render("j/k move · enter select · esc cancel")
	if hw := lipgloss.Width(hint); hw > contentW {
		contentW = hw
	}

	var sb strings.Builder
	sb.WriteString(title + strings.Repeat(" ", contentW-lipgloss.Width(title)))
	sb.WriteString("\n\n")
	for i, w := range m.wtList {
		marker := "  "
		if w.Current {
			marker = modalCurrentMarkerStyle.Render("● ")
		}
		suffix := ""
		switch {
		case w.Branch != "":
			suffix = modalBranchStyle.Render("  " + w.Branch)
		case w.Detached:
			suffix = modalBranchStyle.Render("  (detached)")
		case w.Bare:
			suffix = modalBranchStyle.Render("  (bare)")
		}
		plain := rowsPlain[i]
		pad := contentW - lipgloss.Width(plain)
		if pad < 0 {
			pad = 0
		}
		if i == m.wtModalIdx {
			row := padOrTruncate(plain, contentW)
			sb.WriteString(modalSelectedStyle.Render(row))
		} else {
			sb.WriteString(marker)
			sb.WriteString(w.Path)
			sb.WriteString(suffix)
			sb.WriteString(strings.Repeat(" ", pad))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	sb.WriteString(hint + strings.Repeat(" ", contentW-lipgloss.Width(hint)))

	return modalBorderStyle.Render(sb.String())
}

// overlayCentered draws modal onto base, centered within the (w, h)
// terminal rectangle. Base rows outside the modal's vertical band are
// preserved; rows within the band are fully replaced with `leftPad
// spaces + modal row + rightPad spaces` so the modal renders cleanly
// without ANSI-aware slicing of the underlying view.
func overlayCentered(base, modal string, w, h int) string {
	baseLines := strings.Split(base, "\n")
	modalLines := strings.Split(modal, "\n")
	if len(modalLines) == 0 {
		return base
	}
	mw := 0
	for _, l := range modalLines {
		if lw := lipgloss.Width(l); lw > mw {
			mw = lw
		}
	}
	topPad := (h - len(modalLines)) / 2
	if topPad < 0 {
		topPad = 0
	}
	leftPad := (w - mw) / 2
	if leftPad < 0 {
		leftPad = 0
	}
	rightPad := w - leftPad - mw
	if rightPad < 0 {
		rightPad = 0
	}
	out := make([]string, len(baseLines))
	copy(out, baseLines)
	for i, ml := range modalLines {
		row := topPad + i
		if row < 0 || row >= len(out) {
			continue
		}
		out[row] = strings.Repeat(" ", leftPad) + ml + strings.Repeat(" ", rightPad)
	}
	return strings.Join(out, "\n")
}

func (m model) renderStatus(width int) string {
	if width <= 0 {
		return ""
	}
	if m.searchActive {
		var hint string
		switch {
		case m.searchQuery == "":
			hint = " (type to search; enter confirms, esc cancels)"
		case len(m.searchMatches) == 0:
			hint = " (no matches)"
		default:
			hint = fmt.Sprintf(" (%d match%s)", len(m.searchMatches), plural(len(m.searchMatches)))
		}
		return searchPromptStyle.Render(padOrTruncate("/"+m.searchQuery+"█"+hint, width))
	}
	if m.fileSearchActive {
		var hint string
		switch {
		case m.fileSearchQuery == "":
			hint = " (type to search files; enter confirms, esc cancels)"
		case len(m.fileSearchMatches) == 0:
			hint = " (no matches)"
		default:
			hint = fmt.Sprintf(" (%d match%s)", len(m.fileSearchMatches), plural(len(m.fileSearchMatches)))
		}
		return searchPromptStyle.Render(padOrTruncate("/"+m.fileSearchQuery+"█"+hint, width))
	}
	if m.errMsg != "" {
		ts := m.errMsgTime
		if ts.IsZero() {
			ts = time.Now()
		}
		body := fmt.Sprintf("[%s] %s", ts.Format("15:04:05"), m.errMsg)
		return errMsgStyle.Render(padOrTruncate(body, width))
	}
	if m.statusMsg != "" {
		ts := m.statusMsgTime
		if ts.IsZero() {
			ts = time.Now()
		}
		body := fmt.Sprintf("[%s] %s", ts.Format("15:04:05"), m.statusMsg)
		return statusMsgStyle.Render(padOrTruncate(body, width))
	}
	return strings.Repeat(" ", width)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "es"
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "git-review-tui: cannot determine working directory: %v\n", err)
		os.Exit(1)
	}
	top, err := gitcmd.TopLevel(ctx, cwd)
	if err != nil {
		runStartupError("Not inside a git repository.", cwd)
		return
	}
	git := gitcmd.New(top)

	hasHead, err := git.HasHead(ctx)
	if err != nil {
		runStartupError("Failed to read repository HEAD.", err.Error())
		return
	}
	if !hasHead {
		runStartupError("Repository has no commits yet.", top)
		return
	}

	branch, label := resolveWorktreeLabel(ctx, git)

	p := tea.NewProgram(initialModel(git, branch, label), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runStartupError takes over the terminal with a minimal Bubble Tea
// model that renders a centered full-screen message plus a `q to quit`
// hint. Used for startup failure cases (no repo, no commits) so the
// user gets a clean TUI rather than a stderr dump.
func runStartupError(headline, detail string) {
	p := tea.NewProgram(startupErrorModel{headline: headline, detail: detail}, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type startupErrorModel struct {
	w, h     int
	headline string
	detail   string
}

func (m startupErrorModel) Init() tea.Cmd { return nil }

func (m startupErrorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m startupErrorModel) View() string {
	if m.w == 0 || m.h == 0 {
		return ""
	}
	parts := []string{m.headline}
	if m.detail != "" {
		parts = append(parts, tooSmallHintStyle.Render(m.detail))
	}
	parts = append(parts, "", tooSmallHintStyle.Render("q to quit"))
	body := strings.Join(parts, "\n")
	return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, body)
}

// resolveWorktreeLabel returns the branch name of the current worktree
// and, when more than one worktree exists, the basename of its path so
// the log panel title can disambiguate. A failure to enumerate
// worktrees is non-fatal: the title falls back to the bare "log" form.
func resolveWorktreeLabel(ctx context.Context, git *gitcmd.Client) (string, string) {
	wts, err := git.Worktrees(ctx)
	if err != nil {
		return "", ""
	}
	var cur gitcmd.Worktree
	for _, w := range wts {
		if w.Current {
			cur = w
			break
		}
	}
	branch := cur.Branch
	if branch == "" && cur.Detached && cur.HeadSHA != "" {
		// Surface a short sha for detached HEAD so the title still
		// communicates which commit the worktree points at.
		if len(cur.HeadSHA) >= 7 {
			branch = "(detached " + cur.HeadSHA[:7] + ")"
		} else {
			branch = "(detached)"
		}
	}
	label := ""
	if len(wts) > 1 && cur.Path != "" {
		label = filepath.Base(cur.Path)
	}
	return branch, label
}

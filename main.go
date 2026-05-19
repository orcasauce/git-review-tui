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
	"github.com/orcasauce/git-review-tui/hunkstate"
	"github.com/orcasauce/git-review-tui/layout"
	"github.com/orcasauce/git-review-tui/loader"
	"github.com/orcasauce/git-review-tui/metadata"
	"github.com/orcasauce/git-review-tui/searchfilter"
)

type section int

const (
	sectionLog section = iota
	sectionFiles
	sectionMessage
	sectionDiff
)

// middleTab selects which panel is rendered in the small-mode middle
// region. Only one of the three is visible at a time; Ctrl+h / Ctrl+l
// rotates between them. The Files and Message tabs are focusable
// (their keys move file selection / scroll the message body); the
// Metadata tab is view-only — it has no scroll state of its own, so
// when it is the active tab the focused section stays on the most
// recently focused middle panel and scroll keystrokes target that.
type middleTab int

const (
	tabMetadata middleTab = iota
	tabFiles
	tabMessage
)

// ActionKind is the kind of pending action queued against a commit.
// The enum is shaped so future action types (squash, reword, edit) can
// be added without changing the model field's shape.
type ActionKind int

const (
	// ActionDrop marks a commit to be removed by an interactive rebase.
	ActionDrop ActionKind = iota + 1
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
	// activeHunk is the index into m.diff.HunkStarts of the hunk the
	// user is currently parked on. It is hunkstate.NoActiveHunk for a
	// diff with zero hunks. Plain scrolling never updates it; n / N /
	// ctrl+d / ctrl+u in the diff and files panels do. It is persisted
	// per (commit, file) via hunks so returning to a file restores the
	// last position.
	activeHunk int
	hunks      *hunkstate.Tracker
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

	// Small-mode middle-tab state. In small mode the middle region is
	// a tabbed container with three tabs (metadata, files, message);
	// only the active tab's body is rendered. `Ctrl+h` / `Ctrl+l`
	// rotate the active tab when focus is in the middle region. The
	// value persists across small-mode entry/exit so the user's chosen
	// view survives terminal resizes. In full mode all three middle
	// panels are visible simultaneously and this field is ignored.
	middleTab middleTab

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

	// pendingActions queues per-commit actions staged via ctrl+d (and,
	// in later slices, additional action keys). The leftmost action
	// column appears in the log panel only when this map is non-empty.
	// Entries survive ctrl+r refresh (with vanished SHAs pruned) and
	// are cleared on worktree switch.
	pendingActions map[string]ActionKind

	// Rebase state machine. Exactly one of the popups is up at a time;
	// rebaseState identifies which. Idle means no rebase UI is active.
	rebaseState     rebaseUIState
	rebaseRefuseMsg string
	// rebaseSnapshot is the set of SHAs marked for drop at the moment
	// ctrl+s was confirmed. Used to compute the post-success cursor
	// anchor and to know which marks to clear on success.
	rebaseSnapshot []string
	// rebaseAnchorSHA is the closest unmarked SHA below (older than) the
	// dropped range at confirm time. The post-rebase refresh restores
	// the cursor onto this SHA via the existing pendingRefreshSHA path.
	rebaseAnchorSHA string
	// rebaseCancel cancels the in-flight rebase context when esc is
	// pressed on the blocking modal. Cleared once the goroutine returns.
	rebaseCancel context.CancelFunc
	// rebaseHalt holds the most recent RebaseHalted result while the
	// conflict popup is up. Drives the popup's header and conflicted-path
	// list.
	rebaseHalt gitcmd.RebaseResult
	// rebaseManualUnmerged is populated when the manual-resolve OK button
	// is pressed while unmerged paths remain. Renders the list in the
	// popup so the user knows exactly what is still unresolved.
	rebaseManualUnmerged []string
}

// rebaseUIState tracks which rebase popup is currently up. Most
// rebase-state transitions are driven by tea.Msg deliveries from the
// rebase goroutine; key handlers intercept input while one of these
// states is non-idle.
type rebaseUIState int

const (
	rebaseStateIdle rebaseUIState = iota
	rebaseStateSummary
	rebaseStateRunning
	rebaseStateRefuse
	rebaseStateConflict
	rebaseStateManualWait
)

func initialModel(git *gitcmd.Client, branch, worktreeLabel string) model {
	ldr := loader.New(loader.Config{Source: git})
	return model{
		active:         sectionLog,
		middleTab:      tabFiles,
		git:            git,
		ldr:            ldr,
		branch:         branch,
		worktreeLabel:  worktreeLabel,
		pendingActions: map[string]ActionKind{},
		activeHunk:     hunkstate.NoActiveHunk,
		hunks:          hunkstate.New(),
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

// rebaseDoneMsg delivers the outcome of a RebaseDropStart goroutine
// back to Update. cancelled is set when the rebase context was cancelled
// (esc on the blocking modal); err is set for unexpected gitcmd-level
// failures. Otherwise result.State distinguishes done / halted / error.
type rebaseDoneMsg struct {
	result    gitcmd.RebaseResult
	cancelled bool
	err       error
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
		// On a first-page replacement (refresh or initial load), prune
		// pending-action marks to commits that still exist. Vanished SHAs
		// are silently dropped; no status message.
		if msg.skip == 0 && len(m.pendingActions) > 0 {
			present := make(map[string]struct{}, len(m.commits))
			for _, c := range m.commits {
				present[c.SHA] = struct{}{}
			}
			for sha := range m.pendingActions {
				if _, ok := present[sha]; !ok {
					delete(m.pendingActions, sha)
				}
			}
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
		m.diff = diffrender.Parse(msg.Raw, msg.Hunks, msg.Path)
		m.diffSHA = msg.SHA
		m.diffPath = msg.Path
		m.diffScroll = 0
		m.diffHScroll = 0
		m.diffLoading = false
		m.activeHunk = m.hunks.Get(msg.SHA, msg.Path, len(m.diff.HunkStarts))
		m.scrollToActiveHunk()
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

	case rebaseDoneMsg:
		return m.handleRebaseDone(msg)

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
		// Rebase popups intercept all keys while any of them is up.
		if m.rebaseState != rebaseStateIdle {
			return m.updateRebasePopup(keyStr)
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
			if m.commitListSection() {
				m.openSearchPrompt()
				return m, nil
			}
			if m.fileListSection() {
				m.openFileSearchPrompt()
				return m, nil
			}
		case "n":
			if m.commitListSection() && len(m.searchMatches) > 0 {
				m.cycleSearchMatch(1)
				return m, m.onSelectionChanged()
			}
			if m.fileListSection() && len(m.fileSearchMatches) > 0 {
				m.cycleFileSearchMatch(1)
				return m, m.onFileSelectionChanged()
			}
			if m.fileListSection() {
				m.advanceActiveHunk(1)
				return m, nil
			}
		case "N":
			if m.commitListSection() && len(m.searchMatches) > 0 {
				m.cycleSearchMatch(-1)
				return m, m.onSelectionChanged()
			}
			if m.fileListSection() && len(m.fileSearchMatches) > 0 {
				m.cycleFileSearchMatch(-1)
				return m, m.onFileSelectionChanged()
			}
			if m.fileListSection() {
				m.advanceActiveHunk(-1)
				return m, nil
			}
		case "q", "esc":
			// "Back" semantics: any non-log section steps back to log;
			// from log, quit.
			if m.active != sectionLog {
				m.active = sectionLog
				return m, nil
			}
			return m, tea.Quit
		case "enter":
			// `enter` advances focus along the Tab cycle. From log it
			// activates the next region (middle in small mode, files
			// in full mode); from the middle region it advances to
			// the diff. From diff it's a no-op.
			lo := layout.Compute(m.w, m.h)
			if m.active == sectionLog {
				if lo.SmallMode {
					m.active = m.middleFocusSection()
				} else {
					m.active = sectionFiles
				}
				return m, nil
			}
			if lo.SmallMode && (m.active == sectionFiles || m.active == sectionMessage) {
				m.active = sectionDiff
				return m, nil
			}
			return m, nil
		case "tab":
			m.advanceTab(1)
			return m, nil
		case "shift+tab":
			m.advanceTab(-1)
			return m, nil
		case "ctrl+h":
			if layout.Compute(m.w, m.h).SmallMode && m.inMiddleRegion() {
				m.rotateMiddleTab(-1)
				return m, nil
			}
		case "ctrl+l":
			if layout.Compute(m.w, m.h).SmallMode && m.inMiddleRegion() {
				m.rotateMiddleTab(1)
				return m, nil
			}
		case "ctrl+j":
			if m.commitListSection() && len(m.commits) > 0 {
				if m.selectedIdx < len(m.commits)-1 {
					m.selectedIdx++
				}
				m.clampViewport()
				return m, m.onSelectionChanged()
			}
			if m.active == sectionFiles {
				m.scrollDiff(1)
				return m, nil
			}
		case "ctrl+k":
			if m.commitListSection() && len(m.commits) > 0 {
				if m.selectedIdx > 0 {
					m.selectedIdx--
				}
				m.clampViewport()
				return m, m.onSelectionChanged()
			}
			if m.active == sectionFiles {
				m.scrollDiff(-1)
				return m, nil
			}
		case "j":
			switch m.active {
			case sectionLog:
				if len(m.commits) > 0 && m.selectedIdx < len(m.commits)-1 {
					m.selectedIdx++
					m.clampViewport()
					return m, m.onSelectionChanged()
				}
				return m, nil
			case sectionFiles:
				if len(m.files) > 0 && m.filesSelectedIdx < len(m.files)-1 {
					m.filesSelectedIdx++
					m.clampFilesViewport()
					return m, m.onFileSelectionChanged()
				}
				return m, nil
			case sectionMessage:
				m.scrollMessage(1)
				return m, nil
			case sectionDiff:
				m.scrollDiff(1)
				return m, nil
			}
		case "k":
			switch m.active {
			case sectionLog:
				if len(m.commits) > 0 && m.selectedIdx > 0 {
					m.selectedIdx--
					m.clampViewport()
					return m, m.onSelectionChanged()
				}
				return m, nil
			case sectionFiles:
				if len(m.files) > 0 && m.filesSelectedIdx > 0 {
					m.filesSelectedIdx--
					m.clampFilesViewport()
					return m, m.onFileSelectionChanged()
				}
				return m, nil
			case sectionMessage:
				m.scrollMessage(-1)
				return m, nil
			case sectionDiff:
				m.scrollDiff(-1)
				return m, nil
			}
		case "d":
			switch m.active {
			case sectionLog:
				return m, m.pageCommitSelection(1)
			case sectionFiles:
				return m, m.pageFileSelection(1)
			case sectionMessage:
				m.pageMessage(1)
				return m, nil
			case sectionDiff:
				m.pageDiff(1)
				return m, nil
			}
		case "u":
			switch m.active {
			case sectionLog:
				return m, m.pageCommitSelection(-1)
			case sectionFiles:
				return m, m.pageFileSelection(-1)
			case sectionMessage:
				m.pageMessage(-1)
				return m, nil
			case sectionDiff:
				m.pageDiff(-1)
				return m, nil
			}
		case "G":
			switch m.active {
			case sectionLog:
				return m, m.jumpCommitBottom()
			case sectionFiles:
				return m, m.jumpFileBottom()
			case sectionMessage:
				m.jumpMessageBottom()
				return m, nil
			case sectionDiff:
				m.jumpDiffBottom()
				return m, nil
			}
		case "g":
			if pendingG {
				switch m.active {
				case sectionLog:
					return m, m.jumpCommitTop()
				case sectionFiles:
					return m, m.jumpFileTop()
				case sectionMessage:
					m.msgScroll = 0
					return m, nil
				case sectionDiff:
					m.diffScroll = 0
					return m, nil
				}
				return m, nil
			}
			m.pendingG = true
			return m, nil
		case "h":
			if m.active == sectionDiff {
				if m.diffHScroll > 0 {
					m.diffHScroll--
				}
				return m, nil
			}
		case "l":
			if m.active == sectionDiff {
				m.diffHScroll++
				return m, nil
			}
		case "ctrl+d":
			if m.commitListSection() {
				return m, m.toggleDropMark()
			}
			if m.active == sectionDiff {
				m.advanceActiveHunk(1)
				return m, nil
			}
		case "ctrl+s":
			return m.startSave()
		case "ctrl+u":
			if m.active == sectionMessage {
				m.pageMessage(-1)
				return m, nil
			}
			if m.active == sectionDiff {
				m.advanceActiveHunk(-1)
				return m, nil
			}
		}
	}
	return m, nil
}

// commitListSection reports whether the active section operates on the
// commit list — sectionLog (log focused) or sectionMessage (message
// focused, but the contextual list is still the commits). Used by
// keys that target the commit list regardless of which of the two
// commit-related panels has focus (e.g. `/`, `n`/`N`, `ctrl+j/k`,
// `ctrl+d` mark-for-drop).
func (m *model) commitListSection() bool {
	return m.active == sectionLog || m.active == sectionMessage
}

// fileListSection reports whether the active section operates on the
// file list — sectionFiles or sectionDiff. Symmetric to
// commitListSection.
func (m *model) fileListSection() bool {
	return m.active == sectionFiles || m.active == sectionDiff
}

// inMiddleRegion reports whether the active section is one of the
// panels that lives in the middle region of the small-mode layout
// (files or message). Used to gate the small-mode Ctrl+h / Ctrl+l
// middle-tab rotation.
func (m *model) inMiddleRegion() bool {
	return m.active == sectionFiles || m.active == sectionMessage
}

// middleFocusSection returns the scrollable section that corresponds
// to the current middleTab. Used when entering the middle region in
// small mode (via Tab from log or via Ctrl+h/l onto a focusable tab).
// The metadata tab has no scroll state, so it falls back to sectionFiles
// — the user can still cycle to it via Ctrl+h/l, but focus settles on
// a tab where keystrokes do something useful.
func (m *model) middleFocusSection() section {
	switch m.middleTab {
	case tabMessage:
		return sectionMessage
	default:
		return sectionFiles
	}
}

// advanceTab rotates m.active by one step along the Tab cycle. In full
// mode the cycle is log → files → message → diff → log, with the
// message section skipped when its content fits the viewport. In small
// mode the cycle is log → middle → diff → log, where "middle" is the
// scrollable section implied by the current middleTab; the small-mode
// cycle is unaffected by the message-fits state because the explicit
// middle-tab strip is the user's chosen way to land on message there.
func (m *model) advanceTab(dir int) {
	lo := layout.Compute(m.w, m.h)
	if lo.SmallMode {
		// Three logical regions: log, middle, diff. Map the current
		// section onto one of them, advance, then map back.
		var region int
		switch m.active {
		case sectionLog:
			region = 0
		case sectionFiles, sectionMessage:
			region = 1
		case sectionDiff:
			region = 2
		}
		region = (region + dir + 3) % 3
		switch region {
		case 0:
			m.active = sectionLog
		case 1:
			m.active = m.middleFocusSection()
		case 2:
			m.active = sectionDiff
		}
		return
	}
	m.active = nextTabSection(m.active, dir, m.messageFitsFull(lo))
}

// nextTabSection returns the next focused section along the full-mode
// Tab cycle (log → files → message → diff → log). When msgFits is true,
// the message section is skipped, so the effective cycle is
// log → files → diff → log. dir is +1 for forward, -1 for reverse.
//
// If the current section is sectionMessage and msgFits is true (the
// content just shrank to fit while focus was on it), the function still
// advances correctly: one step out of message in the requested direction.
func nextTabSection(cur section, dir int, msgFits bool) section {
	order := []section{sectionLog, sectionFiles, sectionMessage, sectionDiff}
	idx := 0
	for i, s := range order {
		if s == cur {
			idx = i
			break
		}
	}
	idx = (idx + dir + len(order)) % len(order)
	if msgFits && order[idx] == sectionMessage {
		idx = (idx + dir + len(order)) % len(order)
	}
	return order[idx]
}

// messageFitsFull reports whether the message panel's content fits
// fully within its viewport in full mode (no scrollbar drawn). The
// scrollbar `draw` flag is the same signal used by the message panel
// renderer, so the tab-skip decision stays in lockstep with what the
// user sees on screen. Returns false in small mode — the small-mode
// middle tab strip stays on its own cycle.
func (m *model) messageFitsFull(lo layout.Layout) bool {
	if lo.SmallMode {
		return false
	}
	bodyH := lo.Message.H - 1
	_, _, draw := layout.ScrollbarThumb(messageLineCount(m.detail), bodyH, m.msgScroll, bodyH)
	return !draw
}

// rotateMiddleTab rotates middleTab by one step (metadata → files →
// message → metadata) in the given direction. When the new tab is
// focusable (files or message), m.active is updated to match so j/k
// targets the visible panel. When the new tab is metadata, m.active
// is left alone — the user is viewing metadata while keystrokes still
// route to the last focused middle panel.
func (m *model) rotateMiddleTab(dir int) {
	order := []middleTab{tabMetadata, tabFiles, tabMessage}
	idx := 0
	for i, t := range order {
		if t == m.middleTab {
			idx = i
			break
		}
	}
	idx = (idx + dir + len(order)) % len(order)
	m.middleTab = order[idx]
	switch m.middleTab {
	case tabFiles:
		m.active = sectionFiles
	case tabMessage:
		m.active = sectionMessage
	}
}

// pageCommitSelection moves selectedIdx by ±half a log-body page. Used
// by `d`/`u` when the log panel is focused. Returns a command in case
// the new selection requires loading more commits / refreshing the
// detail and diff panes.
func (m *model) pageCommitSelection(dir int) tea.Cmd {
	if len(m.commits) == 0 {
		return nil
	}
	page := m.logBodyHeight() / 2
	if page < 1 {
		page = 1
	}
	target := m.selectedIdx + dir*page
	if target < 0 {
		target = 0
	}
	if target > len(m.commits)-1 {
		target = len(m.commits) - 1
	}
	if target == m.selectedIdx {
		return nil
	}
	m.selectedIdx = target
	m.clampViewport()
	return m.onSelectionChanged()
}

// pageFileSelection moves filesSelectedIdx by ±half a files-body page.
// Used by `d`/`u` when the files panel is focused.
func (m *model) pageFileSelection(dir int) tea.Cmd {
	if len(m.files) == 0 {
		return nil
	}
	page := m.filesBodyHeight() / 2
	if page < 1 {
		page = 1
	}
	target := m.filesSelectedIdx + dir*page
	if target < 0 {
		target = 0
	}
	if target > len(m.files)-1 {
		target = len(m.files) - 1
	}
	if target == m.filesSelectedIdx {
		return nil
	}
	m.filesSelectedIdx = target
	m.clampFilesViewport()
	return m.onFileSelectionChanged()
}

// jumpCommitTop jumps the commit selection to the first loaded commit.
func (m *model) jumpCommitTop() tea.Cmd {
	if len(m.commits) == 0 || m.selectedIdx == 0 {
		return nil
	}
	m.selectedIdx = 0
	m.clampViewport()
	return m.onSelectionChanged()
}

// jumpCommitBottom jumps the commit selection to the last loaded commit.
// Does NOT trigger another page load; the user can press `j` to load
// more from there.
func (m *model) jumpCommitBottom() tea.Cmd {
	if len(m.commits) == 0 {
		return nil
	}
	target := len(m.commits) - 1
	if target == m.selectedIdx {
		return nil
	}
	m.selectedIdx = target
	m.clampViewport()
	return m.onSelectionChanged()
}

// jumpFileTop jumps the file selection to the first file.
func (m *model) jumpFileTop() tea.Cmd {
	if len(m.files) == 0 || m.filesSelectedIdx == 0 {
		return nil
	}
	m.filesSelectedIdx = 0
	m.clampFilesViewport()
	return m.onFileSelectionChanged()
}

// jumpFileBottom jumps the file selection to the last file.
func (m *model) jumpFileBottom() tea.Cmd {
	if len(m.files) == 0 {
		return nil
	}
	target := len(m.files) - 1
	if target == m.filesSelectedIdx {
		return nil
	}
	m.filesSelectedIdx = target
	m.clampFilesViewport()
	return m.onFileSelectionChanged()
}

// toggleDropMark flips an ActionDrop entry on the currently-selected
// commit's pending-actions map. Refused (with a status-row message)
// when the commit is a merge or the repo's root commit. The merge /
// root checks run synchronously against gitcmd; both shell out to a
// short rev-list call and are bounded by a 2-second context.
func (m *model) toggleDropMark() tea.Cmd {
	if len(m.commits) == 0 {
		return nil
	}
	if m.selectedIdx < 0 || m.selectedIdx >= len(m.commits) {
		return nil
	}
	sha := m.commits[m.selectedIdx].SHA
	if m.pendingActions == nil {
		m.pendingActions = map[string]ActionKind{}
	}
	if _, ok := m.pendingActions[sha]; ok {
		delete(m.pendingActions, sha)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	isMerge, err := m.git.IsMerge(ctx, sha)
	if err != nil {
		return m.raiseError(err)
	}
	if isMerge {
		return m.raiseStatus("Cannot drop merge commits (yet)")
	}
	isRoot, err := m.git.IsRootCommit(ctx, sha)
	if err != nil {
		return m.raiseError(err)
	}
	if isRoot {
		return m.raiseStatus("Cannot drop the root commit")
	}
	m.pendingActions[sha] = ActionDrop
	return nil
}

// startSave is the ctrl+s entry point. With zero marks it flashes
// `No pending actions` in the status row. Otherwise it runs the full
// precondition battery and either opens the refuse popup with an
// actionable message or the action-summary popup. The actual rebase
// is launched only after the user confirms the summary popup (see
// confirmRebase below).
func (m model) startSave() (tea.Model, tea.Cmd) {
	if len(m.pendingActions) == 0 {
		return m, m.raiseStatus("No pending actions")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := m.git.Status(ctx)
	if err != nil {
		return m, m.raiseError(err)
	}
	switch {
	case !info.Clean:
		m.rebaseRefuseMsg = "Worktree has uncommitted changes — commit or stash first"
		m.rebaseState = rebaseStateRefuse
		return m, nil
	case info.HeadBranch == "":
		m.rebaseRefuseMsg = "HEAD is detached — checkout a branch first"
		m.rebaseState = rebaseStateRefuse
		return m, nil
	case info.BranchCheckedOutAt != "":
		m.rebaseRefuseMsg = fmt.Sprintf("Branch %s is also checked out in %s", info.HeadBranch, info.BranchCheckedOutAt)
		m.rebaseState = rebaseStateRefuse
		return m, nil
	}
	// Stale-mark check: every marked SHA must still resolve.
	missing := 0
	for sha := range m.pendingActions {
		ok, err := m.git.CommitExists(ctx, sha)
		if err != nil {
			return m, m.raiseError(err)
		}
		if !ok {
			missing++
		}
	}
	if missing > 0 {
		m.rebaseRefuseMsg = fmt.Sprintf("%d marked commit%s no longer exist — refresh first",
			missing, plural2(missing))
		m.rebaseState = rebaseStateRefuse
		return m, nil
	}
	m.rebaseState = rebaseStateSummary
	return m, nil
}

// plural2 returns "" for n==1 and "s" otherwise. Used for messages like
// "N commits".
func plural2(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// updateRebasePopup handles every keystroke while a rebase popup
// (summary, running, or refuse) is up. The PRD's "popups intercept
// all key input" rule is enforced by returning unconditionally without
// falling through to the panel keybindings.
func (m model) updateRebasePopup(keyStr string) (tea.Model, tea.Cmd) {
	switch m.rebaseState {
	case rebaseStateRefuse:
		switch keyStr {
		case "ctrl+c":
			return m, tea.Quit
		case "esc", "q", "enter":
			m.rebaseState = rebaseStateIdle
			m.rebaseRefuseMsg = ""
			return m, nil
		}
		return m, nil
	case rebaseStateSummary:
		switch keyStr {
		case "ctrl+c":
			return m, tea.Quit
		case "esc", "q":
			m.rebaseState = rebaseStateIdle
			return m, nil
		case "enter":
			return m.confirmRebase()
		}
		return m, nil
	case rebaseStateRunning:
		switch keyStr {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.rebaseCancel != nil {
				m.rebaseCancel()
			}
			return m, nil
		}
		return m, nil
	case rebaseStateConflict:
		switch keyStr {
		case "ctrl+c":
			return m, tea.Quit
		case "a":
			return m.conflictAbort()
		case "t":
			return m.conflictResolveSide(gitcmd.SideTheirs)
		case "o":
			return m.conflictResolveSide(gitcmd.SideOurs)
		case "m":
			return m.conflictManualResolve()
		}
		return m, nil
	case rebaseStateManualWait:
		switch keyStr {
		case "ctrl+c":
			return m, tea.Quit
		case "enter":
			return m.manualWaitOK()
		case "esc", "q":
			// Back to the conflict popup so the user can pick a different
			// strategy without losing whatever staging they've already done.
			m.rebaseState = rebaseStateConflict
			m.rebaseManualUnmerged = nil
			return m, nil
		}
		return m, nil
	}
	return m, nil
}

// conflictManualResolve handles [m] on the conflict popup: hands control
// to the user to resolve the conflict in another terminal. The popup
// switches to a waiting state with an OK button; the rebase is left
// in its halted state on disk.
func (m model) conflictManualResolve() (tea.Model, tea.Cmd) {
	m.rebaseState = rebaseStateManualWait
	m.rebaseManualUnmerged = nil
	return m, nil
}

// manualWaitOK runs the precheck for the [OK] button on the manual-wait
// popup. If `git status` still reports unmerged paths, the popup stays
// up and lists them. Otherwise the rebase continues via the existing
// goroutine + rebaseDoneMsg routing.
func (m model) manualWaitOK() (tea.Model, tea.Cmd) {
	statusCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := m.git.Status(statusCtx)
	if err != nil {
		return m, m.raiseError(err)
	}
	if len(info.UnmergedPaths) > 0 {
		m.rebaseManualUnmerged = info.UnmergedPaths
		return m, nil
	}
	m.rebaseHalt = gitcmd.RebaseResult{}
	m.rebaseManualUnmerged = nil
	m.rebaseState = rebaseStateRunning
	ctx, cancelRun := context.WithCancel(context.Background())
	m.rebaseCancel = cancelRun
	return m, m.runRebaseContinueAfterManualCmd(ctx)
}

// runRebaseContinueAfterManualCmd runs RebaseContinue in a goroutine
// and posts the outcome as a rebaseDoneMsg. Unlike runRebaseContinueCmd,
// no CheckoutSide step is needed — the user has already staged their
// resolution from another terminal.
func (m model) runRebaseContinueAfterManualCmd(ctx context.Context) tea.Cmd {
	git := m.git
	return func() tea.Msg {
		res, err := git.RebaseContinue(ctx)
		if ctx.Err() != nil {
			return rebaseDoneMsg{cancelled: true}
		}
		return rebaseDoneMsg{result: res, err: err}
	}
}

// conflictAbort handles the [a] button on the conflict popup: runs
// `git rebase --abort`, preserves the mark set, and surfaces a status
// message confirming the repo is back to its pre-rebase state.
func (m model) conflictAbort() (tea.Model, tea.Cmd) {
	abortCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = m.git.RebaseAbort(abortCtx)
	m.rebaseState = rebaseStateIdle
	m.rebaseHalt = gitcmd.RebaseResult{}
	m.rebaseManualUnmerged = nil
	m.rebaseSnapshot = nil
	m.rebaseAnchorSHA = ""
	return m, m.raiseStatus("Drop cancelled — repo unchanged")
}

// conflictResolveSide handles [t] / [o] on the conflict popup: stages
// every conflicted path to the chosen side and kicks off `git rebase
// --continue`. The blocking modal returns until the next halt or
// completion.
func (m model) conflictResolveSide(side gitcmd.ConflictSide) (tea.Model, tea.Cmd) {
	paths := append([]string(nil), m.rebaseHalt.ConflictedPaths...)
	m.rebaseHalt = gitcmd.RebaseResult{}
	m.rebaseState = rebaseStateRunning
	ctx, cancel := context.WithCancel(context.Background())
	m.rebaseCancel = cancel
	return m, m.runRebaseContinueCmd(ctx, side, paths)
}

// runRebaseContinueCmd stages the conflict resolution then invokes
// RebaseContinue, posting the resulting RebaseResult back to Update as
// a rebaseDoneMsg. Same context-cancellation semantics as the initial
// rebase goroutine.
func (m model) runRebaseContinueCmd(ctx context.Context, side gitcmd.ConflictSide, paths []string) tea.Cmd {
	git := m.git
	return func() tea.Msg {
		if err := git.CheckoutSide(ctx, side, paths); err != nil {
			if ctx.Err() != nil {
				return rebaseDoneMsg{cancelled: true}
			}
			return rebaseDoneMsg{err: err}
		}
		res, err := git.RebaseContinue(ctx)
		if ctx.Err() != nil {
			return rebaseDoneMsg{cancelled: true}
		}
		return rebaseDoneMsg{result: res, err: err}
	}
}

// confirmRebase is called when the user presses enter on the action
// summary popup. It snapshots the current mark set, computes the
// post-success cursor anchor (closest unmarked SHA below the dropped
// range), transitions to the blocking modal, and launches the rebase
// goroutine.
func (m model) confirmRebase() (tea.Model, tea.Cmd) {
	snapshot := make([]string, 0, len(m.pendingActions))
	for sha := range m.pendingActions {
		snapshot = append(snapshot, sha)
	}
	m.rebaseSnapshot = snapshot
	// Anchor: starting from the selected index, walk increasing index
	// (older direction) until we find an unmarked SHA. That SHA is
	// older than the oldest drop and is therefore preserved by the
	// rebase, so the existing pendingRefreshSHA mechanism can land
	// the cursor onto it after the post-rebase log refresh.
	anchor := ""
	for i := m.selectedIdx; i < len(m.commits); i++ {
		if _, marked := m.pendingActions[m.commits[i].SHA]; !marked {
			anchor = m.commits[i].SHA
			break
		}
	}
	m.rebaseAnchorSHA = anchor
	m.rebaseState = rebaseStateRunning
	ctx, cancel := context.WithCancel(context.Background())
	m.rebaseCancel = cancel
	return m, m.runRebaseCmd(ctx, snapshot)
}

// runRebaseCmd returns a tea.Cmd that runs RebaseDropStart in a
// goroutine and posts the outcome as a rebaseDoneMsg. The context's
// Err is checked after the call so callers can distinguish a user
// cancellation (esc) from a generic rebase failure.
func (m model) runRebaseCmd(ctx context.Context, marked []string) tea.Cmd {
	git := m.git
	return func() tea.Msg {
		res, err := git.RebaseDropStart(ctx, marked)
		if ctx.Err() != nil {
			return rebaseDoneMsg{cancelled: true}
		}
		return rebaseDoneMsg{result: res, err: err}
	}
}

// handleRebaseDone routes the rebase outcome: success clears marks
// and refreshes the log with the cursor restored to the anchor SHA;
// failure / halt / cancellation runs `git rebase --abort` (idempotent),
// preserves marks, and surfaces an appropriate status-row message.
func (m model) handleRebaseDone(msg rebaseDoneMsg) (tea.Model, tea.Cmd) {
	if m.rebaseCancel != nil {
		m.rebaseCancel()
		m.rebaseCancel = nil
	}
	m.rebaseState = rebaseStateIdle
	m.rebaseHalt = gitcmd.RebaseResult{}
	m.rebaseManualUnmerged = nil

	// User cancellation via esc: always abort any mid-rebase state.
	if msg.cancelled {
		abortCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.git.RebaseAbort(abortCtx)
		return m, m.raiseStatus("Drop cancelled — repo unchanged")
	}
	// Unexpected gitcmd-level failure (e.g. cannot fork sed). Auto-
	// abort defensively and surface the underlying error.
	if msg.err != nil {
		abortCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.git.RebaseAbort(abortCtx)
		return m, m.raiseError(msg.err)
	}
	switch msg.result.State {
	case gitcmd.RebaseDone:
		// Success: clear the just-dropped marks, restore cursor onto
		// the anchor, refresh the log so the new HEAD is reflected.
		dropped := len(m.rebaseSnapshot)
		for _, sha := range m.rebaseSnapshot {
			delete(m.pendingActions, sha)
		}
		m.rebaseSnapshot = nil
		anchor := m.rebaseAnchorSHA
		m.rebaseAnchorSHA = ""
		ldr := m.ldr
		if ldr != nil {
			ldr.CancelDetail()
			ldr.CancelNumStat()
			ldr.CancelDiff()
		}
		m.pendingRefreshSHA = anchor
		m.pendingRefreshPath = ""
		m.ldr = loader.New(loader.Config{Source: m.git})
		m.commits = nil
		m.selectedIdx = 0
		m.viewportTop = 0
		m.loadedAll = false
		m.loadingMore = false
		m.detail = gitcmd.CommitDetail{}
		m.detailSHA = ""
		m.msgScroll = 0
		m.files = nil
		m.filesSHA = ""
		m.filesSelectedIdx = 0
		m.filesViewportTop = 0
		m.diff = diffrender.Result{}
		m.diffSHA = ""
		m.diffPath = ""
		m.diffScroll = 0
		m.diffHScroll = 0
		m.activeHunk = hunkstate.NoActiveHunk
		m.hunks = hunkstate.New()
		m.detailLoading = false
		m.filesLoading = false
		m.diffLoading = false
		m.resetBinarySize()
		flash := fmt.Sprintf("Dropped %d commit%s", dropped, plural2(dropped))
		return m, tea.Batch(m.loadLogCmd(0), m.raiseStatus(flash))
	case gitcmd.RebaseHalted:
		// Open the conflict popup with the halt info. The user picks a
		// resolution strategy ([a] abort, [t] theirs, [o] ours) which
		// either aborts the rebase or stages + continues it. Each halt
		// gets its own fresh popup — no sticky strategy across halts.
		m.rebaseHalt = msg.result
		m.rebaseState = rebaseStateConflict
		return m, nil
	default:
		abortCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.git.RebaseAbort(abortCtx)
		summary := summariseStderr(msg.result.Stderr)
		if summary == "" {
			summary = "unknown rebase error"
		}
		return m, m.raiseError(fmt.Errorf("Drop failed: %s", summary))
	}
}

// summariseStderr extracts the first non-blank non-hint line from git's
// stderr — typically the "fatal:" / "error:" line that explains the
// failure best.
func summariseStderr(s string) string {
	for _, line := range strings.Split(s, "\n") {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "hint:") {
			continue
		}
		return l
	}
	return ""
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
	m.activeHunk = hunkstate.NoActiveHunk
	m.hunks = hunkstate.New()

	m.detailLoading = false
	m.filesLoading = false
	m.diffLoading = false
	m.resetBinarySize()

	m.active = sectionLog
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

	m.middleTab = tabFiles

	// Pending action marks belong to a specific repository state. Switching
	// worktrees discards them entirely.
	m.pendingActions = map[string]ActionKind{}
	if m.rebaseCancel != nil {
		m.rebaseCancel()
		m.rebaseCancel = nil
	}
	m.rebaseState = rebaseStateIdle
	m.rebaseRefuseMsg = ""
	m.rebaseSnapshot = nil
	m.rebaseAnchorSHA = ""
	m.rebaseHalt = gitcmd.RebaseResult{}
	m.rebaseManualUnmerged = nil

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
	m.activeHunk = hunkstate.NoActiveHunk
	m.hunks = hunkstate.New()

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
		m.activeHunk = hunkstate.NoActiveHunk
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
	if m.rebaseState != rebaseStateIdle {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		delta := 1
		if msg.Button == tea.MouseButtonWheelUp {
			delta = -1
		}
		msgRect, msgVisible := messagePanelRect(lo, m.middleTab)
		if msgVisible && rectContains(msgRect, msg.X, msg.Y) {
			m.scrollMessage(delta)
			return m, nil
		}
		if rectContains(lo.Diff, msg.X, msg.Y) {
			m.scrollDiff(delta)
			return m, nil
		}
		return m, nil
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		if rectContains(lo.Log, msg.X, msg.Y) {
			row := msg.Y - lo.Log.Y - 1 // -1 for the title row
			if row < 0 {
				return m, nil
			}
			idx := m.viewportTop + row
			if idx < 0 || idx >= len(m.commits) {
				return m, nil
			}
			sectionChanged := m.active != sectionLog
			selectionChanged := idx != m.selectedIdx
			m.active = sectionLog
			m.selectedIdx = idx
			m.clampViewport()
			if selectionChanged || sectionChanged {
				return m, m.onSelectionChanged()
			}
			return m, nil
		}
		filesRect, filesVisible := filesPanelRect(lo, m.middleTab)
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
			m.active = sectionFiles
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
// occupies and whether it is visible. In full mode it always occupies
// the right column of the middle row. In small mode the middle row is
// a tabbed container; the message is on screen only when the active
// middle tab is tabMessage.
func messagePanelRect(lo layout.Layout, mt middleTab) (layout.Rect, bool) {
	if lo.SmallMode {
		if mt == tabMessage {
			return lo.Message, true
		}
		return layout.Rect{}, false
	}
	return lo.Message, true
}

// filesPanelRect returns the rectangle the files panel currently
// occupies and whether it is visible. In full mode it always occupies
// the bottom of the left column of the middle row. In small mode the
// middle row is tabbed; the files panel is on screen only when the
// active middle tab is tabFiles.
func filesPanelRect(lo layout.Layout, mt middleTab) (layout.Rect, bool) {
	if lo.SmallMode {
		if mt == tabFiles {
			return lo.Files, true
		}
		return layout.Rect{}, false
	}
	return lo.Files, true
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

// advanceActiveHunk steps the active hunk index by `dir` (with wrap),
// persists the new index per (sha, path), and scrolls the diff so the
// active hunk's first line is at the viewport top. No-op when the diff
// has zero hunks.
func (m *model) advanceActiveHunk(dir int) {
	total := len(m.diff.HunkStarts)
	if total == 0 {
		m.activeHunk = hunkstate.NoActiveHunk
		return
	}
	m.activeHunk = hunkstate.Advance(m.activeHunk, total, dir)
	if m.diffSHA != "" {
		m.hunks.Set(m.diffSHA, m.diffPath, m.activeHunk)
	}
	m.scrollToActiveHunk()
}

// scrollToActiveHunk scrolls the diff so the active hunk is framed in the
// viewport. When the hunk fits, the remaining viewport rows are split 30/70
// between context above and below the hunk (floored, with the rounding
// remainder falling into the "after" share). When the hunk is taller than
// the viewport, its first line is pinned to the top so the `@@` header
// stays visible. No-op when there is no active hunk.
func (m *model) scrollToActiveHunk() {
	first, last, ok := m.diff.HunkRange(m.activeHunk)
	if !ok {
		return
	}
	bodyH := m.diffPanelBodyHeight()
	hunkLen := last - first + 1
	if hunkLen >= bodyH {
		m.diffScroll = first
	} else {
		linesBefore := (bodyH - hunkLen) * 3 / 10
		m.diffScroll = first - linesBefore
	}
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
// title row) of the diff panel for the current terminal size. The diff
// panel is always rendered in both small and full mode.
func (m *model) diffPanelBodyHeight() int {
	if m.w == 0 || m.h == 0 {
		return 0
	}
	lo := layout.Compute(m.w, m.h)
	if lo.TooSmall {
		return 0
	}
	return lo.Diff.H - 1
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

// clampMsgScroll constrains msgScroll to [0, maxMsgScroll]. The
// message panel now renders only the commit body, so the max is the
// body line count minus the panel's visible body height.
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
// small mode the message panel shares the middle row with the files
// and metadata tabs; the panel is only rendered when the active
// middleTab is tabMessage, in which case the height matches the
// middle rect.
func (m *model) msgPanelBodyHeight() int {
	if m.w == 0 || m.h == 0 {
		return 0
	}
	lo := layout.Compute(m.w, m.h)
	if lo.TooSmall {
		return 0
	}
	if lo.SmallMode {
		if m.middleTab == tabMessage {
			return lo.Message.H - 1
		}
		return 0
	}
	return lo.Message.H - 1
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
	return lo.Log.H - 1 // minus title row
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
	return lo.Files.H - 1
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

	// droppedActionStyle renders the `D` letter in the leftmost action
	// column for commits marked for drop. droppedSubjectStyle is composed
	// over the subject text on the same rows.
	droppedActionStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	droppedSubjectStyle = lipgloss.NewStyle().Strikethrough(true).Faint(true)

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

// renderMiddleTabStrip renders the one-row tab strip shown at the top
// of the middle region in small mode. It lists the three tab labels
// (metadata, files, message) with the active one prefixed by "▸ " and
// styled with activeTitleStyle; the other two are prefixed by "  " and
// styled with inactiveTitleStyle, matching the per-panel title prefix
// convention. The strip is padded to width cells.
func renderMiddleTabStrip(active middleTab, width int) string {
	type entry struct {
		tab  middleTab
		name string
	}
	entries := []entry{
		{tabMetadata, "metadata"},
		{tabFiles, "files"},
		{tabMessage, "message"},
	}
	segs := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.tab == active {
			segs = append(segs, activeTitleStyle.Render("▸ "+e.name))
		} else {
			segs = append(segs, inactiveTitleStyle.Render("  "+e.name))
		}
	}
	s := strings.Join(segs, "  ")
	used := lipgloss.Width(s)
	if used >= width {
		raw := "▸ metadata    files    message"
		if width <= 0 {
			return ""
		}
		if len(raw) > width {
			raw = raw[:width]
		}
		return raw
	}
	return s + strings.Repeat(" ", width-used)
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
// highlight). hasPendingColumn reserves a leading 3-cell action column
// (1-cell letter + 2-space gap); marked applies strikethrough+dim to
// the subject portion of restStyled (the plain version is unchanged so
// row-level highlights remain solid).
func commitRowColumns(c gitcmd.Commit, width int, hasPendingColumn, marked bool) (short, date, author, restStyled, restPlain string, ok bool) {
	const shaW, dateW, authorW = 7, 14, 16
	const gap = "  "
	const actionColW = 3 // 1-cell letter + 2-space gap
	short = c.ShortSHA
	if len(short) > shaW {
		short = short[:shaW]
	} else if len(short) < shaW {
		short += strings.Repeat(" ", shaW-len(short))
	}
	date = padOrTruncate(c.RelDate, dateW)
	author = padOrTruncate(c.Author, authorW)
	fixedW := shaW + len(gap) + dateW + len(gap) + authorW + len(gap)
	if hasPendingColumn {
		fixedW += actionColW
	}
	restW := width - fixedW
	if restW < 1 {
		return short, "", "", "", "", false
	}

	refsStyled, refsPlain := fitRefs(visibleRefs(c.Refs), restW)
	rw := lipgloss.Width(refsPlain)
	if rw == 0 {
		subj := padOrTruncate(c.Subject, restW)
		styled := subj
		if marked {
			styled = droppedSubjectStyle.Render(subj)
		}
		return short, date, author, styled, subj, true
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
	subjStyled := subj
	if marked {
		subjStyled = droppedSubjectStyle.Render(subj)
	}
	restStyled = refsStyled + " " + subjStyled
	restPlain = refsPlain + " " + subj
	return short, date, author, restStyled, restPlain, true
}

// renderLogRow formats one commit row to exactly `width` cells with
// per-column foreground colors (used for non-selected rows). When
// hasPendingColumn is true the row reserves a leading 3-cell action
// column (1-cell letter + 2-space gap); marked toggles the action
// letter to a red-bold `D` and the subject to strikethrough+dim.
func renderLogRow(c gitcmd.Commit, width int, hasPendingColumn, marked bool) string {
	const gap = "  "
	short, date, author, restStyled, _, ok := commitRowColumns(c, width, hasPendingColumn, marked)
	if !ok {
		return padOrTruncate(short, width)
	}
	prefix := ""
	if hasPendingColumn {
		if marked {
			prefix = droppedActionStyle.Render("D") + gap
		} else {
			prefix = " " + gap
		}
	}
	return prefix +
		shortSHAStyle.Render(short) + gap +
		relDateStyle.Render(date) + gap +
		authorStyle.Render(author) + gap +
		restStyled
}

// renderLogRowPlain returns the row content as plain text (no color
// escapes), suitable for wrapping in a row-level highlight style.
// hasPendingColumn reserves the leading 3-cell action column; the
// action letter is emitted plain (the surrounding row-level highlight
// already wins visually on selected/match rows, per the slice spec).
func renderLogRowPlain(c gitcmd.Commit, width int, hasPendingColumn, marked bool) string {
	const gap = "  "
	short, date, author, _, restPlain, ok := commitRowColumns(c, width, hasPendingColumn, marked)
	if !ok {
		return padOrTruncate(short, width)
	}
	prefix := ""
	if hasPendingColumn {
		if marked {
			prefix = "D" + gap
		} else {
			prefix = " " + gap
		}
	}
	return prefix + short + gap + date + gap + author + gap + restPlain
}

// renderLogPanel renders the log panel, including its title and the
// visible window of commits. selectionActive indicates whether the
// selected row should get the bright vs dim highlight.
func renderLogPanel(m model, w, h int, active bool) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	title := renderTitle(logPanelTitle(m), w, active)
	bodyH := h - 1
	if bodyH <= 0 {
		return title
	}
	return title + "\n" + renderLogBodyWithScrollbar(m, w, bodyH, active)
}

// renderLogBodyWithScrollbar renders the log panel body, overlaying a
// vertical scrollbar column on the right when len(m.commits) overflows
// the viewport. When content fits, the body uses the full inner width.
func renderLogBodyWithScrollbar(m model, w, bodyH int, active bool) string {
	start, length, draw := layout.ScrollbarThumb(len(m.commits), bodyH, m.viewportTop, bodyH)
	if !draw || w < 2 {
		return renderLogBody(m, w, bodyH, active)
	}
	return appendScrollbarColumn(renderLogBody(m, w-1, bodyH, active), bodyH, start, length)
}

// renderLogBody renders the log panel's body rows (no title) at the
// given width and body height.
func renderLogBody(m model, w, bodyH int, active bool) string {
	if w <= 0 || bodyH <= 0 {
		return ""
	}
	lines := make([]string, 0, bodyH)
	if len(m.commits) == 0 {
		lines = append(lines, padOrTruncate("loading…", w))
		for i := 1; i < bodyH; i++ {
			lines = append(lines, strings.Repeat(" ", w))
		}
		return strings.Join(lines, "\n")
	}
	hasPendingColumn := len(m.pendingActions) > 0
	for row := 0; row < bodyH; row++ {
		idx := m.viewportTop + row
		if idx >= len(m.commits) {
			lines = append(lines, strings.Repeat(" ", w))
			continue
		}
		_, marked := m.pendingActions[m.commits[idx].SHA]
		var rendered string
		switch {
		case idx == m.selectedIdx:
			plain := renderLogRowPlain(m.commits[idx], w, hasPendingColumn, marked)
			if active {
				rendered = activeSelectedRowStyle.Render(plain)
			} else {
				rendered = inactiveSelectedRowStyle.Render(plain)
			}
		case m.isSearchMatch(idx):
			plain := renderLogRowPlain(m.commits[idx], w, hasPendingColumn, marked)
			rendered = searchMatchRowStyle.Render(plain)
		default:
			rendered = renderLogRow(m.commits[idx], w, hasPendingColumn, marked)
		}
		lines = append(lines, rendered)
	}
	return strings.Join(lines, "\n")
}

// appendScrollbarColumn appends a single-cell scrollbar column to each
// of the bodyH rows in body. Rows in [start, start+length) get a thumb
// cell ("█"); the rest get a space. The body is expected to be exactly
// bodyH newline-separated rows.
func appendScrollbarColumn(body string, bodyH, start, length int) string {
	lines := strings.Split(body, "\n")
	for len(lines) < bodyH {
		lines = append(lines, "")
	}
	if len(lines) > bodyH {
		lines = lines[:bodyH]
	}
	for i := range lines {
		cell := " "
		if i >= start && i < start+length {
			cell = "█"
		}
		lines[i] = lines[i] + cell
	}
	return strings.Join(lines, "\n")
}

// messageLines returns the commit message body split into lines. The
// dedicated metadata panel renders the sha/author/tags header, so the
// message panel is now a pure body viewer.
func messageLines(d gitcmd.CommitDetail) []string {
	if d.SHA == "" || d.Body == "" {
		return nil
	}
	return strings.Split(d.Body, "\n")
}

// messageLineCount returns the total renderable line count for the
// message panel (without considering wrap or visible window height).
func messageLineCount(d gitcmd.CommitDetail) int { return len(messageLines(d)) }

var (
	msgLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	msgSHAStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
)

// styleMessageLine applies dim coloring to the Tags: label, accent
// coloring to the short sha on the first metadata line, and per-kind
// coloring to the refs in parens that follow it. line is the
// (already padded/truncated) plain string; shortSHA identifies the
// first metadata line.
func styleMessageLine(line, shortSHA string) string {
	if shortSHA != "" && (line == shortSHA || strings.HasPrefix(line, shortSHA+" ") || strings.HasPrefix(line, shortSHA+"…")) {
		return styleSHARefsLine(line, shortSHA)
	}
	if strings.HasPrefix(line, "Tags:") {
		end := len("Tags:")
		for end < len(line) && line[end] == ' ' {
			end++
		}
		return msgLabelStyle.Render(line[:end]) + line[end:]
	}
	return line
}

// styleSHARefsLine colors the leading short sha and the parenthesised
// refs that may follow it. Operates on the padded/truncated plain
// string, so missing closing ')' (from truncation) is tolerated.
func styleSHARefsLine(line, shortSHA string) string {
	rest := strings.TrimPrefix(line, shortSHA)
	out := msgSHAStyle.Render(shortSHA)
	// Locate the opening paren that begins the refs block, if any.
	open := strings.Index(rest, "(")
	if open == -1 {
		return out + rest
	}
	out += rest[:open]
	inside := rest[open+1:]
	// Find a closing ')' in what remains; absent when truncated.
	closeIdx := strings.LastIndex(inside, ")")
	var refsStr, suffix string
	if closeIdx == -1 {
		refsStr = inside
		suffix = ""
	} else {
		refsStr = inside[:closeIdx]
		suffix = inside[closeIdx:]
	}
	out += refParenStyle.Render("(")
	for i, name := range strings.Split(refsStr, ", ") {
		if i > 0 {
			out += refParenStyle.Render(", ")
		}
		var style lipgloss.Style
		switch gitcmd.ClassifyRef(name) {
		case gitcmd.RefRemote:
			style = refRemoteStyle
		case gitcmd.RefHEAD:
			style = refHEADStyle
		default:
			style = refLocalStyle
		}
		out += style.Render(name)
	}
	if suffix != "" {
		out += refParenStyle.Render(")") + suffix[1:]
	}
	return out
}

// renderMessagePanel renders the message panel using the model's
// currently-loaded detail and msgScroll offset. The panel is a pure
// body viewer — the commit's identifying metadata lives in the
// dedicated metadata panel and is not duplicated here. When stale is
// true the body is rendered with a single dim color (the spinner in
// the title is the active loading affordance).
func renderMessagePanel(m model, w, h int, active, stale bool) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	title := renderTitleWithSpinner("message", w, active, stale, m.spinnerFrame)
	bodyH := h - 1
	if bodyH <= 0 {
		return title
	}
	return title + "\n" + renderMessageBodyWithScrollbar(m, w, bodyH, stale)
}

// renderMessageBodyWithScrollbar renders the message panel body and
// overlays a vertical scrollbar column on the right when the body
// overflows the viewport.
func renderMessageBodyWithScrollbar(m model, w, bodyH int, stale bool) string {
	start, length, draw := layout.ScrollbarThumb(messageLineCount(m.detail), bodyH, m.msgScroll, bodyH)
	if !draw || w < 2 {
		return renderMessageBody(m, w, bodyH, stale)
	}
	return appendScrollbarColumn(renderMessageBody(m, w-1, bodyH, stale), bodyH, start, length)
}

// renderMessageBody renders the message panel's body rows (no title)
// at the given width and body height. Used directly by the small-mode
// tab strip path, where the tab strip replaces the per-panel title.
func renderMessageBody(m model, w, bodyH int, stale bool) string {
	if w <= 0 || bodyH <= 0 {
		return ""
	}
	lines := make([]string, 0, bodyH)
	body := messageLines(m.detail)
	if m.detail.SHA == "" {
		lines = append(lines, padOrTruncate("loading…", w))
		for i := 1; i < bodyH; i++ {
			lines = append(lines, strings.Repeat(" ", w))
		}
		return strings.Join(lines, "\n")
	}
	start := m.msgScroll
	if max := len(body) - bodyH; start > max {
		start = max
	}
	if start < 0 {
		start = 0
	}
	for row := 0; row < bodyH; row++ {
		idx := start + row
		if idx >= len(body) {
			lines = append(lines, strings.Repeat(" ", w))
			continue
		}
		truncated := padOrTruncate(body[idx], w)
		if stale {
			lines = append(lines, staleStyle.Render(truncated))
		} else {
			lines = append(lines, truncated)
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

// renderMetadataPanel renders the dedicated metadata panel that sits
// above the files panel in the middle row's left column. The panel has
// no title row of its own; its h rows (typically MetadataContentRows)
// are filled with the three strings returned by metadata.Summary —
// short sha + refs, author + date, tags summary — each padded or
// truncated to w cells.
//
// When the commit detail has not yet loaded (m.detail.SHA == ""), the
// three summary rows are empty strings, so the panel renders as h
// blank rows of width w. The row count is taken from h rather than
// pinned at MetadataContentRows so the renderer remains valid if a
// caller (e.g. small-mode tab body) supplies a different height.
func renderMetadataPanel(m model, w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	rows := metadata.Summary(m.detail, time.Local)
	lines := make([]string, 0, h)
	for i := 0; i < h; i++ {
		var raw string
		if i < len(rows) {
			raw = rows[i]
		}
		padded := padOrTruncate(raw, w)
		lines = append(lines, styleMessageLine(padded, m.detail.ShortSHA))
	}
	return strings.Join(lines, "\n")
}

// renderFilesPanel renders the bottom-left files panel for the
// currently-loaded numstat. When stale is true the rows are rendered
// in a single dim color.
func renderFilesPanel(m model, w, h int, active, stale bool) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	title := renderTitleWithSpinner("files", w, active, stale, m.spinnerFrame)
	bodyH := h - 1
	if bodyH <= 0 {
		return title
	}
	return title + "\n" + renderFilesBodyWithScrollbar(m, w, bodyH, active, stale)
}

// renderFilesBodyWithScrollbar renders the files panel body and overlays
// a vertical scrollbar column on the right when len(m.files) overflows
// the viewport.
func renderFilesBodyWithScrollbar(m model, w, bodyH int, active, stale bool) string {
	start, length, draw := layout.ScrollbarThumb(len(m.files), bodyH, m.filesViewportTop, bodyH)
	if !draw || w < 2 {
		return renderFilesBody(m, w, bodyH, active, stale)
	}
	return appendScrollbarColumn(renderFilesBody(m, w-1, bodyH, active, stale), bodyH, start, length)
}

// renderFilesBody renders the files panel's body rows (no title) at
// the given width and body height. Used directly by the small-mode
// tab strip path, where the tab strip replaces the per-panel title.
func renderFilesBody(m model, w, bodyH int, active, stale bool) string {
	if w <= 0 || bodyH <= 0 {
		return ""
	}
	lines := make([]string, 0, bodyH)
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
		for i := 1; i < bodyH; i++ {
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
	title := renderTitleWithSpinner("diff", w, active, stale, m.spinnerFrame)
	bodyH := h - 1
	if bodyH <= 0 {
		return title
	}
	return title + "\n" + renderDiffBodyWithScrollbar(m, w, bodyH, stale)
}

// renderDiffBodyWithScrollbar renders the diff panel body. When a
// placeholder is in effect (no diff loaded, binary, etc.) and there is
// no stale prior content to display, the body is rendered without a
// scrollbar. Otherwise a vertical scrollbar column is overlaid on the
// right when the diff content overflows the viewport.
func renderDiffBodyWithScrollbar(m model, w, bodyH int, stale bool) string {
	placeholder, ok := diffPlaceholder(m)
	showStale := stale && len(m.diff.Lines) > 0
	if ok && !showStale {
		return renderDiffPlaceholderBody(w, bodyH, placeholder)
	}
	start, length, draw := layout.ScrollbarThumb(len(m.diff.Lines), bodyH, m.diffScroll, bodyH)
	if !draw || w < 2 {
		return renderDiffBody(m, w, bodyH, stale)
	}
	return appendScrollbarColumn(renderDiffBody(m, w-1, bodyH, stale), bodyH, start, length)
}

// renderDiffPlaceholderBody renders bodyH rows: the placeholder on the
// first row, blanks below. Used when no diff content is available.
func renderDiffPlaceholderBody(w, bodyH int, placeholder string) string {
	if w <= 0 || bodyH <= 0 {
		return ""
	}
	lines := make([]string, 0, bodyH)
	lines = append(lines, padOrTruncate(placeholder, w))
	for i := 1; i < bodyH; i++ {
		lines = append(lines, strings.Repeat(" ", w))
	}
	return strings.Join(lines, "\n")
}

// renderDiffBody renders the diff body rows (no title) at the given
// width and body height, using m.diffScroll as the top-of-viewport row
// and m.diffHScroll as the horizontal offset.
func renderDiffBody(m model, w, bodyH int, stale bool) string {
	if w <= 0 || bodyH <= 0 {
		return ""
	}
	lines := make([]string, 0, bodyH)
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
			lines = append(lines, m.diff.FormatLineActive(idx, w, m.diffHScroll, m.activeHunk))
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

	logActive := m.active == sectionLog
	filesActive := m.active == sectionFiles
	msgActive := m.active == sectionMessage
	diffActive := m.active == sectionDiff

	logPanel := renderLogPanel(m, lo.Log.W, lo.Log.H, logActive)
	var middleRow string
	if lo.SmallMode {
		// The middle region is a tabbed container — row 0 is the tab
		// strip showing the three labels (metadata / files / message),
		// and the remaining rows render the active tab's body. The
		// strip replaces the per-panel title row, so the body is
		// rendered without its own title.
		w := lo.Metadata.W
		bodyH := lo.Metadata.H - 1
		strip := renderMiddleTabStrip(m.middleTab, w)
		var body string
		switch m.middleTab {
		case tabMetadata:
			body = renderMetadataPanel(m, w, bodyH)
		case tabMessage:
			body = renderMessageBodyWithScrollbar(m, w, bodyH, m.detailLoading)
		default:
			body = renderFilesBodyWithScrollbar(m, w, bodyH, filesActive, m.filesLoading)
		}
		if bodyH > 0 {
			middleRow = strip + "\n" + body
		} else {
			middleRow = strip
		}
	} else {
		// Left column: dedicated metadata panel (sha, author/date, tags
		// summary — 3 rows, no header) above the files panel.
		metadataPanel := renderMetadataPanel(m, lo.Metadata.W, lo.Metadata.H)
		filesPanel := renderFilesPanel(m, lo.Files.W, lo.Files.H, filesActive, m.filesLoading)
		leftCol := lipgloss.JoinVertical(lipgloss.Left, metadataPanel, filesPanel)
		rightCol := renderMessagePanel(m, lo.Message.W, lo.Message.H, msgActive, m.detailLoading)
		// 1-column vertical gap between the left column and the message panel.
		hGap := strings.Repeat(" \n", lo.Message.H-1) + " "
		middleRow = lipgloss.JoinHorizontal(lipgloss.Top, leftCol, hGap, rightCol)
	}
	diffPanel := renderDiffPanel(m, lo.Diff.W, lo.Diff.H, diffActive, m.diffLoading)
	vGap := strings.Repeat(" ", m.w)
	base := strings.Join([]string{logPanel, vGap, middleRow, vGap, diffPanel, m.renderStatus(lo.Status.W)}, "\n")

	if m.helpModalOpen {
		return overlayCentered(base, renderHelpModal(), m.w, m.h)
	}
	if m.wtModalOpen {
		return overlayCentered(base, renderWorktreeModal(m), m.w, m.h)
	}
	switch m.rebaseState {
	case rebaseStateSummary:
		return overlayCentered(base, renderRebaseSummaryPopup(m), m.w, m.h)
	case rebaseStateRunning:
		return overlayCentered(base, renderRebaseRunningPopup(), m.w, m.h)
	case rebaseStateRefuse:
		return overlayCentered(base, renderRebaseRefusePopup(m), m.w, m.h)
	case rebaseStateConflict:
		return overlayCentered(base, renderRebaseConflictPopup(m), m.w, m.h)
	case rebaseStateManualWait:
		return overlayCentered(base, renderRebaseManualWaitPopup(m), m.w, m.h)
	}
	return base
}

// renderRebaseSummaryPopup renders the action-summary popup shown after
// ctrl+s passes all preconditions. Lists the planned actions (today
// only "Drop N commits") and the Confirm / Cancel hint.
func renderRebaseSummaryPopup(m model) string {
	drops := 0
	for _, k := range m.pendingActions {
		if k == ActionDrop {
			drops++
		}
	}
	title := modalTitleStyle.Render("Pending actions")
	action := fmt.Sprintf("Drop %d commit%s", drops, plural2(drops))
	hint := modalHintStyle.Render("enter confirm · esc cancel")

	rows := []string{title, "", "  " + action, "", hint}
	return padAndBorderModal(rows)
}

// renderRebaseRunningPopup is the blocking modal shown while a rebase
// is in flight. Static text per slice 02 — live progress is out of
// scope and may be added later.
func renderRebaseRunningPopup() string {
	title := modalTitleStyle.Render("Rebasing…")
	hint := modalHintStyle.Render("esc cancel")
	rows := []string{title, "", hint}
	return padAndBorderModal(rows)
}

// renderRebaseConflictPopup renders the conflict popup shown when the
// rebase halts mid-flight. The header names the halt commit; the body
// lists conflicted paths; the footer offers the four resolution
// shortcuts ([a] abort, [t] theirs, [o] ours, [m] resolve manually).
func renderRebaseConflictPopup(m model) string {
	title := modalTitleStyle.Render("Conflict")
	header := "Conflict at"
	if sha := m.rebaseHalt.HaltSHA; sha != "" {
		short := sha
		if len(short) > 8 {
			short = short[:8]
		}
		header += " " + short
		if subj := m.rebaseHalt.HaltSubject; subj != "" {
			header += " " + subj
		}
	}
	rows := []string{title, "", header}
	if len(m.rebaseHalt.ConflictedPaths) > 0 {
		rows = append(rows, "")
		for _, p := range m.rebaseHalt.ConflictedPaths {
			rows = append(rows, "  "+p)
		}
	}
	rows = append(rows, "")
	rows = append(rows, modalHintStyle.Render("[a] abort  [t] accept theirs  [o] accept ours  [m] resolve manually"))
	return padAndBorderModal(rows)
}

// renderRebaseManualWaitPopup renders the manual-resolve waiting state.
// The user fixes the conflict in another terminal and presses enter to
// confirm; if any unmerged paths remain at OK time, they are listed
// here so the user knows exactly what is still pending.
func renderRebaseManualWaitPopup(m model) string {
	title := modalTitleStyle.Render("Resolve manually")
	rows := []string{title, ""}
	rows = append(rows, "Resolve the conflict in another terminal,")
	rows = append(rows, "then press enter to continue the rebase.")
	if len(m.rebaseManualUnmerged) > 0 {
		rows = append(rows, "")
		rows = append(rows, "Still unmerged:")
		for _, p := range m.rebaseManualUnmerged {
			rows = append(rows, "  "+p)
		}
	}
	rows = append(rows, "")
	rows = append(rows, modalHintStyle.Render("enter OK · esc back"))
	return padAndBorderModal(rows)
}

// renderRebaseRefusePopup renders the precondition-refusal popup with
// the failure reason and a Close affordance only — there is no Confirm
// option since the user must address the precondition outside the TUI.
func renderRebaseRefusePopup(m model) string {
	title := modalTitleStyle.Render("Cannot save")
	hint := modalHintStyle.Render("esc · enter · q close")
	rows := []string{title, "", m.rebaseRefuseMsg, "", hint}
	return padAndBorderModal(rows)
}

// padAndBorderModal pads each row to the widest row's width and wraps
// the block in the shared modal border.
func padAndBorderModal(rows []string) string {
	contentW := 0
	for _, r := range rows {
		if w := lipgloss.Width(r); w > contentW {
			contentW = w
		}
	}
	for i, r := range rows {
		pad := contentW - lipgloss.Width(r)
		if pad > 0 {
			rows[i] = r + strings.Repeat(" ", pad)
		}
	}
	return modalBorderStyle.Render(strings.Join(rows, "\n"))
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
			{"enter", "activate bottom section (small mode: swap top row to message)"},
			{"w", "switch worktree"},
			{"ctrl+r", "refresh (re-read log, drop caches)"},
		}},
		{title: "Commits (top)", bindings: []binding{
			{"ctrl+j / ctrl+k", "move selection"},
			{"/", "fuzzy search sha / subject / author"},
			{"n / N", "next / previous search match"},
			{"ctrl+d", "mark/unmark commit for drop"},
			{"ctrl+s", "save pending actions"},
		}},
		{title: "Files (bottom)", bindings: []binding{
			{"ctrl+j / ctrl+k", "scroll diff down / up one line"},
			{"/", "fuzzy search file paths"},
			{"n / N", "next / previous search match"},
		}},
		{title: "Message / diff (right panels)", bindings: []binding{
			{"j / k", "line scroll"},
			{"d / u", "page scroll"},
			{"gg / G", "jump to top / bottom"},
			{"ctrl+d / ctrl+u", "next / previous hunk (diff); ctrl+u pages message"},
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

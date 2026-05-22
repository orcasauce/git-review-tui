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
	"github.com/orcasauce/git-review-tui/filefilter"
	"github.com/orcasauce/git-review-tui/fileid"
	"github.com/orcasauce/git-review-tui/filelist"
	"github.com/orcasauce/git-review-tui/gitcmd"
	"github.com/orcasauce/git-review-tui/hunkpatch"
	"github.com/orcasauce/git-review-tui/hunkstate"
	"github.com/orcasauce/git-review-tui/layout"
	"github.com/orcasauce/git-review-tui/loader"
	"github.com/orcasauce/git-review-tui/metadata"
	"github.com/orcasauce/git-review-tui/revertstate"
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
	// diffScroll is the row offset into the diff panel's visible-row
	// view (rows hidden by a flagged hunk's Add-collapse don't count)
	// and diffHScroll is the horizontal character offset.
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
	// reverts tracks revert marks on individual hunks across the
	// session. `d` while the diff panel is focused toggles a mark on
	// the active hunk; rendering surfaces flagged hunks as a grey
	// "tombstone" block in the diff (prd-hunk-revert.md, slice 07).
	reverts *revertstate.Tracker
	// fileIDs is the stable-identity registry that translates raw paths
	// into FileIDs. Marks in revertstate are keyed by FileID so a file
	// can be followed across renames and so the post-rebase adoption
	// walk can match a moved hunk by (FileID, HunkHash). Seeded from
	// rename information on each NumStatResult.
	fileIDs *fileid.Registry
	// diffHunks is the default-context (non-`-U99999`) diff for the
	// currently-loaded (diffSHA, diffPath). It is cached here so that
	// toggleRevertMark can extract the active hunk's text and compute
	// its canonical content hash without re-running git.
	diffHunks string
	// hunkTotals caches the total hunk count per (sha, path) for files
	// the user has visited. Keyed by sha + "\x00" + path. Populated on
	// each DiffResult and consumed by slice-08 display logic to decide
	// whether a file is fully flagged and a commit auto-promotes to D.
	hunkTotals map[string]int
	// filesByCommit caches the file list per commit sha, populated on
	// each FilesResult. Used alongside hunkTotals to compute the
	// commit-wide total hunk count for the auto-promote-to-D rule.
	filesByCommit map[string][]gitcmd.FileStat
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

	// File-list filter state. The file filter hides non-matching files
	// entirely. `fileFilterExpr` is the committed expression; while the
	// prompt is open `fileFilterPromptInput` is what the user is typing
	// and `fileFilterPromptExpr` is its live-parsed Expr, used to render
	// the file list before Enter commits the change. The visible-list
	// indices are cached in `fileFilterVisible` (rebuilt when the files
	// load or the active expression changes); both `filesSelectedIdx`
	// and `filesViewportTop` are positions inside this visible list.
	//
	// `fileFilterPromptOrigIdx` / `fileFilterPromptOrigTop` snapshot the
	// pre-prompt visible-list selection so `esc` can restore it. The
	// filter persists across commit selection and `ctrl+r` refresh.
	fileFilterPromptActive  bool
	fileFilterPromptInput   string
	fileFilterPromptExpr    filefilter.Expr
	fileFilterPromptOrigIdx int
	fileFilterPromptOrigTop int
	fileFilterExpr          filefilter.Expr
	fileFilterVisible       []int

	// Per-position regex debounce cache used while the filter prompt is
	// open. fileFilterLastValid[i] holds the most recent valid Token at
	// position i, and fileFilterInvalidSince[i] records when position i
	// transitioned from valid to invalid (zero when currently valid or
	// never valid). Together they implement stories 17–18: a regex that
	// becomes invalid keeps applying for `fileFilterDebounce` (500ms),
	// and a regex that has never been valid contributes nothing. Cleared
	// on prompt open / close / commit so each editing session starts fresh.
	fileFilterLastValid    []*filefilter.Token
	fileFilterInvalidSince []time.Time

	// commitFilterMatch maps commit SHA → whether at least one of that
	// commit's files matches the active file filter. Absence means the
	// commit has not been evaluated (numstat not yet loaded). Computed on
	// filter submit from the loader's cached numstats, and updated as new
	// NumStatResults arrive so commits dim progressively as the user
	// pages through the log (PRD stories 25–27). Cleared on filter
	// clear, ctrl+r refresh, and worktree switch.
	commitFilterMatch map[string]bool

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

	// rebaseFlow identifies which kind of ctrl+s pipeline is currently
	// staged or in flight: a drop-only rebase (existing behaviour) or
	// the cursor-commit revert rebase introduced by slice 10. The field
	// is set by startSave once the routing decision has been made and
	// drives confirmRebase, the running popup, and the done handler.
	rebaseFlow rebaseFlowKind
	// rebaseRevertSnapshot holds the pre-confirm revertstate snapshot.
	// On any cancel/error path the tracker is restored to this state so
	// the user does not lose flagged hunks they had not yet processed.
	rebaseRevertSnapshot revertstate.Snapshot
	// rebaseRevertCursorSHA is the cursor commit whose revert marks are
	// being processed by the active rebase.
	rebaseRevertCursorSHA string
	// rebaseRevertPatches is the per-file reverse patch map keyed by the
	// path at the cursor commit. Built synchronously at startSave so any
	// DiffHunks failure short-circuits before the rebase begins.
	rebaseRevertPatches map[string]string
	// rebaseRevertCount is the total hunk count being reverted. Used
	// purely for the success status message.
	rebaseRevertCount int
	// rebaseRevertDrops carries the drop-SHA list co-processed with the
	// cursor commit's reverts when ctrl-s combined both kinds of marks.
	// Drops are not removed from pendingActions until the rebase succeeds,
	// so cancel paths leave the user's marks in place.
	rebaseRevertDrops []string
	// rebaseRevertAutoPromote signals that every hunk on the cursor commit
	// is flagged, so the cursor's todo entry becomes `drop` rather than
	// `edit` and the apply / amend steps are skipped. The end state is
	// equivalent to an empty amend + --empty=drop, but cleaner.
	rebaseRevertAutoPromote bool
	// rebaseRevertAtApply tracks whether the conflict popup currently
	// up is for an apply-step conflict (ApplyReverse3Way left unmerged
	// paths at the edit halt) versus a cascade conflict (a downstream
	// commit halted after the cursor was amended and continued). The
	// distinction drives the resume path: apply-step resolutions still
	// need AmendNoEdit to fold the resolved tree into the cursor
	// commit; cascade resolutions go straight to RebaseContinue.
	rebaseRevertAtApply bool

	// adoptionTable carries the (FileID, HunkHash) → count multiset of
	// pre-rebase revert marks queued for re-attachment onto the rewritten
	// history. Built in handleRevertDone success after cursor + drop
	// marks are cleared, consumed by the post-refresh walk in
	// handleAdoptionDone, cleared on the same. Survives nothing — every
	// exit path (success, cancel, worktree switch) zeroes it.
	adoptionTable map[revertstate.AdoptionKey]int
	// adoptionTotal is the count of marks staged for adoption when the
	// table was built. The discard count surfaced post-walk is
	// adoptionTotal minus the number of marks that found a home.
	adoptionTotal int
	// adoptionWanted is the set of hashes referenced by adoptionTable.
	// The walk goroutine uses it to discard non-matching hunks at
	// hash-time, keeping the candidate stream proportional to the
	// flagged set rather than to the entire log.
	adoptionWanted map[string]struct{}
}

// rebaseFlowKind distinguishes the drop-only pipeline (existing
// RebaseDropStart) from the unified revert pipeline (RebaseEditStart),
// which now also handles combined drop + cursor-commit revert and the
// auto-promote case where every hunk on the cursor commit is flagged.
type rebaseFlowKind int

const (
	rebaseFlowNone rebaseFlowKind = iota
	rebaseFlowDrop
	rebaseFlowRevert
)

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
		reverts:        revertstate.New(),
		fileIDs:        fileid.New(),
		hunkTotals:     map[string]int{},
		filesByCommit:  map[string][]gitcmd.FileStat{},
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

// revertDoneMsg delivers the outcome of the runRebaseRevertCmd
// goroutine (and its resume sibling). A halted outcome routes through
// the conflict popup; cancel/err go through the abort-and-restore
// path; done is the success case.
//
// cancelled = esc on the blocking modal.
// err = gitcmd-level failure (start, apply, amend, continue).
// halted = either ApplyReverse3Way produced unmerged paths or
//   RebaseContinue returned RebaseHalted on a downstream commit.
// atApply = halted at the apply step (true) vs. at a cascade
//   replay step from RebaseContinue (false). Drives whether the
//   resume path runs AmendNoEdit before RebaseContinue.
// midRebase = a mid-rebase state needs `git rebase --abort` cleanup.
// done = the whole pipeline ran to RebaseDone.
type revertDoneMsg struct {
	cancelled  bool
	err        error
	halted     bool
	atApply    bool
	midRebase  bool
	done       bool
	conflicted []string
	haltSHA    string
	haltSubj   string
	stderr     string
}

// adoptionCandidate is one hunk in the new history whose canonical
// hash matched a hash in the adoption table. The main thread resolves
// path → FileID and calls revertstate.Adopt to perform the actual
// re-attachment (the goroutine has no safe access to the registry).
type adoptionCandidate struct {
	path string
	idx  int
	hash string
}

// adoptionCommitData groups one new-history commit's rename events with
// the candidates extracted from its diff. Rename events are replayed
// onto the fileid registry on the main thread immediately before that
// commit's candidates are processed, so the FileID lookup matches the
// state the registry would have been in during a normal walk.
type adoptionCommitData struct {
	sha        string
	files      []gitcmd.FileStat
	candidates []adoptionCandidate
}

// adoptionDoneMsg delivers the post-rebase walk's output. The main
// thread iterates commits oldest → newest, seeds the registry per
// commit, and adopts each candidate against the saved adoption table.
type adoptionDoneMsg struct {
	commits []adoptionCommitData
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
		// On first load, kick off the detail + numstat fetches for the
		// initial selection so the message and files panels populate
		// without requiring any keystrokes.
		if firstLoad && len(m.commits) > 0 {
			sha := m.commits[m.selectedIdx].SHA
			m.detailLoading = true
			m.filesLoading = true
			cmds := []tea.Cmd{m.ldr.LoadDetail(sha), m.ldr.LoadNumStat(sha), m.startSpinnerCmd()}
			if pref := m.nextPrefetchCmd(); pref != nil {
				cmds = append(cmds, pref)
			}
			// Post-rebase adoption walk: after handleRevertDone parked
			// the (FileID, HunkHash) multiset on the model, the first
			// log refresh that arrives with non-empty commits is the
			// trigger to re-attach those marks against the new history.
			if cmd := m.maybeStartAdoptionWalk(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			return m, tea.Batch(cmds...)
		}
		// A first load whose commit list came back empty (every commit
		// was dropped) still needs to flush adoption state — every
		// queued mark counts as a discard. Equivalent to running the
		// walk with no candidates.
		if firstLoad && len(m.commits) == 0 && m.adoptionTable != nil {
			return m, m.flushAdoptionAsDiscards()
		}
		// Newly arrived commits (pagination) need dim-state population too.
		if pref := m.nextPrefetchCmd(); pref != nil {
			return m, pref
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
			if msg.Prefetch {
				// Background prefetch failures are ignored: the user has
				// no action to take and the chain should keep going so
				// later commits still get their dim state evaluated.
				return m, m.nextPrefetchCmd()
			}
			m.filesLoading = false
			return m, m.raiseError(msg.Err)
		}
		// Update commit-row dim state for any incoming numstat while a
		// filter is active. This is how commits dim progressively as the
		// user navigates the log (PRD story 27): when the user selects
		// a commit not previously evaluated, its numstat arrives here
		// and is recorded in the dim map.
		if !m.fileFilterExpr.IsEmpty() {
			if m.commitFilterMatch == nil {
				m.commitFilterMatch = make(map[string]bool)
			}
			m.commitFilterMatch[msg.SHA] = commitMatchesFileFilter(msg.Files, m.fileFilterExpr)
		}
		m.seedFileIDs(msg.Files)
		if msg.Prefetch {
			// Prefetch results only populate the dim map; they never
			// drive the files pane. Chain the next prefetch.
			return m, m.nextPrefetchCmd()
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
		if m.filesByCommit == nil {
			m.filesByCommit = map[string][]gitcmd.FileStat{}
		}
		m.filesByCommit[msg.SHA] = msg.Files
		// The file filter persists across commit selection and refresh,
		// so the active Expr applies immediately to the new file set.
		m.recomputeVisibleFiles(-1)
		// Restore a refresh-preserved file selection by path if it still
		// exists and is visible under the active filter. Consumed once.
		if m.pendingRefreshPath != "" {
			for visIdx, origIdx := range m.fileFilterVisible {
				if m.files[origIdx].Path == m.pendingRefreshPath {
					m.filesSelectedIdx = visIdx
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
		m.diffHunks = msg.Hunks
		m.diffScroll = 0
		m.diffHScroll = 0
		m.diffLoading = false
		fid := m.fileIDs.Resolve(msg.Path)
		m.diff.SetFlaggedHunks(m.reverts.MarksForFile(msg.SHA, fid))
		if m.hunkTotals == nil {
			m.hunkTotals = map[string]int{}
		}
		m.hunkTotals[hunkTotalsKey(msg.SHA, msg.Path)] = len(m.diff.HunkStarts)
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

	case fileFilterDebounceMsg:
		// Debounce timer fired: re-run the live parse so any expired
		// prior-valid regex is dropped. No-op when the prompt is no
		// longer open (commit/cancel happened before the tick fired).
		if !m.fileFilterPromptActive {
			return m, nil
		}
		cmd := m.reparseFileFilterPrompt()
		return m, cmd

	case rebaseDoneMsg:
		return m.handleRebaseDone(msg)

	case revertDoneMsg:
		return m.handleRevertDone(msg)

	case adoptionDoneMsg:
		return m.handleAdoptionDone(msg)

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
		if m.fileFilterPromptActive {
			return m.updateFileFilterPrompt(msg)
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
			m.openFileFilterPrompt()
			return m, nil
		case "n":
			if m.fileListSection() {
				m.advanceActiveHunk(1)
				return m, nil
			}
		case "N":
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
				if m.advanceSelectedSkippingDimmed(1) {
					m.clampViewport()
					return m, m.onSelectionChanged()
				}
				return m, nil
			}
			if m.active == sectionFiles {
				m.scrollDiff(1)
				return m, nil
			}
		case "ctrl+k":
			if m.commitListSection() && len(m.commits) > 0 {
				if m.advanceSelectedSkippingDimmed(-1) {
					m.clampViewport()
					return m, m.onSelectionChanged()
				}
				return m, nil
			}
			if m.active == sectionFiles {
				m.scrollDiff(-1)
				return m, nil
			}
		case "j":
			switch m.active {
			case sectionLog:
				if m.advanceSelectedSkippingDimmed(1) {
					m.clampViewport()
					return m, m.onSelectionChanged()
				}
				return m, nil
			case sectionFiles:
				if len(m.fileFilterVisible) > 0 && m.filesSelectedIdx < len(m.fileFilterVisible)-1 {
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
				if m.advanceSelectedSkippingDimmed(-1) {
					m.clampViewport()
					return m, m.onSelectionChanged()
				}
				return m, nil
			case sectionFiles:
				if len(m.fileFilterVisible) > 0 && m.filesSelectedIdx > 0 {
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
				return m, m.toggleRevertMarkFromFiles()
			case sectionMessage:
				m.pageMessage(1)
				return m, nil
			case sectionDiff:
				return m, m.toggleRevertMark()
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
	if len(m.fileFilterVisible) == 0 {
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
	if target > len(m.fileFilterVisible)-1 {
		target = len(m.fileFilterVisible) - 1
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

// jumpFileTop jumps the file selection to the first visible file.
func (m *model) jumpFileTop() tea.Cmd {
	if len(m.fileFilterVisible) == 0 || m.filesSelectedIdx == 0 {
		return nil
	}
	m.filesSelectedIdx = 0
	m.clampFilesViewport()
	return m.onFileSelectionChanged()
}

// jumpFileBottom jumps the file selection to the last visible file.
func (m *model) jumpFileBottom() tea.Cmd {
	if len(m.fileFilterVisible) == 0 {
		return nil
	}
	target := len(m.fileFilterVisible) - 1
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

// toggleRevertMark flips a revert mark on the diff panel's
// currently-active hunk. Returns a tea.Cmd that raises a
// "No hunks to revert" status message when the current diff has no
// hunks (binary file, pure rename) or no diff is loaded yet; nil on a
// successful toggle. After toggling the mark, the diff's flagged-hunk
// styling is refreshed so the next render shows the tombstone change
// immediately.
func (m *model) toggleRevertMark() tea.Cmd {
	if m.diffSHA == "" || m.diffPath == "" {
		return m.raiseStatus("No hunks to revert")
	}
	if m.activeHunk == hunkstate.NoActiveHunk {
		return m.raiseStatus("No hunks to revert")
	}
	if m.activeHunk < 0 || m.activeHunk >= len(m.diff.HunkStarts) {
		return m.raiseStatus("No hunks to revert")
	}
	id := m.fileIDs.Resolve(m.diffPath)
	hash := hunkpatch.Hash(hunkpatch.Canonical(
		hunkpatch.ExtractByIndex(m.diffHunks, m.activeHunk)))
	m.reverts.Toggle(m.diffSHA, id, m.activeHunk, hash)
	m.diff.SetFlaggedHunks(m.reverts.MarksForFile(m.diffSHA, id))
	return nil
}

// toggleRevertMarkFromFiles flips a revert mark from the file list
// panel. It targets the currently-selected file, using the
// most-recently-active hunk for that file (the existing hunkstate
// tracker, which defaults to hunk 0 for files not yet visited). When
// the selected file has zero hunks (binary, pure rename, or the diff
// has not yet loaded), it raises a "No hunks to revert" status.
//
// The current selection ordinarily drives an auto-loaded diff (via
// startDiffForSelection), so under normal navigation
// m.diffSHA / m.diffPath / m.activeHunk align with the file panel's
// selection by the time the user presses `d`. Pressing `d` before the
// auto-load completes falls into the no-hunks branch and yields the
// status message.
func (m *model) toggleRevertMarkFromFiles() tea.Cmd {
	sha, path, ok := m.currentSelection()
	if !ok {
		return m.raiseStatus("No hunks to revert")
	}
	// The selected file in the files panel must match the loaded diff
	// for activeHunk to be meaningful.
	if m.diffSHA != sha || m.diffPath != path {
		return m.raiseStatus("No hunks to revert")
	}
	return m.toggleRevertMark()
}

// seedFileIDs folds the rename relationships from one commit's
// NumStat into m.fileIDs so every path the user has seen across loaded
// history resolves to a stable FileID. Renames apply ApplyRename
// (old → new); plain touches Resolve to allocate an ID if not seen yet.
// Safe to call repeatedly with the same file list; the registry is
// idempotent under re-touch and self-renames.
func (m *model) seedFileIDs(files []gitcmd.FileStat) {
	if m.fileIDs == nil {
		m.fileIDs = fileid.New()
	}
	for _, f := range files {
		if f.OldPath != "" && f.Path != "" && f.OldPath != f.Path {
			m.fileIDs.ApplyRename(f.OldPath, f.Path)
			continue
		}
		if f.Path != "" {
			m.fileIDs.Resolve(f.Path)
		}
	}
}

// hunkTotalsKey returns the map key used by m.hunkTotals for a
// (sha, path) pair.
func hunkTotalsKey(sha, path string) string {
	return sha + "\x00" + path
}

// commitFullyFlagged reports whether every hunk across every file of
// the commit has been flagged for revert. Requires both the commit's
// file list (cached on FilesResult) and a known hunk total for every
// non-binary file (cached on DiffResult). When either is missing the
// renderer falls back to `*` rather than auto-promoting to `D` — so
// the user only sees `D` after they have visited every file in the
// commit at least once. A commit with zero countable hunks (all binary
// or pure-rename) never auto-promotes.
func (m *model) commitFullyFlagged(sha string) bool {
	files, ok := m.filesByCommit[sha]
	if !ok || len(files) == 0 {
		return false
	}
	total := 0
	for _, f := range files {
		if f.IsBinary {
			continue
		}
		t, known := m.hunkTotals[hunkTotalsKey(sha, f.Path)]
		if !known {
			return false
		}
		total += t
	}
	if total == 0 {
		return false
	}
	return m.reverts.MarksForCommit(sha) == total
}

// fileFullyFlagged reports whether every hunk of (sha, path) is
// flagged for revert. Returns false when the file's hunk total is not
// yet known (i.e. the user has not visited the diff for this file).
func (m *model) fileFullyFlagged(sha, path string) bool {
	total, known := m.hunkTotals[hunkTotalsKey(sha, path)]
	if !known || total <= 0 {
		return false
	}
	return m.reverts.CountForFile(sha, m.fileIDs.Resolve(path)) == total
}

// commitActionDisplay returns the action-column letter for a commit
// row and whether the row should be rendered with the dropped-subject
// styling (strikethrough + faint).
//
// Explicit user drops always win: a commit with ActionDrop pending
// shows `D` and gets the dropped subject styling regardless of any
// revert marks. Otherwise, a commit whose every hunk is flagged
// auto-promotes to `D`; a commit with at least one (but not every)
// hunk flagged shows `*` without the subject styling. A commit with
// no marks returns ("", false).
func (m *model) commitActionDisplay(sha string) (letter string, dropStyle bool) {
	if kind, ok := m.pendingActions[sha]; ok && kind == ActionDrop {
		return "D", true
	}
	if m.reverts.MarksForCommit(sha) == 0 {
		return "", false
	}
	if m.commitFullyFlagged(sha) {
		return "D", true
	}
	return "*", false
}

// hasAnyActionMarks reports whether the log view should reserve its
// leading action column. True when any commit has a drop mark or any
// revert mark exists in the session.
func (m *model) hasAnyActionMarks() bool {
	if len(m.pendingActions) > 0 {
		return true
	}
	// Any revert mark forces the column. Walk the cached commits and
	// check the per-commit counter rather than poking at revertstate
	// internals.
	for i := range m.commits {
		if m.reverts.MarksForCommit(m.commits[i].SHA) > 0 {
			return true
		}
	}
	return false
}

// startSave is the ctrl+s entry point. It enforces the slice-12
// edge-case matrix:
//
//   - no marks anywhere → "Nothing to process"
//   - cursor has no marks, no drops, but other commits carry revert
//     marks → guide the user to navigate to one of those commits
//   - cursor has no marks, drops exist → existing drop-only flow
//   - cursor has revert marks (with or without drops) → unified
//     RebaseEditStart pipeline. When every hunk on the cursor commit
//     is flagged, the rebase auto-promotes to dropping the cursor
//     entirely (no apply step).
//
// The full precondition battery (clean worktree, branch checked out
// elsewhere, detached HEAD, stale marks) runs once for every routed
// flow; failures open the refuse popup or raise a status message.
func (m model) startSave() (tea.Model, tea.Cmd) {
	cursorSHA := ""
	if m.selectedIdx >= 0 && m.selectedIdx < len(m.commits) {
		cursorSHA = m.commits[m.selectedIdx].SHA
	}
	cursorReverts := 0
	if cursorSHA != "" {
		cursorReverts = m.reverts.MarksForCommit(cursorSHA)
	}
	if cursorReverts == 0 && len(m.pendingActions) == 0 {
		if m.hasAnyRevertMarks() {
			return m, m.raiseStatus("Navigate to a commit with revert marks to process them")
		}
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
	if cursorReverts > 0 {
		// Combined or revert-only flow: validate cursor + every drop.
		ok, err := m.git.CommitExists(ctx, cursorSHA)
		if err != nil {
			return m, m.raiseError(err)
		}
		if !ok {
			m.rebaseRefuseMsg = "Cursor commit no longer exists — refresh first"
			m.rebaseState = rebaseStateRefuse
			return m, nil
		}
		missing := 0
		drops := make([]string, 0, len(m.pendingActions))
		for sha, kind := range m.pendingActions {
			if kind != ActionDrop {
				continue
			}
			if sha == cursorSHA {
				// Defensive: an explicit drop on the cursor would race
				// with auto-promote. Skip it here; the cursor is handled
				// by the revert pipeline.
				continue
			}
			drops = append(drops, sha)
			exists, err := m.git.CommitExists(ctx, sha)
			if err != nil {
				return m, m.raiseError(err)
			}
			if !exists {
				missing++
			}
		}
		if missing > 0 {
			m.rebaseRefuseMsg = fmt.Sprintf("%d marked commit%s no longer exist — refresh first",
				missing, plural2(missing))
			m.rebaseState = rebaseStateRefuse
			return m, nil
		}
		autoPromote := m.commitFullyFlagged(cursorSHA)
		var patches map[string]string
		count := 0
		if !autoPromote {
			patches, count, err = m.buildRevertPatchesForCommit(ctx, cursorSHA)
			if err != nil {
				return m, m.raiseError(err)
			}
			if count == 0 {
				return m, m.raiseStatus("No revertable hunks on cursor commit")
			}
		} else {
			count = m.reverts.MarksForCommit(cursorSHA)
		}
		m.rebaseFlow = rebaseFlowRevert
		m.rebaseRevertCursorSHA = cursorSHA
		m.rebaseRevertPatches = patches
		m.rebaseRevertCount = count
		m.rebaseRevertDrops = drops
		m.rebaseRevertAutoPromote = autoPromote
		m.rebaseState = rebaseStateSummary
		return m, nil
	}
	// Drop-only flow: stale-check every marked SHA.
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
	m.rebaseFlow = rebaseFlowDrop
	m.rebaseState = rebaseStateSummary
	return m, nil
}

// hasAnyRevertMarks reports whether any commit in the loaded log
// currently carries at least one revert mark. Used by startSave to
// distinguish "nothing to do" from "marks exist but you're not on a
// commit that has them".
func (m *model) hasAnyRevertMarks() bool {
	for i := range m.commits {
		if m.reverts.MarksForCommit(m.commits[i].SHA) > 0 {
			return true
		}
	}
	return false
}

// buildRevertPatchesForCommit produces the per-file reverse-patch map
// for every file with revert marks on sha. Each file's marked hunks are
// extracted from a fresh default-context diff (so canonical hunk text
// matches what `git apply -R --3way` will operate on at the edit halt)
// and combined into a single per-file patch. For renamed files the
// patch carries `rename from`/`rename to` headers so the reverse apply
// undoes both the rename and the flagged content, per PRD story 24.
// Returns the patch map and the total hunk count. A file whose ExtractByIndex
// returns "" for every index — i.e. every marked index is now out of range —
// contributes nothing and is omitted; the count reflects only hunks that
// will actually be applied.
func (m *model) buildRevertPatchesForCommit(ctx context.Context, sha string) (map[string]string, int, error) {
	patches := map[string]string{}
	total := 0
	for _, f := range m.filesByCommit[sha] {
		if f.IsBinary {
			continue
		}
		id := m.fileIDs.Resolve(f.Path)
		idxs := m.reverts.MarksForFile(sha, id)
		if len(idxs) == 0 {
			continue
		}
		diff, err := m.git.DiffHunks(ctx, sha, f.Path)
		if err != nil {
			return nil, 0, err
		}
		hunks := make([]string, 0, len(idxs))
		for _, idx := range idxs {
			h := hunkpatch.ExtractByIndex(diff, idx)
			if h == "" {
				continue
			}
			hunks = append(hunks, h)
		}
		if len(hunks) == 0 {
			continue
		}
		patches[f.Path] = buildPerFilePatch(f, hunks)
		total += len(hunks)
	}
	return patches, total, nil
}

// buildPerFilePatch assembles the reverse patch for one file. For a
// non-renamed file it delegates to hunkpatch.CombineForFile. For a
// renamed file it synthesises a header carrying both the rename and
// the hunks so `git apply -R` undoes them together.
func buildPerFilePatch(f gitcmd.FileStat, hunks []string) string {
	if len(hunks) == 0 {
		return ""
	}
	if f.OldPath == "" || f.OldPath == f.Path {
		return hunkpatch.CombineForFile(f.Path, hunks)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n", f.OldPath, f.Path)
	fmt.Fprintf(&b, "rename from %s\n", f.OldPath)
	fmt.Fprintf(&b, "rename to %s\n", f.Path)
	fmt.Fprintf(&b, "--- a/%s\n", f.OldPath)
	fmt.Fprintf(&b, "+++ b/%s\n", f.Path)
	for _, h := range hunks {
		b.WriteString(strings.TrimRight(h, "\n"))
		b.WriteByte('\n')
	}
	return b.String()
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
			m.rebaseFlow = rebaseFlowNone
			m.rebaseRevertCursorSHA = ""
			m.rebaseRevertPatches = nil
			m.rebaseRevertCount = 0
			m.rebaseRevertDrops = nil
			m.rebaseRevertAutoPromote = false
			return m, nil
		}
		return m, nil
	case rebaseStateSummary:
		switch keyStr {
		case "ctrl+c":
			return m, tea.Quit
		case "esc", "q":
			m.rebaseState = rebaseStateIdle
			m.rebaseFlow = rebaseFlowNone
			m.rebaseRevertCursorSHA = ""
			m.rebaseRevertPatches = nil
			m.rebaseRevertCount = 0
			m.rebaseRevertDrops = nil
			m.rebaseRevertAutoPromote = false
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
		case "a", "esc":
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
	if m.rebaseFlow == rebaseFlowRevert {
		atApply := m.rebaseRevertAtApply
		m.rebaseRevertAtApply = false
		return m, m.runRebaseRevertResumeCmd(ctx, gitcmd.SideTheirs, nil, atApply)
	}
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
// message confirming the repo is back to its pre-rebase state. For the
// revert flow the pre-`ctrl-s` snapshot of revert marks is restored
// (including the cursor commit's marks that the rebase would have
// processed) so cancellation is fully non-destructive.
func (m model) conflictAbort() (tea.Model, tea.Cmd) {
	abortCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = m.git.RebaseAbort(abortCtx)
	m.rebaseState = rebaseStateIdle
	m.rebaseHalt = gitcmd.RebaseResult{}
	m.rebaseManualUnmerged = nil
	m.rebaseAnchorSHA = ""
	if m.rebaseFlow == rebaseFlowRevert {
		m.reverts.Restore(m.rebaseRevertSnapshot)
		m.rebaseRevertSnapshot = revertstate.Snapshot{}
		m.rebaseRevertCursorSHA = ""
		m.rebaseRevertPatches = nil
		m.rebaseRevertCount = 0
		m.rebaseRevertDrops = nil
		m.rebaseRevertAutoPromote = false
		m.rebaseRevertAtApply = false
		m.rebaseFlow = rebaseFlowNone
		return m, m.raiseStatus("Rebase cancelled — no changes made")
	}
	m.rebaseSnapshot = nil
	m.rebaseFlow = rebaseFlowNone
	return m, m.raiseStatus("Drop cancelled — repo unchanged")
}

// conflictResolveSide handles [t] / [o] on the conflict popup: stages
// every conflicted path to the chosen side and kicks off the
// appropriate continuation. For the drop flow that's just
// CheckoutSide + RebaseContinue; for the revert flow at an apply-step
// halt the worktree resolution still needs AmendNoEdit before
// `git rebase --continue` (cascade halts behave like the drop flow).
func (m model) conflictResolveSide(side gitcmd.ConflictSide) (tea.Model, tea.Cmd) {
	paths := append([]string(nil), m.rebaseHalt.ConflictedPaths...)
	m.rebaseHalt = gitcmd.RebaseResult{}
	m.rebaseState = rebaseStateRunning
	ctx, cancel := context.WithCancel(context.Background())
	m.rebaseCancel = cancel
	if m.rebaseFlow == rebaseFlowRevert {
		atApply := m.rebaseRevertAtApply
		m.rebaseRevertAtApply = false
		return m, m.runRebaseRevertResumeCmd(ctx, side, paths, atApply)
	}
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
	if m.rebaseFlow == rebaseFlowRevert {
		return m.confirmRevertRebase()
	}
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

// confirmRevertRebase snapshots the revert tracker, computes the
// post-success cursor anchor, and launches the revert pipeline. The
// anchor is the cursor commit's parent (next row down in the log) so
// the post-rebase refresh can land the cursor on a SHA that survived
// the rebase. If no older commit is loaded the anchor is left empty
// and the cursor falls back to the top of the refreshed log.
func (m model) confirmRevertRebase() (tea.Model, tea.Cmd) {
	m.rebaseRevertSnapshot = m.reverts.Snapshot()
	anchor := ""
	for i := m.selectedIdx + 1; i < len(m.commits); i++ {
		sha := m.commits[i].SHA
		if _, dropped := m.pendingActions[sha]; dropped {
			continue
		}
		anchor = sha
		break
	}
	m.rebaseAnchorSHA = anchor
	m.rebaseState = rebaseStateRunning
	ctx, cancel := context.WithCancel(context.Background())
	m.rebaseCancel = cancel
	return m, m.runRebaseRevertCmd(
		ctx,
		m.rebaseRevertCursorSHA,
		m.rebaseRevertPatches,
		m.rebaseRevertDrops,
		m.rebaseRevertAutoPromote,
	)
}

// runRebaseRevertCmd drives the unified revert pipeline. The normal
// path runs RebaseEditStart → ApplyReverse3Way → AmendNoEdit →
// RebaseContinue; auto-promote skips straight from RebaseEditStart
// (cursor todo entry is `drop`) to RebaseContinue's RebaseDone — no
// apply / amend step. A halt at any stage posts a `halted` message;
// handleRevertDone opens the conflict popup. Resumption re-enters
// via runRebaseRevertResumeCmd.
func (m model) runRebaseRevertCmd(ctx context.Context, cursorSHA string, patches map[string]string, drops []string, autoPromote bool) tea.Cmd {
	git := m.git
	cursorSubj := ""
	for _, c := range m.commits {
		if c.SHA == cursorSHA {
			cursorSubj = c.Subject
			break
		}
	}
	return func() tea.Msg {
		res, err := git.RebaseEditStart(ctx, cursorSHA, drops, autoPromote)
		if ctx.Err() != nil {
			return revertDoneMsg{cancelled: true, midRebase: true}
		}
		if err != nil {
			return revertDoneMsg{err: err}
		}
		switch res.State {
		case gitcmd.RebaseDone:
			// Auto-promote completes here without an edit halt; the
			// drop-only sub-case (no reverts at all) is routed
			// elsewhere, so this branch is the expected success path
			// when autoPromote is true.
			return revertDoneMsg{done: true}
		case gitcmd.RebaseEditHalt:
			if autoPromote {
				// Defensive: should not happen — auto-promote substitutes
				// drop, so no edit halt is expected. Abort.
				return revertDoneMsg{err: errors.New("unexpected edit halt during auto-promote"), midRebase: true}
			}
			// fall through to apply
		case gitcmd.RebaseHalted:
			return revertDoneMsg{halted: true, midRebase: true, conflicted: res.ConflictedPaths, haltSHA: res.HaltSHA, haltSubj: res.HaltSubject, stderr: res.Stderr}
		default:
			return revertDoneMsg{stderr: res.Stderr, err: errors.New("rebase failed before edit halt")}
		}
		unmerged, err := git.ApplyReverse3Way(ctx, patches)
		if ctx.Err() != nil {
			return revertDoneMsg{cancelled: true, midRebase: true}
		}
		if err != nil {
			return revertDoneMsg{err: err, midRebase: true}
		}
		if len(unmerged) > 0 {
			return revertDoneMsg{halted: true, atApply: true, midRebase: true, conflicted: unmerged, haltSHA: cursorSHA, haltSubj: cursorSubj}
		}
		if err := git.AmendNoEdit(ctx); err != nil {
			if ctx.Err() != nil {
				return revertDoneMsg{cancelled: true, midRebase: true}
			}
			return revertDoneMsg{err: err, midRebase: true}
		}
		res, err = git.RebaseContinue(ctx)
		if ctx.Err() != nil {
			return revertDoneMsg{cancelled: true, midRebase: true}
		}
		if err != nil {
			return revertDoneMsg{err: err, midRebase: true}
		}
		switch res.State {
		case gitcmd.RebaseDone:
			return revertDoneMsg{done: true}
		case gitcmd.RebaseHalted:
			return revertDoneMsg{halted: true, midRebase: true, conflicted: res.ConflictedPaths, haltSHA: res.HaltSHA, haltSubj: res.HaltSubject, stderr: res.Stderr}
		default:
			return revertDoneMsg{stderr: res.Stderr, err: errors.New("rebase failed during continue"), midRebase: true}
		}
	}
}

// runRebaseRevertResumeCmd resumes the revert pipeline after a conflict
// popup resolution. Steps (each skipped when not applicable):
//
//   - stage `paths` to `side` (bulk [t]/[o] resolve). For manual
//     resolve, paths is empty and CheckoutSide is skipped — the user
//     has already staged their resolution.
//   - if atApply: AmendNoEdit folds the now-resolved worktree into
//     the cursor commit.
//   - RebaseContinue advances the rebase. A further halt produces
//     another `halted` revertDoneMsg (always with atApply=false, since
//     subsequent halts are cascade halts on downstream commits).
func (m model) runRebaseRevertResumeCmd(ctx context.Context, side gitcmd.ConflictSide, paths []string, atApply bool) tea.Cmd {
	git := m.git
	return func() tea.Msg {
		if len(paths) > 0 {
			if err := git.CheckoutSide(ctx, side, paths); err != nil {
				if ctx.Err() != nil {
					return revertDoneMsg{cancelled: true, midRebase: true}
				}
				return revertDoneMsg{err: err, midRebase: true}
			}
		}
		if atApply {
			if err := git.AmendNoEdit(ctx); err != nil {
				if ctx.Err() != nil {
					return revertDoneMsg{cancelled: true, midRebase: true}
				}
				return revertDoneMsg{err: err, midRebase: true}
			}
		}
		res, err := git.RebaseContinue(ctx)
		if ctx.Err() != nil {
			return revertDoneMsg{cancelled: true, midRebase: true}
		}
		if err != nil {
			return revertDoneMsg{err: err, midRebase: true}
		}
		switch res.State {
		case gitcmd.RebaseDone:
			return revertDoneMsg{done: true}
		case gitcmd.RebaseHalted:
			return revertDoneMsg{halted: true, midRebase: true, conflicted: res.ConflictedPaths, haltSHA: res.HaltSHA, haltSubj: res.HaltSubject, stderr: res.Stderr}
		default:
			return revertDoneMsg{stderr: res.Stderr, err: errors.New("rebase failed during continue"), midRebase: true}
		}
	}
}

// handleRevertDone routes the revert pipeline's outcome. A halt opens
// the conflict popup with the unmerged paths and the apply-vs-cascade
// flag the resume path needs; cancel/err aborts the mid-rebase state
// and restores the snapshot; done clears the cursor commit's marks
// and refreshes the log onto the anchor. Adoption of marks on
// rewritten descendant SHAs lands in slice 13.
func (m model) handleRevertDone(msg revertDoneMsg) (tea.Model, tea.Cmd) {
	if m.rebaseCancel != nil {
		m.rebaseCancel()
		m.rebaseCancel = nil
	}
	m.rebaseManualUnmerged = nil

	cursorSHA := m.rebaseRevertCursorSHA
	count := m.rebaseRevertCount
	drops := m.rebaseRevertDrops
	autoPromote := m.rebaseRevertAutoPromote

	if msg.halted {
		m.rebaseHalt = gitcmd.RebaseResult{
			State:           gitcmd.RebaseHalted,
			HaltSHA:         msg.haltSHA,
			HaltSubject:     msg.haltSubj,
			ConflictedPaths: msg.conflicted,
			Stderr:          msg.stderr,
		}
		m.rebaseRevertAtApply = msg.atApply
		m.rebaseState = rebaseStateConflict
		return m, nil
	}

	m.rebaseState = rebaseStateIdle
	m.rebaseHalt = gitcmd.RebaseResult{}
	m.rebaseRevertAtApply = false

	if !msg.done {
		if msg.midRebase {
			abortCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = m.git.RebaseAbort(abortCtx)
		}
		m.reverts.Restore(m.rebaseRevertSnapshot)
		m.rebaseRevertSnapshot = revertstate.Snapshot{}
		m.rebaseRevertCursorSHA = ""
		m.rebaseRevertPatches = nil
		m.rebaseRevertCount = 0
		m.rebaseRevertDrops = nil
		m.rebaseRevertAutoPromote = false
		m.rebaseAnchorSHA = ""
		m.rebaseFlow = rebaseFlowNone
		switch {
		case msg.cancelled:
			return m, m.raiseStatus("Rebase cancelled — no changes made")
		case msg.err != nil:
			return m, m.raiseError(fmt.Errorf("Revert failed: %s", msg.err.Error()))
		default:
			summary := summariseStderr(msg.stderr)
			if summary == "" {
				summary = "unknown rebase error"
			}
			return m, m.raiseError(fmt.Errorf("Revert failed: %s", summary))
		}
	}

	// Success: clear the cursor commit's revert marks (already processed
	// by the rebase) and every drop commit's marks (the commit is gone,
	// so the marks have no home). Marks remaining after this clearing
	// belong to ancestors or descendants of the rebase range and need
	// to be re-attached to the new history by content match. Build the
	// adoption multiset from the remaining marks, then reset the
	// tracker — every surviving mark will be re-installed by the
	// post-refresh walk in handleAdoptionDone, keyed by the new SHA.
	m.reverts.ClearSHA(cursorSHA)
	for _, sha := range drops {
		delete(m.pendingActions, sha)
		m.reverts.ClearSHA(sha)
	}
	adoptionTable := m.reverts.BuildAdoptionTable()
	adoptionTotal := 0
	adoptionWanted := map[string]struct{}{}
	for k, n := range adoptionTable {
		adoptionTotal += n
		adoptionWanted[k.HunkHash] = struct{}{}
	}
	// Reset the tracker even when the table is empty: any leftover mark
	// without a hash is unadoptable and otherwise becomes ghost state
	// keyed by a stale SHA.
	m.reverts = revertstate.New()
	if adoptionTotal > 0 {
		m.adoptionTable = adoptionTable
		m.adoptionTotal = adoptionTotal
		m.adoptionWanted = adoptionWanted
	} else {
		m.adoptionTable = nil
		m.adoptionTotal = 0
		m.adoptionWanted = nil
	}
	m.rebaseRevertSnapshot = revertstate.Snapshot{}
	m.rebaseRevertCursorSHA = ""
	m.rebaseRevertPatches = nil
	m.rebaseRevertCount = 0
	m.rebaseRevertDrops = nil
	m.rebaseRevertAutoPromote = false
	m.rebaseFlow = rebaseFlowNone
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
	short := cursorSHA
	if len(short) > 8 {
		short = short[:8]
	}
	dropCount := len(drops)
	if autoPromote {
		// In the auto-promote path the cursor was processed as a drop;
		// roll it into the drop count for the user-facing message and
		// suppress the "Reverted N hunks" half (the user marked all
		// the hunks but the rebase didn't actually run apply step).
		dropCount++
	}
	var flash string
	switch {
	case autoPromote:
		flash = fmt.Sprintf("Dropped %d commit%s", dropCount, plural2(dropCount))
	case dropCount > 0:
		flash = fmt.Sprintf("Reverted %d hunk%s in %s, dropped %d commit%s",
			count, plural2(count), short, dropCount, plural2(dropCount))
	default:
		flash = fmt.Sprintf("Reverted %d hunk%s in %s", count, plural2(count), short)
	}
	return m, tea.Batch(m.loadLogCmd(0), m.raiseStatus(flash))
}

// maybeStartAdoptionWalk dispatches the post-rebase adoption walk
// when handleRevertDone has staged an adoption table. Returns nil
// when there is nothing to adopt (no marks survived clearing, or
// the table was already consumed by an earlier walk).
func (m *model) maybeStartAdoptionWalk() tea.Cmd {
	if m.adoptionTable == nil || m.adoptionTotal == 0 || len(m.commits) == 0 {
		return nil
	}
	shas := make([]string, len(m.commits))
	for i, c := range m.commits {
		shas[i] = c.SHA
	}
	return m.runAdoptionWalkCmd(shas, m.adoptionWanted)
}

// flushAdoptionAsDiscards posts an empty walk result to drive the
// discard-counting path without making any git calls. Used when the
// post-rebase log refresh returns no commits at all, so no candidate
// can possibly adopt.
func (m *model) flushAdoptionAsDiscards() tea.Cmd {
	return func() tea.Msg {
		return adoptionDoneMsg{commits: nil}
	}
}

// runAdoptionWalkCmd walks the loaded post-rebase commit range
// (chronologically, oldest → newest) and gathers every hunk whose
// canonical hash appears in `wanted`. NumStat per commit also yields
// the rename events the main thread needs to update its fileid
// registry before the candidates are adopted. The walk is purely
// read-only: it makes no mark insertions itself and does not touch
// the registry — adoption is finalised in handleAdoptionDone with
// access to the model's shared state.
func (m model) runAdoptionWalkCmd(shas []string, wanted map[string]struct{}) tea.Cmd {
	git := m.git
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		out := make([]adoptionCommitData, 0, len(shas))
		// shas comes in newest-first (the log order); reverse-iterate so
		// the main thread sees commits oldest-first, matching the
		// multiset-decrement walk described in the PRD.
		for i := len(shas) - 1; i >= 0; i-- {
			sha := shas[i]
			files, err := git.NumStat(ctx, sha)
			if err != nil {
				continue
			}
			cd := adoptionCommitData{sha: sha, files: files}
			for _, f := range files {
				if f.Path == "" || f.IsBinary {
					continue
				}
				diff, err := git.DiffHunks(ctx, sha, f.Path)
				if err != nil || diff == "" {
					continue
				}
				for idx := 0; ; idx++ {
					hunk := hunkpatch.ExtractByIndex(diff, idx)
					if hunk == "" {
						break
					}
					h := hunkpatch.Hash(hunkpatch.Canonical(hunk))
					if _, want := wanted[h]; !want {
						continue
					}
					cd.candidates = append(cd.candidates, adoptionCandidate{
						path: f.Path,
						idx:  idx,
						hash: h,
					})
				}
			}
			out = append(out, cd)
		}
		return adoptionDoneMsg{commits: out}
	}
}

// handleAdoptionDone consumes the walk's output: it replays each
// commit's rename events onto m.fileIDs (so the registry's view of
// the new history matches what the user sees), then attempts to
// adopt each candidate against the saved table. Adoption decrements
// the table's count for that (FileID, HunkHash); when no count
// remains the candidate is silently discarded. The walk preserves
// the multiset semantics required by the PRD: identical hunks in
// different files don't cross-adopt (different FileIDs), and a hunk
// repeated N times adopts up to N times.
//
// After the walk, any leftover counts in the table are discards:
// marks that had no home in the new history. The post-rebase status
// flash is supplemented with a discard-count line when that count
// is nonzero so the user knows work was lost.
func (m model) handleAdoptionDone(msg adoptionDoneMsg) (tea.Model, tea.Cmd) {
	table := m.adoptionTable
	total := m.adoptionTotal
	m.adoptionTable = nil
	m.adoptionTotal = 0
	m.adoptionWanted = nil
	if table == nil || total == 0 {
		return m, nil
	}
	adopted := 0
	for _, cd := range msg.commits {
		m.seedFileIDs(cd.files)
		for _, cand := range cd.candidates {
			fid := m.fileIDs.Resolve(cand.path)
			if m.reverts.Adopt(table, cd.sha, fid, cand.idx, cand.hash) {
				adopted++
			}
		}
	}
	discards := total - adopted
	if discards <= 0 {
		return m, nil
	}
	return m, m.raiseStatus(fmt.Sprintf(
		"%d mark%s discarded due to history changes", discards, plural2(discards)))
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
		m.rebaseFlow = rebaseFlowNone
		return m, m.raiseStatus("Drop cancelled — repo unchanged")
	}
	// Unexpected gitcmd-level failure (e.g. cannot fork sed). Auto-
	// abort defensively and surface the underlying error.
	if msg.err != nil {
		abortCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.git.RebaseAbort(abortCtx)
		m.rebaseFlow = rebaseFlowNone
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
		m.rebaseFlow = rebaseFlowNone
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
		m.rebaseFlow = rebaseFlowNone
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

// fileFilterDebounce is the window during which a regex token that
// becomes invalid mid-edit keeps applying its prior valid form, so
// brief intermediate states don't cause the file pane to flicker.
const fileFilterDebounce = 500 * time.Millisecond

// fileFilterDebounceMsg is delivered by tea.Tick `fileFilterDebounce`
// after the prompt's debounce window opened. The update path re-runs
// the live parse so any expired prior-valid regex is dropped.
type fileFilterDebounceMsg struct{}

// openFileFilterPrompt opens the file-filter prompt. The current
// selection (visible-list position and viewport top) is captured so
// `esc` can restore it; the prompt is prefilled with the committed
// expression so iterative refinement is one keystroke away.
func (m *model) openFileFilterPrompt() {
	m.fileFilterPromptActive = true
	m.fileFilterPromptInput = m.fileFilterExpr.String()
	m.fileFilterPromptExpr = m.fileFilterExpr
	m.fileFilterPromptOrigIdx = m.filesSelectedIdx
	m.fileFilterPromptOrigTop = m.filesViewportTop
	m.fileFilterLastValid = nil
	m.fileFilterInvalidSince = nil
	m.pendingG = false
}

// reparseFileFilterPrompt re-parses the prompt input via the tolerant
// ParsePartial path and refreshes the visible-file list so typing
// produces live feedback. Catastrophic parse errors (unclosed quote)
// leave the prior valid Expr in place. Invalid regex tokens are
// debounced per position: the prior valid form keeps applying for
// `fileFilterDebounce` (stories 17–18). Returns a tea.Cmd that
// schedules a re-parse when the debounce window opens, so the file
// pane re-renders even if the user stops typing.
// deleteLastFilterToken removes the trailing comma-separated filter
// token from input — the one a cursor at end-of-input is sitting on —
// along with the comma and whitespace that separated it from the
// previous token. With well-formed input it relies on ParsePartial's
// byte spans; if the parser rejects the input (e.g. unclosed quote)
// it falls back to trimming back through the last literal comma so
// ctrl+w still makes progress.
func deleteLastFilterToken(input string) string {
	if input == "" {
		return ""
	}
	_, toks, err := filefilter.ParsePartial(input)
	if err == nil && len(toks) > 0 {
		cut := toks[len(toks)-1].Start
		for cut > 0 && (input[cut-1] == ' ' || input[cut-1] == '\t') {
			cut--
		}
		if cut > 0 && input[cut-1] == ',' {
			cut--
		}
		for cut > 0 && (input[cut-1] == ' ' || input[cut-1] == '\t') {
			cut--
		}
		return input[:cut]
	}
	if idx := strings.LastIndexByte(input, ','); idx >= 0 {
		return strings.TrimRight(input[:idx], " \t")
	}
	return ""
}

func (m *model) reparseFileFilterPrompt() tea.Cmd {
	_, tokens, err := filefilter.ParsePartial(m.fileFilterPromptInput)
	if err != nil {
		return nil
	}
	now := time.Now()
	// Resize per-position cache to current token count.
	if len(m.fileFilterLastValid) > len(tokens) {
		m.fileFilterLastValid = m.fileFilterLastValid[:len(tokens)]
		m.fileFilterInvalidSince = m.fileFilterInvalidSince[:len(tokens)]
	}
	for len(m.fileFilterLastValid) < len(tokens) {
		m.fileFilterLastValid = append(m.fileFilterLastValid, nil)
		m.fileFilterInvalidSince = append(m.fileFilterInvalidSince, time.Time{})
	}
	merged := make([]filefilter.Token, 0, len(tokens))
	needsTick := false
	for i, tk := range tokens {
		if tk.Valid {
			// Becoming valid applies immediately; remember it as the
			// per-position fallback for future invalid edits.
			tcopy := tk
			m.fileFilterLastValid[i] = &tcopy
			m.fileFilterInvalidSince[i] = time.Time{}
			merged = append(merged, tk)
			continue
		}
		// Only invalid regex tokens reach here (globs always parse).
		prior := m.fileFilterLastValid[i]
		if prior == nil || prior.Kind != filefilter.KindRegex {
			// Never valid at this position, or position now means
			// something incompatible — drop with no debounce.
			continue
		}
		if m.fileFilterInvalidSince[i].IsZero() {
			m.fileFilterInvalidSince[i] = now
		}
		if now.Sub(m.fileFilterInvalidSince[i]) >= fileFilterDebounce {
			// Debounce window elapsed — drop and forget.
			m.fileFilterLastValid[i] = nil
			m.fileFilterInvalidSince[i] = time.Time{}
			continue
		}
		merged = append(merged, *prior)
		needsTick = true
	}
	m.fileFilterPromptExpr = filefilter.ExprFromTokens(m.fileFilterPromptInput, merged)
	m.recomputeVisibleFiles(-1)
	if needsTick {
		return tea.Tick(fileFilterDebounce, func(time.Time) tea.Msg {
			return fileFilterDebounceMsg{}
		})
	}
	return nil
}

// updateFileFilterPrompt handles every keypress while the file-filter
// prompt is open. Enter commits the prompt's expression as the active
// filter; Esc discards the in-progress edit and restores the prior
// selection; an empty submit clears the active filter.
func (m model) updateFileFilterPrompt(km tea.KeyMsg) (tea.Model, tea.Cmd) {
	keyStr := km.String()
	switch keyStr {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.fileFilterPromptActive = false
		m.fileFilterPromptInput = ""
		m.fileFilterPromptExpr = filefilter.Expr{}
		m.fileFilterLastValid = nil
		m.fileFilterInvalidSince = nil
		m.filesSelectedIdx = m.fileFilterPromptOrigIdx
		m.filesViewportTop = m.fileFilterPromptOrigTop
		m.recomputeVisibleFiles(-1)
		return m, nil
	case "enter":
		m.fileFilterPromptActive = false
		// Promote the prompt's last-parsed Expr to the committed filter.
		m.fileFilterExpr = m.fileFilterPromptExpr
		m.fileFilterPromptInput = ""
		m.fileFilterPromptExpr = filefilter.Expr{}
		m.fileFilterLastValid = nil
		m.fileFilterInvalidSince = nil
		// The visible list was tracking the prompt expression; now that
		// it equals the committed filter, just re-anchor selection.
		m.recomputeVisibleFiles(-1)
		// Refresh commit-row dim state from cached numstats. Submitting
		// an empty prompt clears the filter and the dim map entirely;
		// otherwise we re-evaluate against the new Expr.
		m.commitFilterMatch = nil
		m.recomputeCommitFilterMatches()
		cmd := m.onFileSelectionChanged()
		if pref := m.nextPrefetchCmd(); pref != nil {
			return m, tea.Batch(cmd, pref)
		}
		return m, cmd
	case "backspace", "ctrl+h":
		if len(m.fileFilterPromptInput) > 0 {
			r := []rune(m.fileFilterPromptInput)
			m.fileFilterPromptInput = string(r[:len(r)-1])
			cmd := m.reparseFileFilterPrompt()
			return m, cmd
		}
		return m, nil
	case "ctrl+w":
		if m.fileFilterPromptInput == "" {
			return m, nil
		}
		m.fileFilterPromptInput = deleteLastFilterToken(m.fileFilterPromptInput)
		cmd := m.reparseFileFilterPrompt()
		return m, cmd
	case "ctrl+k":
		if m.fileFilterPromptInput == "" {
			return m, nil
		}
		m.fileFilterPromptInput = ""
		m.fileFilterLastValid = nil
		m.fileFilterInvalidSince = nil
		cmd := m.reparseFileFilterPrompt()
		return m, cmd
	}
	if km.Type == tea.KeyRunes || km.Type == tea.KeySpace {
		var r []rune
		if km.Type == tea.KeySpace {
			r = []rune{' '}
		} else {
			r = km.Runes
		}
		m.fileFilterPromptInput += string(r)
		cmd := m.reparseFileFilterPrompt()
		return m, cmd
	}
	return m, nil
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

	m.fileFilterPromptActive = false
	m.fileFilterPromptInput = ""
	m.fileFilterPromptExpr = filefilter.Expr{}
	m.fileFilterPromptOrigIdx = 0
	m.fileFilterPromptOrigTop = 0
	m.fileFilterExpr = filefilter.Expr{}
	m.fileFilterVisible = nil
	m.fileFilterLastValid = nil
	m.fileFilterInvalidSince = nil
	m.commitFilterMatch = nil

	m.middleTab = tabFiles

	// Pending action marks belong to a specific repository state. Switching
	// worktrees discards them entirely.
	m.pendingActions = map[string]ActionKind{}
	m.reverts = revertstate.New()
	m.fileIDs = fileid.New()
	m.diffHunks = ""
	m.hunkTotals = map[string]int{}
	m.filesByCommit = map[string][]gitcmd.FileStat{}
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
	m.rebaseFlow = rebaseFlowNone
	m.rebaseRevertSnapshot = revertstate.Snapshot{}
	m.rebaseRevertCursorSHA = ""
	m.rebaseRevertPatches = nil
	m.rebaseRevertCount = 0
	m.rebaseRevertDrops = nil
	m.rebaseRevertAutoPromote = false
	m.rebaseRevertAtApply = false
	m.adoptionTable = nil
	m.adoptionTotal = 0
	m.adoptionWanted = nil

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
	if origIdx := m.selectedFileOrigIdx(); origIdx >= 0 {
		m.pendingRefreshPath = m.files[origIdx].Path
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
	m.fileFilterVisible = nil

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

	// File filter persists across ctrl+r refresh (PRD §10/11) so a
	// reviewer's investigation context survives the reload. The prompt
	// itself must be closed — its origin snapshots no longer apply.
	m.fileFilterPromptActive = false
	m.fileFilterPromptInput = ""
	m.fileFilterPromptExpr = filefilter.Expr{}
	m.fileFilterPromptOrigIdx = 0
	m.fileFilterPromptOrigTop = 0
	// Refresh rebuilds the loader, invalidating its numstat cache, so
	// every cached dim evaluation is stale. The map is rebuilt on
	// demand as NumStatResults arrive for the new loader.
	m.commitFilterMatch = nil

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
	if origIdx := m.selectedFileOrigIdx(); origIdx >= 0 && m.files[origIdx].IsBinary {
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
	origIdx := m.selectedFileOrigIdx()
	if origIdx < 0 {
		return nil
	}
	f := m.files[origIdx]
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
// commit + visible file, plus ok=true. ok is false when either
// selection is missing (including the case where the file filter has
// hidden every file in the current commit).
func (m *model) currentSelection() (sha, path string, ok bool) {
	if len(m.commits) == 0 || len(m.fileFilterVisible) == 0 {
		return "", "", false
	}
	if m.filesSelectedIdx >= len(m.fileFilterVisible) {
		return "", "", false
	}
	origIdx := m.fileFilterVisible[m.filesSelectedIdx]
	return m.commits[m.selectedIdx].SHA, m.files[origIdx].Path, true
}

// handleMouse routes a MouseMsg. v1 acts on two kinds of mouse input:
// (1) wheel-up / wheel-down over a right panel (message or diff)
// scrolls that panel without changing the active section; (2) a
// left-button press on a row in a left panel (log or files) selects
// that row and activates the corresponding section, firing the same
// downstream live-update chain as a keyboard move. Clicks on right
// panels are inert. Mouse events are suppressed entirely when the
// terminal is too small or any modal / prompt is open so they
// can't bypass those gates.
func (m model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	lo := layout.Compute(m.w, m.h)
	if lo.TooSmall {
		return m, nil
	}
	if m.helpModalOpen || m.wtModalOpen || m.fileFilterPromptActive {
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
			if idx < 0 || idx >= len(m.fileFilterVisible) {
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

// jumpDiffBottom scrolls so the final diff row sits at the bottom of
// the visible body.
func (m *model) jumpDiffBottom() {
	m.diffScroll = m.diff.RowCount()
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
// the viewport, its first row is pinned to the top so the `@@` header
// stays visible. No-op when there is no active hunk or when the active
// hunk has no visible rows (a flagged pure-Add hunk under the issue-15
// row model).
func (m *model) scrollToActiveHunk() {
	first, last, ok := m.diff.HunkVisibleRange(m.activeHunk)
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

// clampDiffScroll constrains diffScroll to [0, max(0, RowCount-bodyH)].
func (m *model) clampDiffScroll() {
	if m.diffScroll < 0 {
		m.diffScroll = 0
	}
	bodyH := m.diffPanelBodyHeight()
	max := m.diff.RowCount() - bodyH
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

// clampFilesViewport adjusts filesViewportTop so filesSelectedIdx stays
// visible. `filesSelectedIdx` and `filesViewportTop` are positions
// inside the visible (filtered) file list, not the raw files slice.
func (m *model) clampFilesViewport() {
	bodyH := m.filesBodyHeight()
	if bodyH <= 0 {
		m.filesViewportTop = 0
		return
	}
	visN := len(m.fileFilterVisible)
	if visN == 0 {
		m.filesSelectedIdx = 0
		m.filesViewportTop = 0
		return
	}
	if m.filesSelectedIdx >= visN {
		m.filesSelectedIdx = visN - 1
	}
	if m.filesSelectedIdx < 0 {
		m.filesSelectedIdx = 0
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
	maxTop := visN - bodyH
	if maxTop < 0 {
		maxTop = 0
	}
	if m.filesViewportTop > maxTop {
		m.filesViewportTop = maxTop
	}
}

// recomputeVisibleFiles rebuilds m.fileFilterVisible from m.files and
// the currently active filter expression. If preservedOrigIdx >= 0,
// the selection is moved to that original-file index when it remains
// visible, otherwise it clamps to the nearest visible original index
// (the first visible idx ≥ preservedOrigIdx, or the last visible idx).
// When preservedOrigIdx is -1 the selection's prior original index is
// inferred from the current filesSelectedIdx before the rebuild.
func (m *model) recomputeVisibleFiles(preservedOrigIdx int) {
	if preservedOrigIdx < 0 {
		preservedOrigIdx = m.selectedFileOrigIdx()
	}
	expr := m.activeFileFilterExpr()
	m.fileFilterVisible = m.fileFilterVisible[:0]
	if cap(m.fileFilterVisible) < len(m.files) {
		m.fileFilterVisible = make([]int, 0, len(m.files))
	}
	for i, f := range m.files {
		if expr.Match(f.Path, f.OldPath) {
			m.fileFilterVisible = append(m.fileFilterVisible, i)
		}
	}
	// Re-anchor selection by original index.
	newSel := -1
	if preservedOrigIdx >= 0 {
		for visIdx, origIdx := range m.fileFilterVisible {
			if origIdx == preservedOrigIdx {
				newSel = visIdx
				break
			}
		}
		if newSel < 0 {
			// Nearest visible: first visible idx >= preservedOrigIdx.
			for visIdx, origIdx := range m.fileFilterVisible {
				if origIdx >= preservedOrigIdx {
					newSel = visIdx
					break
				}
			}
			if newSel < 0 && len(m.fileFilterVisible) > 0 {
				newSel = len(m.fileFilterVisible) - 1
			}
		}
	}
	if newSel < 0 {
		newSel = 0
	}
	m.filesSelectedIdx = newSel
	m.clampFilesViewport()
}

// activeFileFilterExpr returns the Expr that should drive the visible
// file set right now: the live prompt preview while the prompt is open,
// otherwise the committed filter.
func (m *model) activeFileFilterExpr() filefilter.Expr {
	if m.fileFilterPromptActive {
		return m.fileFilterPromptExpr
	}
	return m.fileFilterExpr
}

// commitMatchesFileFilter reports whether at least one file in files
// matches expr. Used to compute the dim state of a commit row.
func commitMatchesFileFilter(files []gitcmd.FileStat, expr filefilter.Expr) bool {
	for _, f := range files {
		if expr.Match(f.Path, f.OldPath) {
			return true
		}
	}
	return false
}

// recomputeCommitFilterMatches rebuilds m.commitFilterMatch from the
// loader's cached numstats: every loaded commit whose numstat is cached
// gets evaluated against the current filter and stored. Commits with no
// cached numstat are left absent (they dim later as their NumStatResult
// arrives, per PRD story 27). A no-op when the filter is empty —
// callers should reset the map themselves in that case.
func (m *model) recomputeCommitFilterMatches() {
	if m.fileFilterExpr.IsEmpty() || m.ldr == nil {
		return
	}
	if m.commitFilterMatch == nil {
		m.commitFilterMatch = make(map[string]bool, len(m.commits))
	}
	expr := m.fileFilterExpr
	for _, c := range m.commits {
		if fs, ok := m.ldr.NumStatCached(c.SHA); ok {
			m.commitFilterMatch[c.SHA] = commitMatchesFileFilter(fs, expr)
		}
	}
}

// isCommitDimmed reports whether the row for sha should be rendered
// dim in the commit log: a file filter is active and we've evaluated
// the commit's numstat and found no matching files. Unknown (numstat
// not yet loaded) is intentionally treated as "not dim" so commits
// progressively dim as data arrives rather than starting dim and
// brightening (which would be visually noisier).
func (m *model) isCommitDimmed(sha string) bool {
	if m.fileFilterExpr.IsEmpty() {
		return false
	}
	matches, ok := m.commitFilterMatch[sha]
	if !ok {
		return false
	}
	return !matches
}

// advanceSelectedSkippingDimmed moves m.selectedIdx by dir (+1 or -1),
// skipping over consecutive dimmed commits in the same direction. A
// commit is dimmed when its numstat has been evaluated and contains no
// files matching the active filter; unknown commits (numstat not yet
// loaded) are not skipped, so navigation never blocks on background
// prefetch. Returns true if selection moved.
func (m *model) advanceSelectedSkippingDimmed(dir int) bool {
	if len(m.commits) == 0 || (dir != 1 && dir != -1) {
		return false
	}
	i := m.selectedIdx + dir
	for i >= 0 && i < len(m.commits) && m.isCommitDimmed(m.commits[i].SHA) {
		i += dir
	}
	if i < 0 || i >= len(m.commits) || i == m.selectedIdx {
		return false
	}
	m.selectedIdx = i
	return true
}

// nextPrefetchCmd returns a tea.Cmd that loads the numstat for the
// first commit in m.commits whose numstat is not already cached, so
// commitFilterMatch can be populated for it and the row dimmed (or
// kept un-dimmed). Returns nil when every loaded commit's numstat is
// already cached, or when no file filter is active.
func (m *model) nextPrefetchCmd() tea.Cmd {
	if m.fileFilterExpr.IsEmpty() || m.ldr == nil {
		return nil
	}
	for _, c := range m.commits {
		if _, ok := m.ldr.NumStatCached(c.SHA); ok {
			continue
		}
		return m.ldr.LoadNumStatPrefetch(c.SHA)
	}
	return nil
}

// selectedFileOrigIdx returns the original m.files index that
// filesSelectedIdx currently points to, or -1 if there is no selection.
func (m *model) selectedFileOrigIdx() int {
	if m.filesSelectedIdx < 0 || m.filesSelectedIdx >= len(m.fileFilterVisible) {
		return -1
	}
	return m.fileFilterVisible[m.filesSelectedIdx]
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
	shortSHAStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	relDateStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	authorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("110"))

	// droppedActionStyle renders the `D` letter in the leftmost action
	// column for commits marked for drop. droppedSubjectStyle is composed
	// over the subject text on the same rows. revertActionStyle paints
	// the `*` letter for commits with at least one (but not every) hunk
	// flagged for revert — distinct from drop so the user can tell at a
	// glance whether the row will be dropped or partially reverted.
	droppedActionStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	droppedSubjectStyle = lipgloss.NewStyle().Strikethrough(true).Faint(true)
	revertActionStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("179")).Bold(true)
	// dimmedFileStyle dims a file-list row whose every hunk is flagged
	// for revert (slice 08): the whole file will be reverted when the
	// commit is processed, so the row visually recedes.
	dimmedFileStyle = lipgloss.NewStyle().Faint(true)

	// commitDimStyle renders a non-selected commit row
	// in a uniformly dim color when the active file filter excludes all
	// of that commit's files. Applied over the plain (color-stripped)
	// row so the dim is uniform rather than mixing with the per-column
	// foreground palette.
	commitDimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Strikethrough(true)

	// Ref-decoration colors on commit rows. HEAD is the brightest
	// (cyan + bold), local branches are green, remote-tracking
	// branches are red, and the surrounding parens / commas are dim.
	refHEADStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("159")).Bold(true)
	refLocalStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	refRemoteStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	refParenStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	errMsgStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	statusMsgStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))

	spinnerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	staleStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	searchPromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))

	// Token styles for the persistent filter row. These paint
	// individual spans of the filter expression so the user can see
	// at a glance which terms are special grammar, which are valid /
	// invalid regex, and which are actually selecting files.
	filterSpecialStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("33"))
	filterRegexValidStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("135"))
	filterRegexInvalidStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	filterBaseMatchStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#8f9d6a"))
	filterBaseNoMatchStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	filterLabelStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	// logUnderlineStyle paints a full-width rule under the log panel by
	// underlining the spaces in the row immediately below it.
	logUnderlineStyle = lipgloss.NewStyle().Underline(true).Foreground(lipgloss.Color("240"))

	fileStatusStyles = map[string]lipgloss.Style{
		"A": lipgloss.NewStyle().Foreground(lipgloss.Color("114")), // green
		"M": lipgloss.NewStyle().Foreground(lipgloss.Color("179")), // orange
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
// (1-cell letter + 2-space gap); dropStyle applies strikethrough+dim
// to the subject portion of restStyled (the plain version is unchanged
// so row-level highlights remain solid). dropStyle is true for `D`
// (explicit or auto-promoted) and false for `*` (partial revert) so
// partially-marked rows keep readable subjects.
func commitRowColumns(c gitcmd.Commit, width int, hasPendingColumn, dropStyle bool) (short, date, author, restStyled, restPlain string, ok bool) {
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
		if dropStyle {
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
	if dropStyle {
		subjStyled = droppedSubjectStyle.Render(subj)
	}
	restStyled = refsStyled + " " + subjStyled
	restPlain = refsPlain + " " + subj
	return short, date, author, restStyled, restPlain, true
}

// renderLogRow formats one commit row to exactly `width` cells with
// per-column foreground colors (used for non-selected rows). When
// hasPendingColumn is true the row reserves a leading 3-cell action
// column (1-cell letter + 2-space gap); actionLetter is the character
// placed in that column ("D", "*", or "" for unmarked rows).
// dropStyle toggles the subject to strikethrough+dim (`D` rows only).
func renderLogRow(c gitcmd.Commit, width int, hasPendingColumn bool, actionLetter string, dropStyle bool) string {
	const gap = "  "
	short, date, author, restStyled, _, ok := commitRowColumns(c, width, hasPendingColumn, dropStyle)
	if !ok {
		return padOrTruncate(short, width)
	}
	prefix := ""
	if hasPendingColumn {
		switch actionLetter {
		case "D":
			prefix = droppedActionStyle.Render("D") + gap
		case "*":
			prefix = revertActionStyle.Render("*") + gap
		default:
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
func renderLogRowPlain(c gitcmd.Commit, width int, hasPendingColumn bool, actionLetter string, dropStyle bool) string {
	const gap = "  "
	short, date, author, _, restPlain, ok := commitRowColumns(c, width, hasPendingColumn, dropStyle)
	if !ok {
		return padOrTruncate(short, width)
	}
	prefix := ""
	if hasPendingColumn {
		letter := actionLetter
		if letter == "" {
			letter = " "
		}
		prefix = letter + gap
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
	hasPendingColumn := m.hasAnyActionMarks()
	for row := 0; row < bodyH; row++ {
		idx := m.viewportTop + row
		if idx >= len(m.commits) {
			lines = append(lines, strings.Repeat(" ", w))
			continue
		}
		letter, dropStyle := m.commitActionDisplay(m.commits[idx].SHA)
		var rendered string
		switch {
		case idx == m.selectedIdx:
			plain := renderLogRowPlain(m.commits[idx], w, hasPendingColumn, letter, dropStyle)
			if active {
				rendered = activeSelectedRowStyle.Render(plain)
			} else {
				rendered = inactiveSelectedRowStyle.Render(plain)
			}
		case m.isCommitDimmed(m.commits[idx].SHA):
			plain := renderLogRowPlain(m.commits[idx], w, hasPendingColumn, letter, dropStyle)
			rendered = commitDimStyle.Render(plain)
		default:
			rendered = renderLogRow(m.commits[idx], w, hasPendingColumn, letter, dropStyle)
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
	msgSHAStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
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
// in a single dim color. While a file filter is active the title row
// is suffixed (right-justified) with the expression and a visible/total
// count.
func renderFilesPanel(m model, w, h int, active, stale bool) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	title := renderFilesTitle(m, w, active, stale)
	bodyH := h - 1
	if bodyH <= 0 {
		return title
	}
	return title + "\n" + renderFilesBodyWithScrollbar(m, w, bodyH, active, stale)
}

// renderFilesTitle renders the files-pane title row. The filter
// expression and match counter live in the bottom status panel
// (see renderFilterPanel), not the files header.
func renderFilesTitle(m model, w int, active, stale bool) string {
	return renderTitleWithSpinner("files", w, active, stale, m.spinnerFrame)
}

// renderFilesBodyWithScrollbar renders the files panel body and overlays
// a vertical scrollbar column on the right when the visible file list
// overflows the viewport.
func renderFilesBodyWithScrollbar(m model, w, bodyH int, active, stale bool) string {
	start, length, draw := layout.ScrollbarThumb(len(m.fileFilterVisible), bodyH, m.filesViewportTop, bodyH)
	if !draw || w < 2 {
		return renderFilesBody(m, w, bodyH, active, stale)
	}
	return appendScrollbarColumn(renderFilesBody(m, w-1, bodyH, active, stale), bodyH, start, length)
}

// renderFilesBody renders the files panel's body rows (no title) at
// the given width and body height. Used directly by the small-mode
// tab strip path, where the tab strip replaces the per-panel title.
//
// When a filter is active and no files match, the body renders as
// blank rows (PRD: "file pane is blank when no files in the current
// commit match the filter"). The "loading…" / "No changes" / clean-merge
// placeholders apply only when there is no filter active.
func renderFilesBody(m model, w, bodyH int, active, stale bool) string {
	if w <= 0 || bodyH <= 0 {
		return ""
	}
	lines := make([]string, 0, bodyH)
	filterActive := !m.activeFileFilterExpr().IsEmpty()
	if len(m.files) == 0 && !filterActive {
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
	visible := make([]gitcmd.FileStat, 0, len(m.fileFilterVisible))
	for _, origIdx := range m.fileFilterVisible {
		visible = append(visible, m.files[origIdx])
	}
	rows := filelist.Format(toFilelist(visible), w)
	for row := 0; row < bodyH; row++ {
		idx := m.filesViewportTop + row
		if idx >= len(rows) {
			lines = append(lines, strings.Repeat(" ", w))
			continue
		}
		// Whole-file revert mark: dim the row when every hunk in this
		// file has been flagged. Computed against the file's path under
		// the currently-displayed commit (m.filesSHA).
		dimRevert := false
		if m.filesSHA != "" {
			dimRevert = m.fileFullyFlagged(m.filesSHA, visible[idx].Path)
		}
		var rendered string
		switch {
		case stale:
			rendered = staleStyle.Render(padOrTruncate(fileRowPlain(rows[idx]), w))
		case idx == m.filesSelectedIdx:
			plain := padOrTruncate(fileRowPlain(rows[idx]), w)
			if active {
				rendered = activeSelectedRowStyle.Render(plain)
			} else {
				rendered = inactiveSelectedRowStyle.Render(plain)
			}
		case dimRevert:
			rendered = dimmedFileStyle.Render(padOrTruncate(fileRowPlain(rows[idx]), w))
		default:
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
	start, length, draw := layout.ScrollbarThumb(m.diff.RowCount(), bodyH, m.diffScroll, bodyH)
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
// and m.diffHScroll as the horizontal offset. Iteration is in
// visible-row coordinates so Add lines hidden by a flagged hunk drop
// out of the rendered output.
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
		rowIdx := start + row
		if rowIdx >= m.diff.RowCount() {
			lines = append(lines, strings.Repeat(" ", w))
			continue
		}
		if stale {
			// Render the raw text without diff/syntax colors, padded and
			// horizontally scrolled the same way FormatLine handles it.
			li, ok := m.diff.RowLineIndex(rowIdx)
			if !ok {
				lines = append(lines, strings.Repeat(" ", w))
				continue
			}
			ln := m.diff.Lines[li]
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
			lines = append(lines, m.diff.FormatRow(rowIdx, w, m.diffHScroll, m.activeHunk))
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
	if origIdx := m.selectedFileOrigIdx(); origIdx >= 0 && m.files[origIdx].IsBinary {
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
	showStatus := m.fileFilterPromptActive || !m.fileFilterExpr.IsEmpty() || m.errMsg != "" || m.statusMsg != ""
	diffH := lo.Diff.H
	if !showStatus {
		diffH += lo.Status.H
	}
	// Reserve one blank row at the bottom of the diff so the diff body
	// never butts up against the filter panel (when shown) or the
	// terminal edge (when hidden).
	diffPanel := renderDiffPanel(m, lo.Diff.W, diffH-1, diffActive, m.diffLoading)
	diffTrailingGap := strings.Repeat(" ", m.w)
	// A full-width underline under the log panel: replace the spaces in
	// the log/middle gap row with underline-styled spaces so the log's
	// bottom edge gets a visible rule.
	logUnderlineGap := logUnderlineStyle.Render(strings.Repeat(" ", m.w))
	vGap := strings.Repeat(" ", m.w)
	parts := []string{logPanel, logUnderlineGap, middleRow, vGap, diffPanel, diffTrailingGap}
	if showStatus {
		parts = append(parts, m.renderStatus(lo.Status.W))
	}
	base := strings.Join(parts, "\n")

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
// ctrl+s passes all preconditions. The action lines depend on the
// staged rebase flow: drop-only lists "Drop N commits"; the revert
// flow lists "Revert N hunks in <short-sha>" and adds a "Drop M
// commits" line when the combined rebase is also dropping commits.
// Auto-promote collapses to a single drop line that includes the
// cursor commit.
func renderRebaseSummaryPopup(m model) string {
	title := modalTitleStyle.Render("Pending actions")
	hint := modalHintStyle.Render("enter confirm · esc cancel")
	rows := []string{title, ""}
	if m.rebaseFlow == rebaseFlowRevert {
		short := m.rebaseRevertCursorSHA
		if len(short) > 8 {
			short = short[:8]
		}
		dropCount := len(m.rebaseRevertDrops)
		if m.rebaseRevertAutoPromote {
			dropCount++
			rows = append(rows, fmt.Sprintf("  Drop %d commit%s", dropCount, plural2(dropCount)))
		} else {
			rows = append(rows, fmt.Sprintf("  Revert %d hunk%s in %s",
				m.rebaseRevertCount, plural2(m.rebaseRevertCount), short))
			if dropCount > 0 {
				rows = append(rows, fmt.Sprintf("  Drop %d commit%s", dropCount, plural2(dropCount)))
			}
		}
	} else {
		drops := 0
		for _, k := range m.pendingActions {
			if k == ActionDrop {
				drops++
			}
		}
		rows = append(rows, fmt.Sprintf("  Drop %d commit%s", drops, plural2(drops)))
	}
	rows = append(rows, "", hint)
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
			{"ctrl+d", "mark/unmark commit for drop"},
			{"ctrl+s", "save pending actions"},
		}},
		{title: "Files (bottom)", bindings: []binding{
			{"ctrl+j / ctrl+k", "scroll diff down / up one line"},
			{"n / N", "next / previous hunk in diff"},
		}},
		{title: "Filter (any pane)", bindings: []binding{
			{"/", "filter files (glob, /regex/, comma-OR, !negate); dims non-matching commits"},
			{"ctrl+w", "while editing: delete the token under the cursor"},
			{"ctrl+k", "while editing: clear all filter tokens"},
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
	if m.fileFilterPromptActive {
		return m.renderFilterPanel(width, true)
	}
	if !m.fileFilterExpr.IsEmpty() {
		return m.renderFilterPanel(width, false)
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

// renderFilterPanel renders the single-line filter row that lives in the
// status slot. When `active`, it shows the live prompt input with a
// cursor block at the end; otherwise it shows the committed expression.
// In both cases a right-justified (visible/total) match indicator is
// appended.
func (m model) renderFilterPanel(width int, active bool) string {
	var input string
	if active {
		input = m.fileFilterPromptInput
	} else {
		input = m.fileFilterExpr.String()
	}
	label := filterLabelStyle.Render("filter: ")
	body := m.renderStyledFilterInput(input)
	if active {
		body += "█"
	}
	left := label + body
	var right string
	if active && input == "" {
		right = filterLabelStyle.Render("(type a glob; enter commits, esc cancels)")
	} else if len(m.files) == 0 {
		right = ""
	} else if len(m.fileFilterVisible) == 0 {
		right = filterLabelStyle.Render("(no matches)")
	} else {
		right = filterLabelStyle.Render(fmt.Sprintf("(%d/%d)", len(m.fileFilterVisible), len(m.files)))
	}
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	if leftW+rightW+1 > width {
		// Not enough room for both — drop the right side.
		return withOverline(padOrTruncate(left, width))
	}
	gap := width - leftW - rightW
	if gap < 1 {
		gap = 1
	}
	return withOverline(left + strings.Repeat(" ", gap) + right)
}

// withOverline wraps s in SGR 53 (overline on) / 55 (overline off) so
// the rendered row carries a full-width line along its top edge. The
// rule itself is pinned to white via SGR 58 (underline/overline color)
// so it doesn't pick up the foreground of the styled text underneath.
// Any embedded full resets (`\x1b[0m` / `\x1b[m`) inside s would
// otherwise drop the overline and its color mid-row, so re-emit both
// after each.
const overlineOn = "\x1b[53m\x1b[58;5;15m"

func withOverline(s string) string {
	s = strings.ReplaceAll(s, "\x1b[0m", "\x1b[0m"+overlineOn)
	s = strings.ReplaceAll(s, "\x1b[m", "\x1b[m"+overlineOn)
	return overlineOn + s + "\x1b[59m\x1b[55m"
}

// renderStyledFilterInput renders `input` with per-token coloring:
//   - whole input red if it fails to tokenize
//   - leading `!`, `*`/`**` (outside quotes), and regex `/` delimiters → blue
//   - regex body → purple (valid) or red (invalid)
//   - glob body → green when at least one file matches the token, orange otherwise
func (m model) renderStyledFilterInput(input string) string {
	if input == "" {
		return ""
	}
	_, tokens, err := filefilter.ParsePartial(input)
	if err != nil {
		return filterRegexInvalidStyle.Render(input)
	}
	var b strings.Builder
	pos := 0
	for _, tk := range tokens {
		if tk.Start < pos || tk.End > len(input) || tk.End <= tk.Start {
			continue
		}
		if tk.Start > pos {
			b.WriteString(filterLabelStyle.Render(input[pos:tk.Start]))
		}
		anyMatch := false
		for _, f := range m.files {
			if tk.Matches(f.Path, f.OldPath) {
				anyMatch = true
				break
			}
		}
		b.WriteString(renderFilterToken(input[tk.Start:tk.End], tk, anyMatch))
		pos = tk.End
	}
	if pos < len(input) {
		b.WriteString(filterLabelStyle.Render(input[pos:]))
	}
	return b.String()
}

// renderFilterToken styles one token's raw source bytes.
func renderFilterToken(src string, tk filefilter.Token, anyMatch bool) string {
	if src == "" {
		return ""
	}
	var b strings.Builder
	i := 0
	if tk.Negate && i < len(src) && src[i] == '!' {
		b.WriteString(filterSpecialStyle.Render("!"))
		i++
	}
	rest := src[i:]
	if tk.Kind == filefilter.KindRegex {
		regexStyle := filterRegexValidStyle
		if !tk.Valid {
			regexStyle = filterRegexInvalidStyle
		}
		// rest is `/body/` or `/body` (unclosed — falls back to glob, but
		// kind is regex only when there's a close). Paint the leading and
		// trailing `/` as special, body in regex style.
		if len(rest) > 0 && rest[0] == '/' {
			b.WriteString(filterSpecialStyle.Render("/"))
			rest = rest[1:]
		}
		hasClose := len(rest) > 0 && rest[len(rest)-1] == '/'
		body := rest
		if hasClose {
			body = rest[:len(rest)-1]
		}
		if body != "" {
			b.WriteString(regexStyle.Render(body))
		}
		if hasClose {
			b.WriteString(filterSpecialStyle.Render("/"))
		}
		return b.String()
	}
	// Glob: walk runes, paint `*`/`**` runs outside quotes as special;
	// everything else gets the orange/green base style.
	baseStyle := filterBaseNoMatchStyle
	if anyMatch {
		baseStyle = filterBaseMatchStyle
	}
	inQuote := false
	runs := []byte(rest)
	flush := func(s string, special bool) {
		if s == "" {
			return
		}
		if special {
			b.WriteString(filterSpecialStyle.Render(s))
		} else {
			b.WriteString(baseStyle.Render(s))
		}
	}
	start := 0
	mode := 0 // 0 = base, 1 = special-star
	for k := 0; k < len(runs); k++ {
		c := runs[k]
		if c == '"' {
			// Flush current run including the quote as base.
			if mode == 1 {
				flush(string(runs[start:k]), true)
				start = k
				mode = 0
			}
			inQuote = !inQuote
			continue
		}
		if c == '*' && !inQuote {
			if mode == 0 {
				flush(string(runs[start:k]), false)
				start = k
				mode = 1
			}
			continue
		}
		if mode == 1 {
			flush(string(runs[start:k]), true)
			start = k
			mode = 0
		}
	}
	flush(string(runs[start:]), mode == 1)
	return b.String()
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

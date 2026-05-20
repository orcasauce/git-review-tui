// Package loader mediates between the Bubble Tea event loop and the
// gitcmd package. It owns LRU caches for commit detail, numstat, and
// diff results, debounces rapid-fire selection moves so the underlying
// git command only runs once a burst has settled, and cancels the
// context of any in-flight load when a newer load supersedes it.
//
// The loader is stateful: callers should drive it from a single
// goroutine (the Bubble Tea event loop). Each request method returns a
// tea.Cmd suitable for returning from Update. Results are delivered as
// typed messages (DetailResult, NumStatResult, DiffResult) carrying the
// identity of the request so callers can discard messages that no
// longer match the current selection.
package loader

import (
	"container/list"
	"context"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/orcasauce/git-review-tui/gitcmd"
)

// Source is the subset of gitcmd the loader depends on. Tests
// substitute a fake implementation.
type Source interface {
	Show(ctx context.Context, sha string) (gitcmd.CommitDetail, error)
	NumStat(ctx context.Context, sha string) ([]gitcmd.FileStat, error)
	Diff(ctx context.Context, sha, path string) (string, error)
	DiffHunks(ctx context.Context, sha, path string) (string, error)
}

// Defaults; the PRD calls for ~80 ms debounce, ~64 commit-metadata
// cache entries, and ~32 per-file diff entries.
const (
	DefaultDebounce   = 80 * time.Millisecond
	DefaultDetailCap  = 64
	DefaultNumStatCap = 64
	DefaultDiffCap    = 32
)

// Config configures a new Loader. Zero-valued fields fall back to the
// package defaults.
type Config struct {
	Source     Source
	Debounce   time.Duration
	DetailCap  int
	NumStatCap int
	DiffCap    int
}

// Loader is the public surface; construct with New.
type Loader struct {
	src      Source
	debounce time.Duration

	detail  *lru[string, gitcmd.CommitDetail]
	numstat *lru[string, []gitcmd.FileStat]
	diff    *lru[diffKey, diffEntry]

	mu            sync.Mutex
	detailCancel  context.CancelFunc
	numstatCancel context.CancelFunc
	diffCancel    context.CancelFunc
}

// diffKey is the cache key for diff entries.
type diffKey struct {
	SHA  string
	Path string
}

// Result types delivered as tea.Msg.

// DetailResult is the message produced by LoadDetail.
type DetailResult struct {
	SHA    string
	Detail gitcmd.CommitDetail
	Err    error
	// Cached is true when the result came from the in-memory cache
	// (no git call ran, no debounce was applied).
	Cached bool
}

// NumStatResult is the message produced by LoadNumStat.
type NumStatResult struct {
	SHA    string
	Files  []gitcmd.FileStat
	Err    error
	Cached bool
	// Prefetch is true when the result came from LoadNumStatPrefetch,
	// so the model can distinguish background dim-state population from
	// selection-driven loads.
	Prefetch bool
}

// DiffResult is the message produced by LoadDiff. Raw is the full-file
// (-U99999) unified diff used for rendering; Hunks is the same diff at
// git's default context, used to identify real hunk boundaries inside
// Raw. For merge commits both fields hold the same `--cc` output.
type DiffResult struct {
	SHA    string
	Path   string
	Raw    string
	Hunks  string
	Err    error
	Cached bool
}

// diffEntry is the cache value for a (sha, path) pair: both the full-
// context render diff and the default-context hunks diff.
type diffEntry struct {
	Raw   string
	Hunks string
}

// New constructs a Loader from cfg. cfg.Source must be non-nil.
func New(cfg Config) *Loader {
	if cfg.Source == nil {
		panic("loader: Source is required")
	}
	deb := cfg.Debounce
	if deb < 0 {
		deb = 0
	}
	if cfg.Debounce == 0 {
		deb = DefaultDebounce
	}
	dCap := cfg.DetailCap
	if dCap <= 0 {
		dCap = DefaultDetailCap
	}
	nCap := cfg.NumStatCap
	if nCap <= 0 {
		nCap = DefaultNumStatCap
	}
	fCap := cfg.DiffCap
	if fCap <= 0 {
		fCap = DefaultDiffCap
	}
	return &Loader{
		src:      cfg.Source,
		debounce: deb,
		detail:   newLRU[string, gitcmd.CommitDetail](dCap),
		numstat:  newLRU[string, []gitcmd.FileStat](nCap),
		diff:     newLRU[diffKey, diffEntry](fCap),
	}
}

// LoadDetail returns a tea.Cmd that will deliver a DetailResult for
// sha. On cache hit, the cmd returns immediately without scheduling
// any git work or debounce. On miss, the cmd cancels any in-flight
// detail load, waits the debounce window, and then runs Source.Show.
// If a newer LoadDetail call happens during the debounce or git work,
// the in-flight context is cancelled and this cmd returns a result
// whose Err is the context error.
func (l *Loader) LoadDetail(sha string) tea.Cmd {
	if d, ok := l.detail.get(sha); ok {
		return func() tea.Msg {
			return DetailResult{SHA: sha, Detail: d, Cached: true}
		}
	}
	ctx := l.swapDetailCtx()
	deb := l.debounce
	return func() tea.Msg {
		if err := waitDebounce(ctx, deb); err != nil {
			return DetailResult{SHA: sha, Err: err}
		}
		d, err := l.src.Show(ctx, sha)
		if err == nil {
			l.detail.add(sha, d)
		}
		return DetailResult{SHA: sha, Detail: d, Err: err}
	}
}

// LoadNumStat returns a tea.Cmd that delivers a NumStatResult for sha.
// Cache, debounce, and cancellation semantics match LoadDetail.
func (l *Loader) LoadNumStat(sha string) tea.Cmd {
	if fs, ok := l.numstat.get(sha); ok {
		return func() tea.Msg {
			return NumStatResult{SHA: sha, Files: fs, Cached: true}
		}
	}
	ctx := l.swapNumStatCtx()
	deb := l.debounce
	return func() tea.Msg {
		if err := waitDebounce(ctx, deb); err != nil {
			return NumStatResult{SHA: sha, Err: err}
		}
		fs, err := l.src.NumStat(ctx, sha)
		if err == nil {
			l.numstat.add(sha, fs)
		}
		return NumStatResult{SHA: sha, Files: fs, Err: err}
	}
}

// LoadNumStatPrefetch returns a tea.Cmd that delivers a NumStatResult
// for sha without participating in the cancel/debounce protocol used by
// LoadNumStat. Intended for background dim-state population: it never
// cancels in-flight selection-driven loads and never gets cancelled by
// them. The emitted NumStatResult has Prefetch=true.
func (l *Loader) LoadNumStatPrefetch(sha string) tea.Cmd {
	if fs, ok := l.numstat.get(sha); ok {
		return func() tea.Msg {
			return NumStatResult{SHA: sha, Files: fs, Cached: true, Prefetch: true}
		}
	}
	return func() tea.Msg {
		ctx := context.Background()
		fs, err := l.src.NumStat(ctx, sha)
		if err == nil {
			l.numstat.add(sha, fs)
		}
		return NumStatResult{SHA: sha, Files: fs, Err: err, Prefetch: true}
	}
}

// LoadDiff returns a tea.Cmd that delivers a DiffResult for (sha,
// path). Cache, debounce, and cancellation semantics match LoadDetail.
func (l *Loader) LoadDiff(sha, path string) tea.Cmd {
	key := diffKey{SHA: sha, Path: path}
	if entry, ok := l.diff.get(key); ok {
		return func() tea.Msg {
			return DiffResult{SHA: sha, Path: path, Raw: entry.Raw, Hunks: entry.Hunks, Cached: true}
		}
	}
	ctx := l.swapDiffCtx()
	deb := l.debounce
	return func() tea.Msg {
		if err := waitDebounce(ctx, deb); err != nil {
			return DiffResult{SHA: sha, Path: path, Err: err}
		}
		type result struct {
			s   string
			err error
		}
		rawCh := make(chan result, 1)
		hunksCh := make(chan result, 1)
		go func() {
			s, err := l.src.Diff(ctx, sha, path)
			rawCh <- result{s, err}
		}()
		go func() {
			s, err := l.src.DiffHunks(ctx, sha, path)
			hunksCh <- result{s, err}
		}()
		rawR := <-rawCh
		hunksR := <-hunksCh
		err := rawR.err
		if err == nil {
			err = hunksR.err
		}
		if err == nil {
			l.diff.add(key, diffEntry{Raw: rawR.s, Hunks: hunksR.s})
		}
		return DiffResult{SHA: sha, Path: path, Raw: rawR.s, Hunks: hunksR.s, Err: err}
	}
}

// CancelDetail cancels any in-flight detail load (e.g. when the
// caller knows the selection has changed and doesn't yet have a new
// sha to request).
func (l *Loader) CancelDetail()  { l.swapDetailCtx() }
func (l *Loader) CancelNumStat() { l.swapNumStatCtx() }
func (l *Loader) CancelDiff()    { l.swapDiffCtx() }

// NumStatCached returns the cached numstat for sha if present, without
// triggering a fetch or affecting LRU ordering's effect on in-flight
// loads. Callers that want to evaluate commits in bulk (e.g. the file
// filter's commit-dim pass) use this to read whatever's already cached
// without disturbing the single-slot in-flight numstat fetch.
func (l *Loader) NumStatCached(sha string) ([]gitcmd.FileStat, bool) {
	return l.numstat.get(sha)
}

func (l *Loader) swapDetailCtx() context.Context {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.detailCancel != nil {
		l.detailCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.detailCancel = cancel
	return ctx
}

func (l *Loader) swapNumStatCtx() context.Context {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.numstatCancel != nil {
		l.numstatCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.numstatCancel = cancel
	return ctx
}

func (l *Loader) swapDiffCtx() context.Context {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.diffCancel != nil {
		l.diffCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.diffCancel = cancel
	return ctx
}

// waitDebounce blocks until either the debounce window elapses or the
// context is cancelled. Returns nil on natural elapse, ctx.Err() on
// cancellation. A non-positive debounce returns immediately, but still
// checks ctx so already-cancelled requests bail out.
func waitDebounce(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// --- LRU ---------------------------------------------------------------

// lru is a small map+doubly-linked-list LRU. It is not safe for
// concurrent use; the loader serializes access via tea.Cmd boundaries
// and the in-flight context guarantees that at most one writer for a
// given kind is running at a time.
type lru[K comparable, V any] struct {
	mu   sync.Mutex
	cap  int
	ll   *list.List
	idx  map[K]*list.Element
}

type lruEntry[K comparable, V any] struct {
	k K
	v V
}

func newLRU[K comparable, V any](cap int) *lru[K, V] {
	if cap < 1 {
		cap = 1
	}
	return &lru[K, V]{
		cap: cap,
		ll:  list.New(),
		idx: make(map[K]*list.Element, cap),
	}
}

func (c *lru[K, V]) get(k K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.idx[k]; ok {
		c.ll.MoveToFront(e)
		return e.Value.(*lruEntry[K, V]).v, true
	}
	var zero V
	return zero, false
}

func (c *lru[K, V]) add(k K, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.idx[k]; ok {
		e.Value.(*lruEntry[K, V]).v = v
		c.ll.MoveToFront(e)
		return
	}
	e := c.ll.PushFront(&lruEntry[K, V]{k: k, v: v})
	c.idx[k] = e
	if c.ll.Len() > c.cap {
		oldest := c.ll.Back()
		if oldest != nil {
			c.ll.Remove(oldest)
			delete(c.idx, oldest.Value.(*lruEntry[K, V]).k)
		}
	}
}

// len returns the current entry count; exposed for tests.
func (c *lru[K, V]) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

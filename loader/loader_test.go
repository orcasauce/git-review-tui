package loader

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/orcasauce/git-review-tui/gitcmd"
)

// fakeSource is a controllable Source for tests. Each call records its
// arguments and blocks on a per-call gate that the test can release;
// the call also honors ctx cancellation.
type fakeSource struct {
	mu sync.Mutex

	showCalls    []string
	numStatCalls []string
	diffCalls    []diffKey

	showGate    chan struct{}
	numStatGate chan struct{}
	diffGate    chan struct{}

	showResult    map[string]gitcmd.CommitDetail
	numStatResult map[string][]gitcmd.FileStat
	diffResult    map[diffKey]string
}

func newFakeSource() *fakeSource {
	return &fakeSource{
		showResult:    map[string]gitcmd.CommitDetail{},
		numStatResult: map[string][]gitcmd.FileStat{},
		diffResult:    map[diffKey]string{},
	}
}

func (f *fakeSource) Show(ctx context.Context, sha string) (gitcmd.CommitDetail, error) {
	f.mu.Lock()
	f.showCalls = append(f.showCalls, sha)
	gate := f.showGate
	res := f.showResult[sha]
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return gitcmd.CommitDetail{}, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return gitcmd.CommitDetail{}, err
	}
	return res, nil
}

func (f *fakeSource) NumStat(ctx context.Context, sha string) ([]gitcmd.FileStat, error) {
	f.mu.Lock()
	f.numStatCalls = append(f.numStatCalls, sha)
	gate := f.numStatGate
	res := f.numStatResult[sha]
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return res, nil
}

func (f *fakeSource) Diff(ctx context.Context, sha, path string) (string, error) {
	key := diffKey{SHA: sha, Path: path}
	f.mu.Lock()
	f.diffCalls = append(f.diffCalls, key)
	gate := f.diffGate
	res := f.diffResult[key]
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return res, nil
}

func (f *fakeSource) DiffHunks(ctx context.Context, sha, path string) (string, error) {
	key := diffKey{SHA: sha, Path: path}
	f.mu.Lock()
	gate := f.diffGate
	res := f.diffResult[key]
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return res, nil
}

func (f *fakeSource) showCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.showCalls)
}

func (f *fakeSource) numStatCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.numStatCalls)
}

func (f *fakeSource) diffCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.diffCalls)
}

// runMsg runs the tea.Cmd asynchronously and returns a channel that
// receives the produced tea.Msg.
func runMsg(cmd tea.Cmd) <-chan tea.Msg {
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	return ch
}

func TestDetailCacheHitSkipsSource(t *testing.T) {
	t.Parallel()
	f := newFakeSource()
	f.showResult["abc"] = gitcmd.CommitDetail{SHA: "abc", Body: "first"}
	l := New(Config{Source: f, Debounce: 1 * time.Millisecond})

	// First call: miss → goes through Source.
	res := (<-runMsg(l.LoadDetail("abc"))).(DetailResult)
	if res.Err != nil {
		t.Fatalf("first load: unexpected err %v", res.Err)
	}
	if res.Cached {
		t.Fatalf("first load should not be cached")
	}
	if got := f.showCallCount(); got != 1 {
		t.Fatalf("first load: source called %d times, want 1", got)
	}

	// Second call: hit → returns immediately, no second source call.
	res = (<-runMsg(l.LoadDetail("abc"))).(DetailResult)
	if !res.Cached {
		t.Fatalf("second load should be cached")
	}
	if res.Detail.Body != "first" {
		t.Fatalf("cached detail body = %q", res.Detail.Body)
	}
	if got := f.showCallCount(); got != 1 {
		t.Fatalf("second load: source called %d times, want 1", got)
	}
}

func TestDetailNewRequestCancelsInFlight(t *testing.T) {
	t.Parallel()
	f := newFakeSource()
	f.showGate = make(chan struct{})
	f.showResult["b"] = gitcmd.CommitDetail{SHA: "b", Body: "second"}
	// Long debounce so the first request is still inside its debounce
	// window when the second one swaps the context out. That way only
	// the second request ever reaches Source.
	l := New(Config{Source: f, Debounce: 200 * time.Millisecond})

	first := runMsg(l.LoadDetail("a"))
	// Yield so the first goroutine reliably starts its debounce wait.
	time.Sleep(10 * time.Millisecond)
	second := runMsg(l.LoadDetail("b"))

	// First load should return promptly with context.Canceled.
	select {
	case msg := <-first:
		r := msg.(DetailResult)
		if !errors.Is(r.Err, context.Canceled) {
			t.Fatalf("first load: err = %v, want context.Canceled", r.Err)
		}
		if r.SHA != "a" {
			t.Fatalf("first load: sha = %q, want a", r.SHA)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("first load did not return after cancellation")
	}

	// Release the second load's gate so it can finish.
	close(f.showGate)

	select {
	case msg := <-second:
		r := msg.(DetailResult)
		if r.Err != nil {
			t.Fatalf("second load: unexpected err %v", r.Err)
		}
		if r.Detail.Body != "second" {
			t.Fatalf("second detail body = %q", r.Detail.Body)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("second load did not complete")
	}

	// The first load was cancelled during debounce; only the second call
	// should have reached Source.
	if got := f.showCallCount(); got != 1 {
		t.Fatalf("source called %d times, want 1", got)
	}
	if calls := f.showCalls; len(calls) != 1 || calls[0] != "b" {
		t.Fatalf("source called with %v, want [b]", calls)
	}
}

func TestDetailMidFlightCancellationReturnsCtxErr(t *testing.T) {
	t.Parallel()
	f := newFakeSource()
	f.showGate = make(chan struct{})
	// Short debounce so the first goroutine reaches Source quickly and
	// blocks on the gate; the second LoadDetail then cancels the ctx,
	// unblocking it via ctx.Done().
	l := New(Config{Source: f, Debounce: 1 * time.Millisecond})

	first := runMsg(l.LoadDetail("a"))
	// Wait long enough for the first goroutine to clear debounce and
	// enter the source's gate select.
	time.Sleep(30 * time.Millisecond)
	_ = l.LoadDetail("b") // triggers the swap; we don't need to run the cmd

	select {
	case msg := <-first:
		r := msg.(DetailResult)
		if !errors.Is(r.Err, context.Canceled) {
			t.Fatalf("first load: err = %v, want context.Canceled", r.Err)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("mid-flight load did not return after cancellation")
	}
}

func TestDetailDebounceCoalesces(t *testing.T) {
	t.Parallel()
	f := newFakeSource()
	f.showResult["x"] = gitcmd.CommitDetail{SHA: "x"}
	f.showResult["y"] = gitcmd.CommitDetail{SHA: "y"}
	f.showResult["z"] = gitcmd.CommitDetail{SHA: "z"}
	// 50ms debounce; spam three loads back-to-back inside it.
	l := New(Config{Source: f, Debounce: 50 * time.Millisecond})

	a := runMsg(l.LoadDetail("x"))
	b := runMsg(l.LoadDetail("y"))
	c := runMsg(l.LoadDetail("z"))

	// Both early loads should be cancelled before reaching Source.
	for _, ch := range []<-chan tea.Msg{a, b} {
		select {
		case msg := <-ch:
			r := msg.(DetailResult)
			if !errors.Is(r.Err, context.Canceled) {
				t.Fatalf("early load err = %v, want context.Canceled", r.Err)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("early load did not return")
		}
	}

	// Final load completes successfully after debounce.
	select {
	case msg := <-c:
		r := msg.(DetailResult)
		if r.Err != nil {
			t.Fatalf("final load: unexpected err %v", r.Err)
		}
		if r.SHA != "z" {
			t.Fatalf("final load sha = %q", r.SHA)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("final load did not complete")
	}

	if got := f.showCallCount(); got != 1 {
		t.Fatalf("source called %d times, want 1 (coalesced)", got)
	}
}

func TestDiffCacheKeyedBySHAAndPath(t *testing.T) {
	t.Parallel()
	f := newFakeSource()
	f.diffResult[diffKey{SHA: "s1", Path: "a.go"}] = "diff-a"
	f.diffResult[diffKey{SHA: "s1", Path: "b.go"}] = "diff-b"
	l := New(Config{Source: f, Debounce: 1 * time.Millisecond})

	r1 := (<-runMsg(l.LoadDiff("s1", "a.go"))).(DiffResult)
	if r1.Raw != "diff-a" || r1.Cached {
		t.Fatalf("first a.go load: raw=%q cached=%v", r1.Raw, r1.Cached)
	}
	r2 := (<-runMsg(l.LoadDiff("s1", "b.go"))).(DiffResult)
	if r2.Raw != "diff-b" || r2.Cached {
		t.Fatalf("first b.go load: raw=%q cached=%v", r2.Raw, r2.Cached)
	}
	// Repeat both — both should now be cache hits.
	r3 := (<-runMsg(l.LoadDiff("s1", "a.go"))).(DiffResult)
	if !r3.Cached || r3.Raw != "diff-a" {
		t.Fatalf("a.go cache hit: raw=%q cached=%v", r3.Raw, r3.Cached)
	}
	r4 := (<-runMsg(l.LoadDiff("s1", "b.go"))).(DiffResult)
	if !r4.Cached || r4.Raw != "diff-b" {
		t.Fatalf("b.go cache hit: raw=%q cached=%v", r4.Raw, r4.Cached)
	}
	if got := f.diffCallCount(); got != 2 {
		t.Fatalf("source called %d times, want 2", got)
	}
}

func TestNumStatCancellation(t *testing.T) {
	t.Parallel()
	f := newFakeSource()
	f.numStatGate = make(chan struct{})
	f.numStatResult["sha2"] = []gitcmd.FileStat{{Status: "M", Path: "x"}}
	l := New(Config{Source: f, Debounce: 200 * time.Millisecond})

	first := runMsg(l.LoadNumStat("sha1"))
	time.Sleep(10 * time.Millisecond)
	second := runMsg(l.LoadNumStat("sha2"))

	r := (<-first).(NumStatResult)
	if !errors.Is(r.Err, context.Canceled) {
		t.Fatalf("first numstat: err = %v, want context.Canceled", r.Err)
	}

	close(f.numStatGate)
	r = (<-second).(NumStatResult)
	if r.Err != nil {
		t.Fatalf("second numstat err = %v", r.Err)
	}
	if len(r.Files) != 1 || r.Files[0].Path != "x" {
		t.Fatalf("second numstat files = %+v", r.Files)
	}
	if got := f.numStatCallCount(); got != 1 {
		t.Fatalf("source numstat called %d times, want 1", got)
	}
}

func TestLRUEvictsLeastRecentlyUsed(t *testing.T) {
	t.Parallel()
	c := newLRU[string, int](2)
	c.add("a", 1)
	c.add("b", 2)
	// touch a so b becomes the LRU
	if v, ok := c.get("a"); !ok || v != 1 {
		t.Fatalf("get a = %v %v", v, ok)
	}
	c.add("c", 3) // evicts b
	if _, ok := c.get("b"); ok {
		t.Fatalf("b should have been evicted")
	}
	if v, ok := c.get("a"); !ok || v != 1 {
		t.Fatalf("a should still be cached, got %v %v", v, ok)
	}
	if v, ok := c.get("c"); !ok || v != 3 {
		t.Fatalf("c should be cached, got %v %v", v, ok)
	}
	if got := c.len(); got != 2 {
		t.Fatalf("len = %d, want 2", got)
	}
}

func TestCancelDetailReleasesInFlight(t *testing.T) {
	t.Parallel()
	f := newFakeSource()
	f.showGate = make(chan struct{})
	l := New(Config{Source: f, Debounce: 1 * time.Millisecond})

	first := runMsg(l.LoadDetail("a"))
	time.Sleep(5 * time.Millisecond)
	l.CancelDetail()

	select {
	case msg := <-first:
		r := msg.(DetailResult)
		if !errors.Is(r.Err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", r.Err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("cancelled load did not return")
	}
}

func TestParallelLoadsDoNotInterfere(t *testing.T) {
	// Smoke test: many concurrent LoadDetail invocations on distinct
	// shas should each get their own result (no shared mutation of the
	// returned message). Uses cache hits to avoid debounce delays.
	t.Parallel()
	f := newFakeSource()
	for i := 0; i < 16; i++ {
		sha := string(rune('a' + i))
		f.showResult[sha] = gitcmd.CommitDetail{SHA: sha}
	}
	l := New(Config{Source: f, Debounce: 1 * time.Millisecond})
	// Prime cache so the parallel runs are all hits.
	for i := 0; i < 16; i++ {
		sha := string(rune('a' + i))
		<-runMsg(l.LoadDetail(sha))
	}

	var wg sync.WaitGroup
	var ok atomic.Int32
	for i := 0; i < 16; i++ {
		wg.Add(1)
		sha := string(rune('a' + i))
		go func() {
			defer wg.Done()
			r := (<-runMsg(l.LoadDetail(sha))).(DetailResult)
			if r.SHA == sha && r.Cached {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 16 {
		t.Fatalf("only %d/16 parallel loads succeeded", ok.Load())
	}
}

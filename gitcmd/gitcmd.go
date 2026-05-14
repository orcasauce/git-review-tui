// Package gitcmd is the single boundary to the git CLI. Every operation
// invokes `git -C <worktree-path> ...` and returns typed structs;
// callers never see raw porcelain text.
package gitcmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Client is scoped to a single worktree path. Construct one with New;
// switch worktrees by constructing a new Client (no chdir).
type Client struct {
	workTreePath string
}

// New returns a Client scoped to workTreePath.
func New(workTreePath string) *Client {
	return &Client{workTreePath: workTreePath}
}

// WorkTreePath returns the worktree path the client is scoped to.
func (c *Client) WorkTreePath() string { return c.workTreePath }

// TopLevel discovers the repository top-level starting from dir,
// walking upward like git itself does. Returns an error when dir is
// not inside a git working tree.
func TopLevel(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", &Error{Op: "rev-parse --show-toplevel", Err: err, Stderr: stderr.String()}
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Commit is one row of the log: enough to render a list row without
// any further git calls.
type Commit struct {
	SHA      string
	ShortSHA string
	Author   string
	Email    string
	// AuthorDateISO is the strict ISO-8601 author date, e.g. "2026-05-13T10:42:11-07:00".
	AuthorDateISO string
	// RelDate is git's own relative date, e.g. "3 days ago".
	RelDate string
	Subject string
	// Refs is the parsed list of decorations on the commit (branches,
	// HEAD, remote-tracking, tags). Empty when there are none.
	Refs []string
}

// HasHead reports whether the repository has at least one commit
// reachable from HEAD. A freshly-initialized repo with no commits
// returns false with no error. Anything else (permission failure,
// corrupted repo, unreachable HEAD ref) returns false plus a wrapped
// error so callers can distinguish "empty repo" from "broken repo".
func (c *Client) HasHead(ctx context.Context) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", c.workTreePath,
		"rev-parse", "--verify", "--quiet", "HEAD")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	// `--quiet` makes rev-parse exit 1 with no stderr when HEAD does
	// not resolve (the standard "no commits yet" case). Any other
	// failure (non-zero exit + stderr, or non-exit error like context
	// cancellation) is treated as a real error.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && stderr.Len() == 0 {
		return false, nil
	}
	return false, &Error{Op: "rev-parse --verify HEAD", Err: err, Stderr: stderr.String()}
}

// Log returns up to limit commits starting at skip, in reverse-
// chronological order from HEAD. limit <= 0 means "use the default".
func (c *Client) Log(ctx context.Context, skip, limit int) ([]Commit, error) {
	if limit <= 0 {
		limit = 500
	}
	// Use ASCII unit (0x1F) between fields and record (0x1E) between
	// commits so newlines and other punctuation in subjects survive.
	const sep = "\x1f"
	const rec = "\x1e"
	format := strings.Join([]string{"%H", "%h", "%an", "%ae", "%aI", "%ar", "%s", "%D"}, sep) + rec
	args := []string{
		"-C", c.workTreePath,
		"log", "HEAD",
		fmt.Sprintf("--skip=%d", skip),
		fmt.Sprintf("--max-count=%d", limit),
		"--pretty=format:" + format,
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, &Error{Op: "log HEAD", Err: err, Stderr: stderr.String()}
	}
	return parseLog(stdout.Bytes()), nil
}

func parseLog(out []byte) []Commit {
	// Each record ends in 0x1E. The last record may or may not have a
	// trailing 0x1E (git omits a trailing separator at the end of
	// --pretty=format output), so split and drop empties.
	records := bytes.Split(out, []byte{0x1e})
	commits := make([]Commit, 0, len(records))
	for _, r := range records {
		r = bytes.TrimLeft(r, "\n")
		if len(r) == 0 {
			continue
		}
		fields := bytes.Split(r, []byte{0x1f})
		if len(fields) < 8 {
			continue
		}
		commits = append(commits, Commit{
			SHA:           string(fields[0]),
			ShortSHA:      string(fields[1]),
			Author:        string(fields[2]),
			Email:         string(fields[3]),
			AuthorDateISO: string(fields[4]),
			RelDate:       string(fields[5]),
			Subject:       string(fields[6]),
			Refs:          parseRefs(string(fields[7])),
		})
	}
	return commits
}

func parseRefs(decoration string) []string {
	decoration = strings.TrimSpace(decoration)
	if decoration == "" {
		return nil
	}
	parts := strings.Split(decoration, ", ")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// RefKind buckets a decoration token by its origin so callers can
// style HEAD, local branches, remote-tracking branches, and tags
// distinctly (and, where appropriate, omit one kind entirely).
type RefKind int

const (
	// RefLocal is a local branch (e.g., "main", "feature").
	RefLocal RefKind = iota
	// RefHEAD is HEAD itself, including the "HEAD -> branch" form
	// git emits when HEAD points at a local branch.
	RefHEAD
	// RefRemote is a remote-tracking branch (e.g., "origin/main").
	RefRemote
	// RefTag is a tag decoration ("tag: vX.Y").
	RefTag
)

// ClassifyRef classifies one decoration token. The HEAD check is
// intentionally a prefix match so "HEAD" and "HEAD -> main" both come
// back as RefHEAD. Remote-tracking branches are detected by the
// presence of a "/" separator (which is git's standard format for
// remote refs and cannot occur in a local branch name).
func ClassifyRef(decoration string) RefKind {
	d := strings.TrimSpace(decoration)
	if strings.HasPrefix(d, "tag:") {
		return RefTag
	}
	if d == "HEAD" || strings.HasPrefix(d, "HEAD ") || strings.HasPrefix(d, "HEAD\t") {
		return RefHEAD
	}
	if strings.Contains(d, "/") {
		return RefRemote
	}
	return RefLocal
}

// CommitDetail is the full detail of a single commit, suitable for
// rendering in the message panel: identity, dates, parents, ref
// decorations, and the full raw message body (subject + body, blank-
// line separated as authored).
type CommitDetail struct {
	SHA              string
	ShortSHA         string
	AuthorName       string
	AuthorEmail      string
	AuthorDateISO    string
	AuthorDateRel    string
	CommitterName    string
	CommitterEmail   string
	CommitterDateISO string
	CommitterDateRel string
	Parents          []string
	Refs             []string
	// Tags is the list of tag refs pointing at this commit, with the
	// annotated-tag message body extracted when present. Lightweight
	// tags have Annotated=false and Message="".
	Tags []TagInfo
	// Body is the full raw commit message (%B): subject on the first
	// line(s), then a blank line, then the body. Trailing newlines
	// trimmed.
	Body string
}

// TagInfo is a single tag pointing at a commit. Annotated tags
// (`git tag -a`) carry a tag object with its own message; lightweight
// tags (`git tag`) are plain refs and carry no message of their own.
type TagInfo struct {
	Name      string
	Annotated bool
	// Message is the annotated tag's body (subject + body as authored,
	// trailing newlines trimmed). Empty for lightweight tags.
	Message string
}

// Show returns the full CommitDetail for sha.
func (c *Client) Show(ctx context.Context, sha string) (CommitDetail, error) {
	const sep = "\x1f"
	// %B (raw body) is placed last so any embedded 0x1F or newlines
	// inside the body do not confuse the split.
	format := strings.Join([]string{
		"%H", "%h",
		"%an", "%ae", "%aI", "%ar",
		"%cn", "%ce", "%cI", "%cr",
		"%P", "%D",
	}, sep) + sep + "%B"
	args := []string{
		"-C", c.workTreePath,
		"show", "-s",
		"--pretty=format:" + format,
		sha,
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return CommitDetail{}, &Error{Op: "show -s " + sha, Err: err, Stderr: stderr.String()}
	}
	d, err := parseShow(stdout.String())
	if err != nil {
		return CommitDetail{}, err
	}
	tags, err := c.Tags(ctx, sha)
	if err != nil {
		return CommitDetail{}, err
	}
	d.Tags = tags
	return d, nil
}

// Tags returns the tag refs pointing at sha, with annotated-tag
// messages extracted when present. The result is empty when no tags
// point at sha. Tags are returned in `for-each-ref` order (typically
// alphabetical by full refname).
func (c *Client) Tags(ctx context.Context, sha string) ([]TagInfo, error) {
	const sep = "\x1f"
	const rec = "\x1e"
	format := strings.Join([]string{"%(refname:short)", "%(objecttype)", "%(contents)"}, sep) + rec
	cmd := exec.CommandContext(ctx, "git",
		"-C", c.workTreePath,
		"for-each-ref", "--points-at="+sha, "refs/tags/",
		"--format="+format,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, &Error{Op: "for-each-ref --points-at " + sha, Err: err, Stderr: stderr.String()}
	}
	return parseTags(stdout.Bytes()), nil
}

func parseTags(out []byte) []TagInfo {
	records := bytes.Split(out, []byte{0x1e})
	var tags []TagInfo
	for _, r := range records {
		r = bytes.TrimLeft(r, "\n")
		if len(r) == 0 {
			continue
		}
		fields := bytes.SplitN(r, []byte{0x1f}, 3)
		if len(fields) < 3 {
			continue
		}
		info := TagInfo{Name: string(fields[0])}
		if string(fields[1]) == "tag" {
			info.Annotated = true
			info.Message = strings.TrimRight(string(fields[2]), "\n")
		}
		tags = append(tags, info)
	}
	return tags
}

func parseShow(out string) (CommitDetail, error) {
	// 13 fields total: 12 fixed + 1 body. Use SplitN so the body keeps
	// any embedded 0x1F bytes intact.
	parts := strings.SplitN(out, "\x1f", 13)
	if len(parts) < 13 {
		return CommitDetail{}, fmt.Errorf("gitcmd.Show: unexpected output (%d fields): %q", len(parts), out)
	}
	return CommitDetail{
		SHA:              parts[0],
		ShortSHA:         parts[1],
		AuthorName:       parts[2],
		AuthorEmail:      parts[3],
		AuthorDateISO:    parts[4],
		AuthorDateRel:    parts[5],
		CommitterName:    parts[6],
		CommitterEmail:   parts[7],
		CommitterDateISO: parts[8],
		CommitterDateRel: parts[9],
		Parents:          parseParents(parts[10]),
		Refs:             parseRefs(parts[11]),
		Body:             strings.TrimRight(parts[12], "\n"),
	}, nil
}

func parseParents(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return strings.Fields(s)
}

// FileStat is one row of file changes for a commit, as produced by
// numstat + raw together. Status is a single letter (A/M/D/R/C/T/...);
// for renames and copies, OldPath is the source and Path is the
// destination. Added and Deleted are line counts; IsBinary is set for
// binary files (in which case Added and Deleted are zero).
type FileStat struct {
	Status   string
	Path     string
	OldPath  string
	Added    int
	Deleted  int
	IsBinary bool
}

// NumStat returns the per-file change records for sha, including
// rename detection and binary-file marking. The root commit is handled
// naturally — every file appears as added. Merge commits (multi-parent)
// are routed through the combined-diff (`--cc`) codepath; a clean merge
// with no conflict-resolution edits returns an empty slice.
func (c *Client) NumStat(ctx context.Context, sha string) ([]FileStat, error) {
	parents, err := c.commitParents(ctx, sha)
	if err != nil {
		return nil, err
	}
	if len(parents) > 1 {
		return c.numStatMerge(ctx, sha)
	}
	args := []string{
		"-C", c.workTreePath,
		"diff-tree", "--no-commit-id", "--root", "-r", "-M",
		"--raw", "--numstat",
		sha,
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, &Error{Op: "diff-tree --raw --numstat " + sha, Err: err, Stderr: stderr.String()}
	}
	return parseNumStat(stdout.String())
}

// commitParents returns the parent SHAs of sha (empty for the root
// commit, one for a normal commit, two or more for a merge).
func (c *Client) commitParents(ctx context.Context, sha string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git",
		"-C", c.workTreePath,
		"show", "-s", "--format=%P", sha,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, &Error{Op: "show -s --format=%P " + sha, Err: err, Stderr: stderr.String()}
	}
	return strings.Fields(strings.TrimSpace(stdout.String())), nil
}

// numStatMerge produces the file list for a merge commit via combined
// diff (`--cc`), which restricts output to files that received
// conflict-resolution edits. Files come back with Status="M" since the
// combined raw format's per-parent status field doesn't map cleanly to
// a single letter; the line counts are the merged diff's added/deleted.
func (c *Client) numStatMerge(ctx context.Context, sha string) ([]FileStat, error) {
	args := []string{
		"-C", c.workTreePath,
		"show", "--cc", "--numstat", "--format=", sha,
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, &Error{Op: "show --cc --numstat " + sha, Err: err, Stderr: stderr.String()}
	}
	var files []FileStat
	for _, line := range strings.Split(stdout.String(), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			return nil, fmt.Errorf("gitcmd.NumStat (merge): unexpected numstat: %q", line)
		}
		f := FileStat{Status: "M", Path: parts[2]}
		if parts[0] == "-" && parts[1] == "-" {
			f.IsBinary = true
		} else {
			a, err := strconv.Atoi(parts[0])
			if err != nil {
				return nil, fmt.Errorf("gitcmd.NumStat (merge): parse added: %w", err)
			}
			d, err := strconv.Atoi(parts[1])
			if err != nil {
				return nil, fmt.Errorf("gitcmd.NumStat (merge): parse deleted: %w", err)
			}
			f.Added = a
			f.Deleted = d
		}
		files = append(files, f)
	}
	return files, nil
}

type rawRec struct {
	status  string
	path    string
	oldPath string
}

type numRec struct {
	added   int
	deleted int
	binary  bool
}

func parseNumStat(out string) ([]FileStat, error) {
	var raws []rawRec
	var nums []numRec
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if line[0] == ':' {
			r, err := parseRawLine(line)
			if err != nil {
				return nil, err
			}
			raws = append(raws, r)
			continue
		}
		n, err := parseNumstatLine(line)
		if err != nil {
			return nil, err
		}
		nums = append(nums, n)
	}
	// Zip raw and numstat records; they appear in the same order. If
	// counts diverge (rare; e.g., one section truncated), fall back to
	// whichever data we have so the caller still sees the files.
	max := len(raws)
	if len(nums) > max {
		max = len(nums)
	}
	files := make([]FileStat, 0, max)
	for i := 0; i < max; i++ {
		f := FileStat{}
		if i < len(raws) {
			f.Status = raws[i].status
			f.Path = raws[i].path
			f.OldPath = raws[i].oldPath
		}
		if i < len(nums) {
			f.Added = nums[i].added
			f.Deleted = nums[i].deleted
			f.IsBinary = nums[i].binary
		}
		files = append(files, f)
	}
	return files, nil
}

func parseRawLine(line string) (rawRec, error) {
	// ":<srcMode> <dstMode> <srcSha> <dstSha> <status>\t<path>[\t<newPath>]"
	tab := strings.IndexByte(line, '\t')
	if tab < 0 {
		return rawRec{}, fmt.Errorf("gitcmd.NumStat: unexpected raw line: %q", line)
	}
	meta := line[1:tab]
	rest := line[tab+1:]
	fields := strings.Fields(meta)
	if len(fields) < 5 {
		return rawRec{}, fmt.Errorf("gitcmd.NumStat: unexpected raw meta: %q", meta)
	}
	status := fields[4]
	letter := string(status[0])
	r := rawRec{status: letter}
	if letter == "R" || letter == "C" {
		parts := strings.SplitN(rest, "\t", 2)
		if len(parts) != 2 {
			return rawRec{}, fmt.Errorf("gitcmd.NumStat: rename without new path: %q", line)
		}
		r.oldPath = parts[0]
		r.path = parts[1]
		return r, nil
	}
	r.path = rest
	return r, nil
}

func parseNumstatLine(line string) (numRec, error) {
	// "<added>\t<deleted>\t<path>" — for binaries, "-\t-\t<path>".
	parts := strings.SplitN(line, "\t", 3)
	if len(parts) < 3 {
		return numRec{}, fmt.Errorf("gitcmd.NumStat: unexpected numstat: %q", line)
	}
	if parts[0] == "-" && parts[1] == "-" {
		return numRec{binary: true}, nil
	}
	a, err := strconv.Atoi(parts[0])
	if err != nil {
		return numRec{}, fmt.Errorf("gitcmd.NumStat: parse added: %w", err)
	}
	d, err := strconv.Atoi(parts[1])
	if err != nil {
		return numRec{}, fmt.Errorf("gitcmd.NumStat: parse deleted: %w", err)
	}
	return numRec{added: a, deleted: d}, nil
}

// Diff returns the full-file unified diff (-U99999) for the given path
// at the given sha, with rename detection enabled. For merge commits
// (multi-parent) the combined diff (`--cc`) is returned instead, which
// shows only the conflict-resolution edits relative to all parents. The
// returned text is the raw diff as emitted by git (including
// "diff --git", "---", "+++", and "@@" header lines for normal commits,
// or "@@@" combined headers for merges); callers are expected to parse
// it into rendered rows via the diffrender package.
func (c *Client) Diff(ctx context.Context, sha, path string) (string, error) {
	parents, err := c.commitParents(ctx, sha)
	if err != nil {
		return "", err
	}
	var args []string
	if len(parents) > 1 {
		args = []string{
			"-C", c.workTreePath,
			"show", "--cc", "--format=", "-M", "--no-color",
			sha, "--", path,
		}
	} else {
		args = []string{
			"-C", c.workTreePath,
			"show", "--format=", "-U99999", "-M", "--no-color",
			sha, "--", path,
		}
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", &Error{Op: "show " + sha + " -- " + path, Err: err, Stderr: stderr.String()}
	}
	return stdout.String(), nil
}

// FileSize returns the byte size of the blob at sha:path via
// `git cat-file -s <sha>:<path>`. Returns a wrapped *Error when the
// path does not exist at the given sha (e.g., a freshly-added file
// looked up at its parent, or a deleted file looked up at its child).
// Callers handling binary file deltas check the error to decide
// whether one side of the change is missing.
func (c *Client) FileSize(ctx context.Context, sha, path string) (int64, error) {
	cmd := exec.CommandContext(ctx, "git",
		"-C", c.workTreePath,
		"cat-file", "-s", sha+":"+path,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, &Error{Op: "cat-file -s " + sha + ":" + path, Err: err, Stderr: stderr.String()}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(stdout.String()), 10, 64)
	if err != nil {
		return 0, &Error{Op: "cat-file -s " + sha + ":" + path, Err: err}
	}
	return n, nil
}

// Worktree is one entry from `git worktree list --porcelain`. Path is
// the worktree's top-level directory. Branch is the short branch name
// the worktree has checked out, empty when the worktree is detached or
// bare. HeadSHA is the full sha at HEAD. Current is true for the
// worktree that this Client is scoped to.
type Worktree struct {
	Path     string
	Branch   string
	HeadSHA  string
	Current  bool
	Detached bool
	Bare     bool
}

// Worktrees returns every worktree registered with the repository,
// marking the one this Client is scoped to as Current. The path
// comparison uses filepath.Clean on both sides so trailing slashes and
// minor path-shape differences do not de-mark the current worktree.
func (c *Client) Worktrees(ctx context.Context) ([]Worktree, error) {
	cmd := exec.CommandContext(ctx, "git",
		"-C", c.workTreePath,
		"worktree", "list", "--porcelain",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, &Error{Op: "worktree list --porcelain", Err: err, Stderr: stderr.String()}
	}
	wts := parseWorktrees(stdout.String())
	want := canonicalPath(c.workTreePath)
	for i := range wts {
		if canonicalPath(wts[i].Path) == want {
			wts[i].Current = true
		}
	}
	return wts, nil
}

// canonicalPath returns a path comparison key: symlinks resolved when
// possible (so macOS /tmp vs /private/tmp matches), trailing slash
// stripped, and falls back to the original string when EvalSymlinks
// fails (e.g. a worktree that no longer exists on disk).
func canonicalPath(p string) string {
	if len(p) > 1 && p[len(p)-1] == '/' {
		p = p[:len(p)-1]
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

func parseWorktrees(out string) []Worktree {
	// Records are separated by blank lines; each non-blank line is
	// "<key> <value>" or a bare key like "detached" / "bare".
	var (
		result []Worktree
		cur    Worktree
		have   bool
	)
	flush := func() {
		if have {
			result = append(result, cur)
		}
		cur = Worktree{}
		have = false
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			flush()
			continue
		}
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			cur.Path = val
			have = true
		case "HEAD":
			cur.HeadSHA = val
		case "branch":
			cur.Branch = strings.TrimPrefix(val, "refs/heads/")
		case "detached":
			cur.Detached = true
		case "bare":
			cur.Bare = true
		}
	}
	flush()
	return result
}

// IsMerge reports whether sha names a merge commit (two or more
// parents). The check is a thin wrapper around
// `git rev-list --no-walk --merges <sha>`, which prints sha when it is
// a merge and nothing otherwise. A non-existent or malformed SHA
// produces a wrapped *Error.
func (c *Client) IsMerge(ctx context.Context, sha string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git",
		"-C", c.workTreePath,
		"rev-list", "--no-walk", "--merges", sha,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return false, &Error{Op: "rev-list --no-walk --merges " + sha, Err: err, Stderr: stderr.String()}
	}
	return strings.TrimSpace(stdout.String()) != "", nil
}

// IsRootCommit reports whether sha is the repository's root commit
// (has no parents). Implementation runs
// `git rev-list --parents -n 1 <sha>` and checks that the single output
// line carries exactly one token (the sha itself, with no parents).
func (c *Client) IsRootCommit(ctx context.Context, sha string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git",
		"-C", c.workTreePath,
		"rev-list", "--parents", "-n", "1", sha,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return false, &Error{Op: "rev-list --parents -n 1 " + sha, Err: err, Stderr: stderr.String()}
	}
	fields := strings.Fields(strings.TrimSpace(stdout.String()))
	return len(fields) == 1, nil
}

// StatusInfo summarises the repository state needed to gate destructive
// operations like rebase. Clean is true when there are no modified,
// staged, or conflicted tracked files (untracked files are ignored).
// HeadBranch is the short branch name HEAD points at, empty when HEAD
// is detached. BranchCheckedOutAt is the path of another worktree that
// also has HeadBranch checked out, empty when no such worktree exists.
// UnmergedPaths lists the paths of files with conflict markers in the
// index — populated mid-rebase / mid-merge.
type StatusInfo struct {
	Clean              bool
	HeadBranch         string
	BranchCheckedOutAt string
	UnmergedPaths      []string
}

// Status reports current worktree state. The implementation issues
// `git status --porcelain`, `git symbolic-ref HEAD`, and (when on a
// branch) `git worktree list --porcelain` to assemble the result.
func (c *Client) Status(ctx context.Context) (StatusInfo, error) {
	var info StatusInfo
	// Tracked-file dirty check + unmerged extraction.
	cmd := exec.CommandContext(ctx, "git",
		"-C", c.workTreePath,
		"status", "--porcelain", "--untracked-files=no",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return StatusInfo{}, &Error{Op: "status --porcelain", Err: err, Stderr: stderr.String()}
	}
	statusOut := stdout.String()
	info.Clean = strings.TrimSpace(statusOut) == ""
	for _, line := range strings.Split(statusOut, "\n") {
		if len(line) < 4 {
			continue
		}
		x, y := line[0], line[1]
		// Unmerged combinations per git-status(1).
		unmerged := (x == 'U' || y == 'U') ||
			(x == 'A' && y == 'A') ||
			(x == 'D' && y == 'D')
		if unmerged {
			info.UnmergedPaths = append(info.UnmergedPaths, strings.TrimSpace(line[3:]))
		}
	}
	// HEAD branch / detached.
	cmd = exec.CommandContext(ctx, "git",
		"-C", c.workTreePath,
		"symbolic-ref", "--short", "--quiet", "HEAD",
	)
	stdout.Reset()
	stderr.Reset()
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		info.HeadBranch = strings.TrimSpace(stdout.String())
	} else {
		var exitErr *exec.ExitError
		if !(errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && stderr.Len() == 0) {
			return StatusInfo{}, &Error{Op: "symbolic-ref HEAD", Err: err, Stderr: stderr.String()}
		}
		// Detached HEAD: leave HeadBranch empty.
	}
	// Branch-also-checked-out-elsewhere lookup.
	if info.HeadBranch != "" {
		wts, err := c.Worktrees(ctx)
		if err != nil {
			return StatusInfo{}, err
		}
		for _, w := range wts {
			if !w.Current && w.Branch == info.HeadBranch {
				info.BranchCheckedOutAt = w.Path
				break
			}
		}
	}
	return info, nil
}

// CommitExists reports whether sha resolves to a reachable object in
// the repository. Thin wrapper around `git cat-file -e <sha>`. Any
// failure other than "object missing" is returned as a wrapped *Error.
func (c *Client) CommitExists(ctx context.Context, sha string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git",
		"-C", c.workTreePath,
		"cat-file", "-e", sha,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, &Error{Op: "cat-file -e " + sha, Err: err, Stderr: stderr.String()}
}

// RebaseState identifies the post-invocation state of a rebase command.
// RebaseDone means the rebase finished cleanly; RebaseHalted means the
// rebase stopped (typically on a conflict) and the repo is in a
// mid-rebase state requiring a follow-up (--continue / --abort);
// RebaseError means git exited non-zero and the repo is NOT mid-rebase.
type RebaseState int

const (
	// RebaseDone means git rebase exited zero and HEAD has advanced.
	RebaseDone RebaseState = iota
	// RebaseHalted means git rebase exited non-zero but left a mid-rebase
	// state on disk (e.g. .git/rebase-merge/), typically due to a conflict.
	RebaseHalted
	// RebaseError means git rebase exited non-zero with no mid-rebase
	// state — i.e. a generic failure that did not start, or rolled back.
	RebaseError
)

// RebaseResult describes the outcome of a RebaseDropStart /
// RebaseContinue invocation. The fields populated depend on State.
type RebaseResult struct {
	State           RebaseState
	HaltSHA         string
	HaltSubject     string
	ConflictedPaths []string
	Progress        string
	Stderr          string
}

// RebaseDropStart runs `git rebase -i --rebase-merges <base>` with a
// generated sequence-editor script that rewrites every `pick <sha>`
// todo line whose SHA is in marked to `drop <sha>`. base is the parent
// of the oldest marked commit (the commit with the lowest rev-list
// count). The marked slice may be unordered; the function determines
// the rebase base itself.
//
// On a clean completion the returned RebaseResult has State=RebaseDone.
// On a conflict halt, State=RebaseHalted with HaltSHA, HaltSubject, and
// ConflictedPaths populated. On any other failure, State=RebaseError
// with Stderr populated.
func (c *Client) RebaseDropStart(ctx context.Context, marked []string) (RebaseResult, error) {
	if len(marked) == 0 {
		return RebaseResult{}, errors.New("gitcmd.RebaseDropStart: empty marked set")
	}
	// Find the oldest marked sha: smallest `git rev-list --count <sha>`.
	oldest := ""
	minCount := -1
	for _, sha := range marked {
		out, err := c.revListCount(ctx, sha)
		if err != nil {
			return RebaseResult{}, err
		}
		if minCount < 0 || out < minCount {
			minCount = out
			oldest = sha
		}
	}
	// Generate the sequence-editor shell script into a temp dir.
	tmpdir, err := os.MkdirTemp("", "git-review-tui-rebase-")
	if err != nil {
		return RebaseResult{}, &Error{Op: "mkdir tempdir", Err: err}
	}
	defer os.RemoveAll(tmpdir)
	scriptPath := filepath.Join(tmpdir, "sequence-editor.sh")
	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	script.WriteString("todofile=\"$1\"\n")
	script.WriteString("sed ")
	for _, sha := range marked {
		// Only `pick` lines whose first SHA token matches one of the
		// marked SHAs are rewritten to `drop`. `--abbrev=40` below
		// guarantees the todo lists full SHAs so this match is exact.
		script.WriteString("-e 's/^pick " + sha + "/drop " + sha + "/' ")
	}
	script.WriteString("\"$todofile\" > \"$todofile.new\" && mv \"$todofile.new\" \"$todofile\"\n")
	if err := os.WriteFile(scriptPath, []byte(script.String()), 0o755); err != nil {
		return RebaseResult{}, &Error{Op: "write sequence editor", Err: err}
	}
	// Invoke git rebase with the generated script wired in as the
	// sequence editor. `core.abbrev=40` forces full SHAs in the todo
	// file. GIT_EDITOR=true ensures any unexpected editor prompt
	// (e.g. a reword step) returns immediately rather than blocking.
	base := oldest + "~1"
	cmd := exec.CommandContext(ctx, "git",
		"-C", c.workTreePath,
		"-c", "core.abbrev=40",
		"-c", "sequence.editor="+scriptPath,
		"rebase", "-i", "--rebase-merges", base,
	)
	cmd.Env = append(os.Environ(), "GIT_EDITOR=true")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	result := RebaseResult{Stderr: stderr.String()}
	if runErr == nil {
		result.State = RebaseDone
		return result, nil
	}
	// Rebase exited non-zero. Distinguish halt (mid-rebase state on
	// disk) from a generic error.
	inProgress, _ := c.rebaseInProgress(ctx)
	if !inProgress {
		result.State = RebaseError
		return result, nil
	}
	result.State = RebaseHalted
	if haltSHA, _ := c.readStoppedSHA(ctx); haltSHA != "" {
		result.HaltSHA = haltSHA
		if subj, err := c.commitSubject(ctx, haltSHA); err == nil {
			result.HaltSubject = subj
		}
	}
	if info, err := c.Status(ctx); err == nil {
		result.ConflictedPaths = info.UnmergedPaths
	}
	return result, nil
}

// ConflictSide picks which side of a conflict to take when resolving
// in bulk via `git checkout --theirs|--ours`.
type ConflictSide int

const (
	// SideTheirs takes the version from the commit being applied (the
	// commit the interactive rebase halted on).
	SideTheirs ConflictSide = iota
	// SideOurs takes the version from the rebase base / accumulated
	// reapply state.
	SideOurs
)

// CheckoutSide resolves every path in paths to the given side and stages
// the result. Concretely: runs `git checkout --theirs|--ours <paths...>`
// followed by `git add <paths...>`. Used by the conflict popup's
// [t] / [o] resolution handlers.
func (c *Client) CheckoutSide(ctx context.Context, side ConflictSide, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	flag := "--theirs"
	if side == SideOurs {
		flag = "--ours"
	}
	args := append([]string{"-C", c.workTreePath, "checkout", flag, "--"}, paths...)
	cmd := exec.CommandContext(ctx, "git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return &Error{Op: "checkout " + flag, Err: err, Stderr: stderr.String()}
	}
	addArgs := append([]string{"-C", c.workTreePath, "add", "--"}, paths...)
	addCmd := exec.CommandContext(ctx, "git", addArgs...)
	var addStderr bytes.Buffer
	addCmd.Stderr = &addStderr
	if err := addCmd.Run(); err != nil {
		return &Error{Op: "add", Err: err, Stderr: addStderr.String()}
	}
	return nil
}

// RebaseContinue runs `git rebase --continue` and reports the resulting
// state with the same RebaseResult semantics as RebaseDropStart: Done
// when the rebase completed, Halted when it stopped on another conflict,
// Error when it bailed out without leaving a mid-rebase state.
func (c *Client) RebaseContinue(ctx context.Context) (RebaseResult, error) {
	cmd := exec.CommandContext(ctx, "git",
		"-C", c.workTreePath,
		"-c", "core.abbrev=40",
		"rebase", "--continue",
	)
	// GIT_EDITOR=true short-circuits any commit-message editor prompt
	// `rebase --continue` might want to open (e.g. for a reworded commit
	// or a commit-message edit step), so the call never blocks.
	cmd.Env = append(os.Environ(), "GIT_EDITOR=true")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	result := RebaseResult{Stderr: stderr.String()}
	if runErr == nil {
		result.State = RebaseDone
		return result, nil
	}
	inProgress, _ := c.rebaseInProgress(ctx)
	if !inProgress {
		result.State = RebaseError
		return result, nil
	}
	result.State = RebaseHalted
	if haltSHA, _ := c.readStoppedSHA(ctx); haltSHA != "" {
		result.HaltSHA = haltSHA
		if subj, err := c.commitSubject(ctx, haltSHA); err == nil {
			result.HaltSubject = subj
		}
	}
	if info, err := c.Status(ctx); err == nil {
		result.ConflictedPaths = info.UnmergedPaths
	}
	return result, nil
}

// RebaseAbort runs `git rebase --abort` when a rebase is in progress,
// no-op (returns nil) otherwise. Used both for explicit cancellation
// (esc on the blocking modal) and for auto-rollback on rebase errors.
func (c *Client) RebaseAbort(ctx context.Context) error {
	inProgress, err := c.rebaseInProgress(ctx)
	if err != nil {
		return err
	}
	if !inProgress {
		return nil
	}
	cmd := exec.CommandContext(ctx, "git",
		"-C", c.workTreePath,
		"rebase", "--abort",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return &Error{Op: "rebase --abort", Err: err, Stderr: stderr.String()}
	}
	return nil
}

// rebaseInProgress reports whether the repository currently has a
// mid-rebase state on disk (either `.git/rebase-merge/` for interactive
// rebases or `.git/rebase-apply/` for am-style rebases).
func (c *Client) rebaseInProgress(ctx context.Context) (bool, error) {
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		p, err := c.gitPath(ctx, name)
		if err != nil {
			return false, err
		}
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return true, nil
		}
	}
	return false, nil
}

// gitPath returns the absolute path of a file inside the .git
// directory, resolving the path relative to the worktree when git
// returns a relative one. Used for direct filesystem inspection of
// rebase state files.
func (c *Client) gitPath(ctx context.Context, name string) (string, error) {
	cmd := exec.CommandContext(ctx, "git",
		"-C", c.workTreePath,
		"rev-parse", "--git-path", name,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", &Error{Op: "rev-parse --git-path " + name, Err: err, Stderr: stderr.String()}
	}
	p := strings.TrimSpace(stdout.String())
	if p == "" {
		return "", nil
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(c.workTreePath, p)
	}
	return p, nil
}

// revListCount returns `git rev-list --count <sha>` — the number of
// commits reachable from sha (inclusive). The oldest commit in any
// linear-ish set has the smallest count.
func (c *Client) revListCount(ctx context.Context, sha string) (int, error) {
	cmd := exec.CommandContext(ctx, "git",
		"-C", c.workTreePath,
		"rev-list", "--count", sha,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, &Error{Op: "rev-list --count " + sha, Err: err, Stderr: stderr.String()}
	}
	n, err := strconv.Atoi(strings.TrimSpace(stdout.String()))
	if err != nil {
		return 0, &Error{Op: "rev-list --count " + sha, Err: err}
	}
	return n, nil
}

// readStoppedSHA returns the SHA recorded in
// `.git/rebase-merge/stopped-sha` (the commit the interactive rebase
// halted on). Returns empty string with no error when the file is
// absent.
func (c *Client) readStoppedSHA(ctx context.Context) (string, error) {
	p, err := c.gitPath(ctx, "rebase-merge/stopped-sha")
	if err != nil {
		return "", err
	}
	if p == "" {
		return "", nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		// Missing file is not an error — the rebase may not have
		// recorded a stopped-sha yet (e.g. halt before the first pick).
		return "", nil
	}
	// stopped-sha is sometimes abbreviated; expand to full sha when
	// possible so callers always see a 40-hex SHA.
	short := strings.TrimSpace(string(b))
	if short == "" {
		return "", nil
	}
	if len(short) == 40 {
		return short, nil
	}
	cmd := exec.CommandContext(ctx, "git",
		"-C", c.workTreePath,
		"rev-parse", short,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return short, nil
	}
	return strings.TrimSpace(stdout.String()), nil
}

// commitSubject returns the subject line of sha.
func (c *Client) commitSubject(ctx context.Context, sha string) (string, error) {
	cmd := exec.CommandContext(ctx, "git",
		"-C", c.workTreePath,
		"show", "-s", "--format=%s", sha,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", &Error{Op: "show -s --format=%s " + sha, Err: err, Stderr: stderr.String()}
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Error wraps a git invocation failure with the command summary and
// captured stderr, so callers can surface a useful message.
type Error struct {
	Op     string
	Err    error
	Stderr string
}

func (e *Error) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("git %s: %v: %s", e.Op, e.Err, strings.TrimSpace(e.Stderr))
	}
	return fmt.Sprintf("git %s: %v", e.Op, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

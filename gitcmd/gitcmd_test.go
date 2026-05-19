package gitcmd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runGit runs git in dir for test setup. Fails the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	// Deterministic identity + no signing so tests do not depend on
	// the developer's git config.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test Author",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test Author",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// initRepo creates an empty repo at dir with a default branch of "main".
func initRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init", "--initial-branch=main", "--quiet")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	runGit(t, dir, "config", "tag.gpgsign", "false")
}

// writeAndCommit writes path:contents and commits with the given message.
// Uses a fixed committer date so relative-date output is deterministic.
func writeAndCommit(t *testing.T, dir, path, contents, msg string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	runGit(t, dir, "add", path)
	runGit(t, dir, "commit", "-m", msg, "--quiet")
}

func TestTopLevel_DiscoversFromSubdir(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeAndCommit(t, repo, "README.md", "hello\n", "initial")

	sub := filepath.Join(repo, "deep", "nested", "dir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	top, err := TopLevel(ctx, sub)
	if err != nil {
		t.Fatalf("TopLevel: %v", err)
	}
	// Resolve symlinks on both sides (macOS /tmp -> /private/tmp).
	gotResolved, _ := filepath.EvalSymlinks(top)
	wantResolved, _ := filepath.EvalSymlinks(repo)
	if gotResolved != wantResolved {
		t.Fatalf("TopLevel = %q, want %q", gotResolved, wantResolved)
	}
}

func TestTopLevel_OutsideRepo(t *testing.T) {
	dir := t.TempDir() // empty temp dir, not a repo
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := TopLevel(ctx, dir)
	if err == nil {
		t.Fatalf("expected error outside a repo, got nil")
	}
	var gErr *Error
	if !errors.As(err, &gErr) {
		t.Fatalf("expected *gitcmd.Error, got %T: %v", err, err)
	}
	if gErr.Stderr == "" {
		t.Errorf("expected captured stderr, got empty")
	}
}

func TestHasHead_EmptyRepoAndPopulated(t *testing.T) {
	// Fresh repo with no commits yet: rev-parse --verify HEAD fails
	// with exit 1 and empty stderr (the --quiet flag suppresses the
	// "fatal:" line), which HasHead recognizes as the empty-repo case.
	empty := t.TempDir()
	initRepo(t, empty)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := New(empty)
	ok, err := c.HasHead(ctx)
	if err != nil {
		t.Fatalf("HasHead on empty repo: unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("HasHead on empty repo = true, want false")
	}

	// After a commit, HasHead returns true.
	writeAndCommit(t, empty, "a.txt", "a\n", "first")
	ok, err = c.HasHead(ctx)
	if err != nil {
		t.Fatalf("HasHead on populated repo: %v", err)
	}
	if !ok {
		t.Fatalf("HasHead on populated repo = false, want true")
	}
}

func TestLog_OrderAndFields(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeAndCommit(t, repo, "a.txt", "a\n", "first commit")
	writeAndCommit(t, repo, "b.txt", "b\n", "second commit")
	writeAndCommit(t, repo, "c.txt", "c\n", "third commit, with: punctuation & symbols!")

	c := New(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	commits, err := c.Log(ctx, 0, 0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 3 {
		t.Fatalf("got %d commits, want 3: %#v", len(commits), commits)
	}
	// Reverse-chronological: newest first.
	if got, want := commits[0].Subject, "third commit, with: punctuation & symbols!"; got != want {
		t.Errorf("commits[0].Subject = %q, want %q", got, want)
	}
	if got, want := commits[2].Subject, "first commit"; got != want {
		t.Errorf("commits[2].Subject = %q, want %q", got, want)
	}
	for i, k := range commits {
		if len(k.SHA) != 40 {
			t.Errorf("commits[%d].SHA = %q, expected 40 hex chars", i, k.SHA)
		}
		if k.ShortSHA == "" || len(k.ShortSHA) >= 40 {
			t.Errorf("commits[%d].ShortSHA = %q, expected non-empty short sha", i, k.ShortSHA)
		}
		if k.Author != "Test Author" {
			t.Errorf("commits[%d].Author = %q, want %q", i, k.Author, "Test Author")
		}
		if k.Email != "test@example.com" {
			t.Errorf("commits[%d].Email = %q, want %q", i, k.Email, "test@example.com")
		}
		if k.AuthorDateISO == "" {
			t.Errorf("commits[%d].AuthorDateISO empty", i)
		}
		if k.RelDate == "" {
			t.Errorf("commits[%d].RelDate empty", i)
		}
	}
}

func TestLog_PaginationSkipAndLimit(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	const N = 12
	for i := 0; i < N; i++ {
		writeAndCommit(t, repo, "f.txt", strings.Repeat("x", i+1)+"\n",
			"commit-"+string(rune('A'+i)))
	}

	c := New(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	page1, err := c.Log(ctx, 0, 5)
	if err != nil {
		t.Fatalf("Log page1: %v", err)
	}
	if len(page1) != 5 {
		t.Fatalf("page1 len = %d, want 5", len(page1))
	}
	page2, err := c.Log(ctx, 5, 5)
	if err != nil {
		t.Fatalf("Log page2: %v", err)
	}
	if len(page2) != 5 {
		t.Fatalf("page2 len = %d, want 5", len(page2))
	}
	page3, err := c.Log(ctx, 10, 5)
	if err != nil {
		t.Fatalf("Log page3: %v", err)
	}
	if len(page3) != 2 {
		t.Fatalf("page3 len = %d, want 2 (only %d left)", len(page3), N-10)
	}
	// No overlap: SHAs across pages must be distinct.
	seen := map[string]int{}
	for _, c := range append(append(page1, page2...), page3...) {
		seen[c.SHA]++
	}
	for sha, n := range seen {
		if n != 1 {
			t.Errorf("sha %s appeared %d times across pages, want 1", sha, n)
		}
	}
	if len(seen) != N {
		t.Errorf("distinct shas across pages = %d, want %d", len(seen), N)
	}
}

func TestLog_RefDecorations(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeAndCommit(t, repo, "a.txt", "a\n", "first")
	writeAndCommit(t, repo, "b.txt", "b\n", "second")
	runGit(t, repo, "branch", "feature")
	runGit(t, repo, "tag", "v0.1")

	c := New(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	commits, err := c.Log(ctx, 0, 0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) == 0 {
		t.Fatalf("no commits")
	}
	refs := commits[0].Refs
	// HEAD, main, feature, tag: v0.1 should all be present on the tip.
	want := []string{"HEAD -> main", "feature", "tag: v0.1"}
	for _, w := range want {
		found := false
		for _, r := range refs {
			if r == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected ref %q in %v", w, refs)
		}
	}
}

func TestClassifyRef(t *testing.T) {
	cases := []struct {
		in   string
		want RefKind
	}{
		{"HEAD", RefHEAD},
		{"HEAD -> main", RefHEAD},
		{"HEAD -> feature/x", RefHEAD},
		{"main", RefLocal},
		{"feature", RefLocal},
		{"origin/main", RefRemote},
		{"upstream/feature/x", RefRemote},
		{"tag: v0.1", RefTag},
		{"tag: release-1.0", RefTag},
	}
	for _, tc := range cases {
		got := ClassifyRef(tc.in)
		if got != tc.want {
			t.Errorf("ClassifyRef(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestLog_RefDecorations_Remote(t *testing.T) {
	// Build a "remote" repo and a clone that has remote-tracking refs.
	remote := t.TempDir()
	initRepo(t, remote)
	writeAndCommit(t, remote, "a.txt", "a\n", "first")

	local := t.TempDir()
	// clone into local
	cmd := exec.Command("git", "clone", "--quiet", remote, local)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test Author",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test Author",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}

	c := New(local)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	commits, err := c.Log(ctx, 0, 0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) == 0 {
		t.Fatalf("no commits")
	}
	// Expect at least one ref to classify as RefRemote (origin/...).
	var remoteCount, headCount, tagCount int
	for _, r := range commits[0].Refs {
		switch ClassifyRef(r) {
		case RefRemote:
			remoteCount++
		case RefHEAD:
			headCount++
		case RefTag:
			tagCount++
		}
	}
	if remoteCount == 0 {
		t.Errorf("expected at least one remote-tracking ref in %v", commits[0].Refs)
	}
	if headCount == 0 {
		t.Errorf("expected HEAD ref in %v", commits[0].Refs)
	}
}

// writeAndCommitMsg is like writeAndCommit but takes a full message
// (subject + body) via -F so newlines are preserved exactly.
func writeAndCommitMsg(t *testing.T, dir, path, contents, msg string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	msgFile := filepath.Join(dir, ".git-msg-tmp")
	if err := os.WriteFile(msgFile, []byte(msg), 0o644); err != nil {
		t.Fatalf("write msg: %v", err)
	}
	defer os.Remove(msgFile)
	runGit(t, dir, "add", path)
	runGit(t, dir, "commit", "-F", msgFile, "--quiet")
}

func TestShow_MultilineBody(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	body := "feat: add thing\n\nThis is the body.\nIt spans multiple lines.\n\nAnd has a blank line in it.\n"
	writeAndCommitMsg(t, repo, "a.txt", "a\n", body)

	c := New(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	commits, err := c.Log(ctx, 0, 0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("len(commits) = %d, want 1", len(commits))
	}
	sha := commits[0].SHA

	d, err := c.Show(ctx, sha)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if d.SHA != sha {
		t.Errorf("SHA = %q, want %q", d.SHA, sha)
	}
	if d.ShortSHA == "" {
		t.Errorf("ShortSHA empty")
	}
	if d.AuthorName != "Test Author" || d.AuthorEmail != "test@example.com" {
		t.Errorf("author = %q <%q>", d.AuthorName, d.AuthorEmail)
	}
	if d.CommitterName != "Test Author" || d.CommitterEmail != "test@example.com" {
		t.Errorf("committer = %q <%q>", d.CommitterName, d.CommitterEmail)
	}
	if d.AuthorDateISO == "" || d.AuthorDateRel == "" {
		t.Errorf("missing author date fields: %+v", d)
	}
	if d.CommitterDateISO == "" || d.CommitterDateRel == "" {
		t.Errorf("missing committer date fields: %+v", d)
	}
	// Initial commit: no parents.
	if len(d.Parents) != 0 {
		t.Errorf("Parents = %v, want empty for initial commit", d.Parents)
	}
	// Body should preserve subject, blank line, and multi-line body.
	wantBody := strings.TrimRight(body, "\n")
	if d.Body != wantBody {
		t.Errorf("Body = %q, want %q", d.Body, wantBody)
	}
}

func TestShow_ParentsAndRefs(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeAndCommit(t, repo, "a.txt", "a\n", "first")
	writeAndCommit(t, repo, "b.txt", "b\n", "second")
	runGit(t, repo, "tag", "v1.0")

	c := New(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	commits, err := c.Log(ctx, 0, 0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	tip := commits[0]
	d, err := c.Show(ctx, tip.SHA)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if len(d.Parents) != 1 || len(d.Parents[0]) != 40 {
		t.Errorf("Parents = %v, want one 40-char sha", d.Parents)
	}
	if d.Parents[0] != commits[1].SHA {
		t.Errorf("Parents[0] = %q, want %q", d.Parents[0], commits[1].SHA)
	}
	foundTag, foundHead := false, false
	for _, r := range d.Refs {
		if r == "tag: v1.0" {
			foundTag = true
		}
		if strings.Contains(r, "HEAD") {
			foundHead = true
		}
	}
	if !foundTag {
		t.Errorf("expected tag in Refs, got %v", d.Refs)
	}
	if !foundHead {
		t.Errorf("expected HEAD in Refs, got %v", d.Refs)
	}
}

func TestShow_MissingSha(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeAndCommit(t, repo, "a.txt", "a\n", "first")
	c := New(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.Show(ctx, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err == nil {
		t.Fatalf("expected error for missing sha, got nil")
	}
	var gErr *Error
	if !errors.As(err, &gErr) {
		t.Fatalf("expected *gitcmd.Error, got %T: %v", err, err)
	}
}

func TestNumStat_InitialAndModify(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeAndCommit(t, repo, "a.txt", "one\ntwo\nthree\n", "initial")
	// Modify a.txt and add b.txt in a second commit.
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\ntwo\nthree\nfour\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "b.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "add b and modify a", "--quiet")

	c := New(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	commits, err := c.Log(ctx, 0, 0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	// Newest first: commits[1] is the initial commit; commits[0] is "add b".
	initial, err := c.NumStat(ctx, commits[1].SHA)
	if err != nil {
		t.Fatalf("NumStat initial: %v", err)
	}
	if len(initial) != 1 || initial[0].Path != "a.txt" || initial[0].Status != "A" {
		t.Fatalf("initial commit numstat = %+v", initial)
	}
	if initial[0].Added != 3 || initial[0].Deleted != 0 {
		t.Errorf("initial a.txt = +%d/-%d, want +3/-0", initial[0].Added, initial[0].Deleted)
	}

	second, err := c.NumStat(ctx, commits[0].SHA)
	if err != nil {
		t.Fatalf("NumStat second: %v", err)
	}
	if len(second) != 2 {
		t.Fatalf("second commit numstat len = %d, want 2: %+v", len(second), second)
	}
	byPath := map[string]FileStat{}
	for _, f := range second {
		byPath[f.Path] = f
	}
	if a := byPath["a.txt"]; a.Status != "M" || a.Added != 1 || a.Deleted != 0 {
		t.Errorf("a.txt = %+v, want M +1/-0", a)
	}
	if b := byPath["b.txt"]; b.Status != "A" || b.Added != 1 || b.Deleted != 0 {
		t.Errorf("b.txt = %+v, want A +1/-0", b)
	}
}

// TestNumStat_RootCommitMultipleFiles verifies that the numstat
// codepath handles a root commit with multiple heterogeneous files
// (text, binary, and a file in a subdirectory) without ever
// dereferencing a non-existent parent sha. Every file should appear
// with status "A" and the correct added-line counts. This is the
// primary `gitcmd` coverage for issue 12 (initial commit handling).
func TestNumStat_RootCommitMultipleFiles(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	// Write three files in a single root commit: a text file with
	// three lines, a binary file, and a text file in a subdirectory.
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "sub", "c.txt"), []byte("nested\n"), 0o644); err != nil {
		t.Fatalf("write sub/c.txt: %v", err)
	}
	bin := []byte{0x00, 0x01, 0x02, 0x00, 0x03}
	if err := os.WriteFile(filepath.Join(repo, "data.bin"), bin, 0o644); err != nil {
		t.Fatalf("write data.bin: %v", err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "root commit", "--quiet")

	c := New(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	commits, err := c.Log(ctx, 0, 1)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	root := commits[0].SHA

	// Confirm the root commit really has no parents — guards against
	// the test repo being seeded with something we didn't expect.
	parents, err := c.commitParents(ctx, root)
	if err != nil {
		t.Fatalf("commitParents: %v", err)
	}
	if len(parents) != 0 {
		t.Fatalf("expected root commit to have 0 parents, got %v", parents)
	}

	stats, err := c.NumStat(ctx, root)
	if err != nil {
		t.Fatalf("NumStat: %v", err)
	}
	if len(stats) != 3 {
		t.Fatalf("len(stats) = %d, want 3: %+v", len(stats), stats)
	}
	byPath := map[string]FileStat{}
	for _, f := range stats {
		byPath[f.Path] = f
		if f.Status != "A" {
			t.Errorf("Path %q Status = %q, want %q", f.Path, f.Status, "A")
		}
	}
	if a := byPath["a.txt"]; a.Added != 3 || a.Deleted != 0 || a.IsBinary {
		t.Errorf("a.txt = %+v, want A +3/-0 non-binary", a)
	}
	if c := byPath["sub/c.txt"]; c.Added != 1 || c.Deleted != 0 || c.IsBinary {
		t.Errorf("sub/c.txt = %+v, want A +1/-0 non-binary", c)
	}
	if b := byPath["data.bin"]; !b.IsBinary {
		t.Errorf("data.bin = %+v, want binary", b)
	}
}

// TestDiff_RootCommitNoParentDeref verifies that the diff codepath for
// a root commit succeeds without dereferencing a non-existent parent
// sha. The diff for every file should show its content as added and
// carry the `new file mode` header. This is the diff-side coverage
// for issue 12.
func TestDiff_RootCommitNoParentDeref(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "b.txt"), []byte("gamma\n"), 0o644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "root", "--quiet")

	c := New(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	commits, err := c.Log(ctx, 0, 1)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	root := commits[0].SHA

	for _, tc := range []struct {
		path  string
		wants []string
	}{
		{"a.txt", []string{"new file mode", "+alpha", "+beta"}},
		{"b.txt", []string{"new file mode", "+gamma"}},
	} {
		diff, err := c.Diff(ctx, root, tc.path)
		if err != nil {
			t.Fatalf("Diff(%s): %v", tc.path, err)
		}
		for _, want := range tc.wants {
			if !strings.Contains(diff, want) {
				t.Errorf("expected %q in diff for %s:\n%s", want, tc.path, diff)
			}
		}
		// A root-commit diff must not contain any `-` removal lines.
		for _, line := range strings.Split(diff, "\n") {
			if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "--- ") {
				t.Errorf("unexpected removal line in root-commit diff for %s: %q", tc.path, line)
			}
		}
	}
}

func TestNumStat_Rename(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeAndCommit(t, repo, "old.txt", "one\ntwo\nthree\nfour\nfive\n", "initial")
	// Rename old.txt -> new.txt
	oldP := filepath.Join(repo, "old.txt")
	newP := filepath.Join(repo, "new.txt")
	if err := os.Rename(oldP, newP); err != nil {
		t.Fatalf("rename: %v", err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "rename", "--quiet")

	c := New(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	commits, err := c.Log(ctx, 0, 1)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	stats, err := c.NumStat(ctx, commits[0].SHA)
	if err != nil {
		t.Fatalf("NumStat: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("len(stats) = %d, want 1: %+v", len(stats), stats)
	}
	s := stats[0]
	if s.Status != "R" {
		t.Errorf("Status = %q, want %q", s.Status, "R")
	}
	if s.OldPath != "old.txt" || s.Path != "new.txt" {
		t.Errorf("OldPath/Path = %q/%q, want old.txt/new.txt", s.OldPath, s.Path)
	}
}

func TestNumStat_Binary(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	// Bytes with embedded NULs — git treats this as binary.
	bin := []byte{0x00, 0x01, 0x02, 0x00, 0x03, 0x04, 0x00, 0x05}
	if err := os.WriteFile(filepath.Join(repo, "data.bin"), bin, 0o644); err != nil {
		t.Fatalf("write bin: %v", err)
	}
	runGit(t, repo, "add", "data.bin")
	runGit(t, repo, "commit", "-m", "add binary", "--quiet")

	c := New(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	commits, err := c.Log(ctx, 0, 1)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	stats, err := c.NumStat(ctx, commits[0].SHA)
	if err != nil {
		t.Fatalf("NumStat: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("len(stats) = %d, want 1", len(stats))
	}
	if !stats[0].IsBinary {
		t.Errorf("expected IsBinary=true, got %+v", stats[0])
	}
	if stats[0].Status != "A" {
		t.Errorf("Status = %q, want %q", stats[0].Status, "A")
	}
}

func TestDiff_ModifyHasHunkAndPlusMinus(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeAndCommit(t, repo, "a.txt", "one\ntwo\nthree\n", "initial")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\nTWO\nthree\nfour\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, repo, "add", "a.txt")
	runGit(t, repo, "commit", "-m", "modify a", "--quiet")

	c := New(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	commits, err := c.Log(ctx, 0, 1)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	diff, err := c.Diff(ctx, commits[0].SHA, "a.txt")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	for _, want := range []string{"@@", "-two", "+TWO", "+four", " one", " three"} {
		if !strings.Contains(diff, want) {
			t.Errorf("expected %q in diff:\n%s", want, diff)
		}
	}
}

func TestDiff_InitialCommit(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeAndCommit(t, repo, "a.txt", "hello\nworld\n", "initial")

	c := New(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	commits, err := c.Log(ctx, 0, 1)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	diff, err := c.Diff(ctx, commits[0].SHA, "a.txt")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "+hello") || !strings.Contains(diff, "+world") {
		t.Errorf("expected both lines as added in initial commit:\n%s", diff)
	}
	if !strings.Contains(diff, "new file mode") {
		t.Errorf("expected 'new file mode' header for initial commit:\n%s", diff)
	}
}

func TestDiff_MissingSha(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeAndCommit(t, repo, "a.txt", "x\n", "initial")
	c := New(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.Diff(ctx, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "a.txt")
	if err == nil {
		t.Fatalf("expected error for missing sha")
	}
	var gErr *Error
	if !errors.As(err, &gErr) {
		t.Fatalf("expected *gitcmd.Error, got %T: %v", err, err)
	}
}

// makeMergeWithConflict builds a repo with a merge commit whose merge
// included a non-trivial conflict resolution on conflict.txt. Returns
// the merge commit's sha.
func makeMergeWithConflict(t *testing.T, repo string) string {
	t.Helper()
	initRepo(t, repo)
	writeAndCommit(t, repo, "conflict.txt", "base\n", "base")
	writeAndCommit(t, repo, "other.txt", "shared\n", "second on main")
	runGit(t, repo, "checkout", "-b", "branch", "--quiet")
	if err := os.WriteFile(filepath.Join(repo, "conflict.txt"), []byte("branch change\n"), 0o644); err != nil {
		t.Fatalf("write conflict.txt on branch: %v", err)
	}
	runGit(t, repo, "add", "conflict.txt")
	runGit(t, repo, "commit", "-m", "branch edit", "--quiet")
	runGit(t, repo, "checkout", "main", "--quiet")
	if err := os.WriteFile(filepath.Join(repo, "conflict.txt"), []byte("main change\n"), 0o644); err != nil {
		t.Fatalf("write conflict.txt on main: %v", err)
	}
	runGit(t, repo, "add", "conflict.txt")
	runGit(t, repo, "commit", "-m", "main edit", "--quiet")
	// Attempt the merge — it conflicts; resolve to a third distinct value.
	cmd := exec.Command("git", "-C", repo, "merge", "--no-ff", "branch", "-m", "merge branch")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test Author",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test Author",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	_ = cmd.Run() // expected to exit non-zero due to conflict
	if err := os.WriteFile(filepath.Join(repo, "conflict.txt"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatalf("write resolved conflict.txt: %v", err)
	}
	runGit(t, repo, "add", "conflict.txt")
	runGit(t, repo, "commit", "--no-edit", "--quiet")
	// Read the merge sha.
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func TestNumStat_MergeWithConflictResolution(t *testing.T) {
	repo := t.TempDir()
	merge := makeMergeWithConflict(t, repo)

	c := New(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stats, err := c.NumStat(ctx, merge)
	if err != nil {
		t.Fatalf("NumStat: %v", err)
	}
	// --cc restricts output to files whose merge resolution differs from
	// every parent. Only conflict.txt qualifies — other.txt was equal in
	// both parents and is not shown.
	if len(stats) != 1 {
		t.Fatalf("len(stats) = %d, want 1: %+v", len(stats), stats)
	}
	if stats[0].Path != "conflict.txt" {
		t.Errorf("Path = %q, want %q", stats[0].Path, "conflict.txt")
	}
	if stats[0].Added == 0 && stats[0].Deleted == 0 && !stats[0].IsBinary {
		t.Errorf("expected non-zero added/deleted for resolved conflict, got %+v", stats[0])
	}
}

func TestNumStat_CleanMerge(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeAndCommit(t, repo, "shared.txt", "hello\n", "base")
	runGit(t, repo, "checkout", "-b", "branch", "--quiet")
	// Both branches make the *same* edit independently. The merge
	// result ("modified\n") trivially matches both parents, so --cc
	// (which only shows hunks where the result differs from every
	// parent) omits the file entirely.
	writeAndCommit(t, repo, "shared.txt", "modified\n", "modify on branch")
	runGit(t, repo, "checkout", "main", "--quiet")
	writeAndCommit(t, repo, "shared.txt", "modified\n", "same modification on main")
	runGit(t, repo, "merge", "--no-ff", "branch", "-m", "clean merge", "--quiet")
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	mergeSHA := strings.TrimSpace(string(out))

	// Confirm it really is a merge.
	parents, err := New(repo).commitParents(context.Background(), mergeSHA)
	if err != nil {
		t.Fatalf("commitParents: %v", err)
	}
	if len(parents) < 2 {
		t.Fatalf("expected merge commit (>=2 parents), got %d: %v", len(parents), parents)
	}

	c := New(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stats, err := c.NumStat(ctx, mergeSHA)
	if err != nil {
		t.Fatalf("NumStat: %v", err)
	}
	// Clean merge → --cc produces no rows.
	if len(stats) != 0 {
		t.Errorf("expected empty stats for clean merge, got %+v", stats)
	}
}

func TestDiff_MergeUsesCombined(t *testing.T) {
	repo := t.TempDir()
	merge := makeMergeWithConflict(t, repo)

	c := New(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	diff, err := c.Diff(ctx, merge, "conflict.txt")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	// Combined diff hunk headers use "@@@" (one more @ than the parent
	// count). For our 2-parent merge that's "@@@" — a reliable sentinel
	// that the --cc codepath was used.
	if !strings.Contains(diff, "@@@") {
		t.Errorf("expected combined-diff header (@@@) in merge diff, got:\n%s", diff)
	}
	if !strings.Contains(diff, "resolved") {
		t.Errorf("expected resolved content in merge diff, got:\n%s", diff)
	}
}

func TestLog_OutsideRepo(t *testing.T) {
	dir := t.TempDir()
	c := New(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.Log(ctx, 0, 0)
	if err == nil {
		t.Fatalf("expected error logging outside a repo")
	}
}

func TestFileSize_PresentAndAbsent(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	// Two commits: initial empty, then add a binary file we know the size of.
	writeAndCommit(t, repo, "README.md", "hello\n", "initial")
	bin := []byte{0x00, 0x01, 0x02, 0x00, 0x03, 0x04, 0x00, 0x05}
	if err := os.WriteFile(filepath.Join(repo, "data.bin"), bin, 0o644); err != nil {
		t.Fatalf("write bin: %v", err)
	}
	runGit(t, repo, "add", "data.bin")
	runGit(t, repo, "commit", "-m", "add binary", "--quiet")

	c := New(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	commits, err := c.Log(ctx, 0, 2)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	head := commits[0].SHA
	parent := commits[1].SHA

	// File exists at HEAD with the expected byte size.
	got, err := c.FileSize(ctx, head, "data.bin")
	if err != nil {
		t.Fatalf("FileSize(head): %v", err)
	}
	if got != int64(len(bin)) {
		t.Errorf("FileSize(head) = %d, want %d", got, len(bin))
	}

	// File does not exist at the parent commit.
	_, err = c.FileSize(ctx, parent, "data.bin")
	if err == nil {
		t.Fatalf("expected error looking up file at parent commit, got nil")
	}
	var gErr *Error
	if !errors.As(err, &gErr) {
		t.Fatalf("expected *gitcmd.Error, got %T: %v", err, err)
	}

	// README.md exists at both commits with stable byte size 6.
	got, err = c.FileSize(ctx, head, "README.md")
	if err != nil {
		t.Fatalf("FileSize(README): %v", err)
	}
	if got != 6 {
		t.Errorf("FileSize(README) = %d, want 6", got)
	}
}

func TestTags_LightweightAndAnnotated(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeAndCommit(t, repo, "a.txt", "a\n", "first")
	// Lightweight tag — bare ref, no tag object.
	runGit(t, repo, "tag", "v1-light")
	// Annotated tag — full tag object with subject + body.
	runGit(t, repo, "tag", "-a", "v2-annot", "-m", "Release v2\n\nFull notes here.")
	// A second commit so we can confirm Tags only returns tags pointing
	// at the requested sha.
	writeAndCommit(t, repo, "b.txt", "b\n", "second")

	c := New(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	commits, err := c.Log(ctx, 0, 0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("Log: got %d commits, want 2", len(commits))
	}
	tip := commits[0].SHA   // second commit, no tags
	first := commits[1].SHA // first commit, both tags

	tags, err := c.Tags(ctx, tip)
	if err != nil {
		t.Fatalf("Tags(tip): %v", err)
	}
	if len(tags) != 0 {
		t.Errorf("Tags(tip) = %v, want none", tags)
	}

	tags, err = c.Tags(ctx, first)
	if err != nil {
		t.Fatalf("Tags(first): %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("Tags(first) = %v, want 2 entries", tags)
	}
	byName := map[string]TagInfo{}
	for _, tg := range tags {
		byName[tg.Name] = tg
	}
	if light, ok := byName["v1-light"]; !ok {
		t.Errorf("lightweight tag v1-light not found in %v", tags)
	} else {
		if light.Annotated {
			t.Errorf("v1-light Annotated = true, want false")
		}
		if light.Message != "" {
			t.Errorf("v1-light Message = %q, want empty", light.Message)
		}
	}
	if annot, ok := byName["v2-annot"]; !ok {
		t.Errorf("annotated tag v2-annot not found in %v", tags)
	} else {
		if !annot.Annotated {
			t.Errorf("v2-annot Annotated = false, want true")
		}
		if !strings.Contains(annot.Message, "Release v2") {
			t.Errorf("v2-annot Message = %q, want to contain %q", annot.Message, "Release v2")
		}
		if !strings.Contains(annot.Message, "Full notes here.") {
			t.Errorf("v2-annot Message = %q, want to contain body line", annot.Message)
		}
	}

	// Show on the first commit should embed the tag info in CommitDetail.
	d, err := c.Show(ctx, first)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if len(d.Tags) != 2 {
		t.Fatalf("Show.Tags = %v, want 2", d.Tags)
	}
	// Show on the tip should have no tags.
	d2, err := c.Show(ctx, tip)
	if err != nil {
		t.Fatalf("Show(tip): %v", err)
	}
	if len(d2.Tags) != 0 {
		t.Errorf("Show(tip).Tags = %v, want none", d2.Tags)
	}
}

func TestWorktrees_SingleWorktree(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeAndCommit(t, repo, "README.md", "hello\n", "initial")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	top, err := TopLevel(ctx, repo)
	if err != nil {
		t.Fatalf("TopLevel: %v", err)
	}
	c := New(top)
	wts, err := c.Worktrees(ctx)
	if err != nil {
		t.Fatalf("Worktrees: %v", err)
	}
	if len(wts) != 1 {
		t.Fatalf("Worktrees len = %d, want 1: %#v", len(wts), wts)
	}
	w := wts[0]
	if !w.Current {
		t.Errorf("expected sole worktree to be marked Current: %#v", w)
	}
	if w.Branch != "main" {
		t.Errorf("Branch = %q, want %q", w.Branch, "main")
	}
	if w.HeadSHA == "" {
		t.Errorf("HeadSHA empty")
	}
	if w.Detached {
		t.Errorf("Detached = true, want false")
	}
}

func TestWorktrees_MultiWorktree(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeAndCommit(t, repo, "README.md", "hello\n", "initial")
	// Build a second worktree on a brand-new branch off HEAD.
	runGit(t, repo, "branch", "feature")
	extra := filepath.Join(t.TempDir(), "extra-wt")
	runGit(t, repo, "worktree", "add", extra, "feature")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Client scoped to main worktree.
	top, err := TopLevel(ctx, repo)
	if err != nil {
		t.Fatalf("TopLevel: %v", err)
	}
	c := New(top)
	wts, err := c.Worktrees(ctx)
	if err != nil {
		t.Fatalf("Worktrees: %v", err)
	}
	if len(wts) != 2 {
		t.Fatalf("Worktrees len = %d, want 2: %#v", len(wts), wts)
	}
	byBranch := map[string]Worktree{}
	for _, w := range wts {
		byBranch[w.Branch] = w
	}
	if _, ok := byBranch["main"]; !ok {
		t.Fatalf("no main worktree in %#v", wts)
	}
	if _, ok := byBranch["feature"]; !ok {
		t.Fatalf("no feature worktree in %#v", wts)
	}
	if !byBranch["main"].Current {
		t.Errorf("main worktree should be Current when client scoped to repo root")
	}
	if byBranch["feature"].Current {
		t.Errorf("feature worktree should not be Current when client scoped to main: %#v", byBranch["feature"])
	}

	// Client scoped to the extra worktree flips Current.
	top2, err := TopLevel(ctx, extra)
	if err != nil {
		t.Fatalf("TopLevel(extra): %v", err)
	}
	c2 := New(top2)
	wts2, err := c2.Worktrees(ctx)
	if err != nil {
		t.Fatalf("Worktrees(c2): %v", err)
	}
	byBranch2 := map[string]Worktree{}
	for _, w := range wts2 {
		byBranch2[w.Branch] = w
	}
	if !byBranch2["feature"].Current {
		t.Errorf("feature worktree should be Current when client scoped to extra: %#v", byBranch2["feature"])
	}
	if byBranch2["main"].Current {
		t.Errorf("main worktree should not be Current when client scoped to extra: %#v", byBranch2["main"])
	}
}

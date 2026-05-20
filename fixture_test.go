package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/orcasauce/git-review-tui/gitcmd"
)

// repoFixture mirrors the JSON schema written by
// .scratch/capture-repo-fixture.sh. Only the fields the current tests
// consume are decoded; others are kept as raw strings.
type repoFixture struct {
	SchemaVersion int                       `json:"schema_version"`
	Head          string                    `json:"head"`
	HeadShort     string                    `json:"head_short"`
	LogB64        string                    `json:"log_b64"`
	Commits       map[string]commitFixture  `json:"commits"`
}

type commitFixture struct {
	ShowB64         string   `json:"show_b64"`
	TagsB64         string   `json:"tags_b64"`
	Parents         []string `json:"parents"`
	IsMerge         bool     `json:"is_merge"`
	NumStatB64      string   `json:"numstat_b64"`
	NumStatMergeB64 string   `json:"numstat_merge_b64"`
}

func loadRepoFixture(t *testing.T, path string) repoFixture {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	var fx repoFixture
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatalf("decode fixture %s: %v", path, err)
	}
	if fx.SchemaVersion != 1 {
		t.Fatalf("fixture %s has schema_version=%d, want 1", path, fx.SchemaVersion)
	}
	return fx
}

// numStatFor decodes and parses the captured numstat output for sha
// into []gitcmd.FileStat. The format is documented in gitcmd.parseNumStat
// (which is unexported); this is a tightly-scoped re-implementation
// for test fixtures only. If gitcmd's `diff-tree --raw --numstat`
// invocation ever changes shape, both sides must update together.
func numStatFor(t *testing.T, fx repoFixture, sha string) []gitcmd.FileStat {
	t.Helper()
	c, ok := fx.Commits[sha]
	if !ok {
		t.Fatalf("fixture has no commit %s", sha)
	}
	if c.IsMerge {
		t.Fatalf("commit %s is a merge; numStatFor only handles non-merge captures", sha)
	}
	raw, err := base64.StdEncoding.DecodeString(c.NumStatB64)
	if err != nil {
		t.Fatalf("decode numstat for %s: %v", sha, err)
	}
	out := string(raw)

	type rawRec struct{ status, path, oldPath string }
	type numRec struct {
		added, deleted int
		binary         bool
	}
	var raws []rawRec
	var nums []numRec
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if line[0] == ':' {
			tab := strings.IndexByte(line, '\t')
			if tab < 0 {
				t.Fatalf("unexpected raw line: %q", line)
			}
			fields := strings.Fields(line[1:tab])
			if len(fields) < 5 {
				t.Fatalf("unexpected raw meta: %q", line)
			}
			letter := string(fields[4][0])
			rest := line[tab+1:]
			r := rawRec{status: letter}
			if letter == "R" || letter == "C" {
				parts := strings.SplitN(rest, "\t", 2)
				if len(parts) != 2 {
					t.Fatalf("rename without new path: %q", line)
				}
				r.oldPath, r.path = parts[0], parts[1]
			} else {
				r.path = rest
			}
			raws = append(raws, r)
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			t.Fatalf("unexpected numstat line: %q", line)
		}
		if parts[0] == "-" && parts[1] == "-" {
			nums = append(nums, numRec{binary: true})
			continue
		}
		a, err := strconv.Atoi(parts[0])
		if err != nil {
			t.Fatalf("parse added in %q: %v", line, err)
		}
		d, err := strconv.Atoi(parts[1])
		if err != nil {
			t.Fatalf("parse deleted in %q: %v", line, err)
		}
		nums = append(nums, numRec{added: a, deleted: d})
	}
	n := len(raws)
	if len(nums) > n {
		n = len(nums)
	}
	files := make([]gitcmd.FileStat, 0, n)
	for i := 0; i < n; i++ {
		f := gitcmd.FileStat{}
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
	return files
}

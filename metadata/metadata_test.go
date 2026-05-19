package metadata

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/orcasauce/git-review-tui/gitcmd"
)

func TestFormatDateSameOffset(t *testing.T) {
	loc := time.FixedZone("PST", -7*3600)
	got := formatDate("2026-05-13T10:42:11-07:00", "2 days ago", loc)
	want := "2026-05-13 10:42:11 (2 days ago)"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestFormatDateDifferentOffset(t *testing.T) {
	loc := time.FixedZone("UTC", 0)
	got := formatDate("2026-05-13T10:42:11-07:00", "2 days ago", loc)
	want := "2026-05-13 10:42:11 -0700 (2 days ago)"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestFormatDatePositiveOffset(t *testing.T) {
	loc := time.FixedZone("UTC", 0)
	got := formatDate("2026-05-13T10:42:11+05:30", "1 hour ago", loc)
	want := "2026-05-13 10:42:11 +0530 (1 hour ago)"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestFormatDateUTC(t *testing.T) {
	loc := time.UTC
	got := formatDate("2026-05-13T10:42:11+00:00", "now", loc)
	want := "2026-05-13 10:42:11 (now)"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestFormatDateUTCFromOffsetLocation(t *testing.T) {
	loc := time.FixedZone("EST", -5*3600)
	got := formatDate("2026-05-13T10:42:11+00:00", "now", loc)
	want := "2026-05-13 10:42:11 +0000 (now)"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// TestFormatDateDSTBoundary verifies the offset comparison uses the
// location's offset at the commit's instant, not "now". The IANA
// "America/Los_Angeles" zone is PST (-0800) in January and PDT
// (-0700) in July; a January commit at -0800 should match the
// location's January offset (no suffix) regardless of when the test
// runs.
func TestFormatDateDSTBoundary(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skipf("tz data not available: %v", err)
	}
	// January 15 — standard time in LA (-0800).
	if got := formatDate("2026-01-15T10:42:11-08:00", "rel", loc); got != "2026-01-15 10:42:11 (rel)" {
		t.Errorf("Jan PST same-offset: got %q", got)
	}
	// July 15 — daylight time in LA (-0700).
	if got := formatDate("2026-07-15T10:42:11-07:00", "rel", loc); got != "2026-07-15 10:42:11 (rel)" {
		t.Errorf("Jul PDT same-offset: got %q", got)
	}
	// July commit at -0800 (clearly not LA in July) → offset appended.
	if got := formatDate("2026-07-15T10:42:11-08:00", "rel", loc); got != "2026-07-15 10:42:11 -0800 (rel)" {
		t.Errorf("Jul -0800 different from LA's PDT: got %q", got)
	}
}

func TestFormatOffset(t *testing.T) {
	cases := []struct {
		sec  int
		want string
	}{
		{-7 * 3600, "-0700"},
		{+5*3600 + 30*60, "+0530"},
		{0, "+0000"},
		{-30 * 60, "-0030"},
	}
	for _, c := range cases {
		if got := formatOffset(c.sec); got != c.want {
			t.Errorf("formatOffset(%d) = %q want %q", c.sec, got, c.want)
		}
	}
}

func TestFilterRefs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", nil, []string{}},
		{"only_head", []string{"HEAD"}, []string{}},
		{"head_arrow_main", []string{"HEAD -> main"}, []string{"main"}},
		{"head_arrow_with_siblings", []string{"HEAD -> main", "origin/main"}, []string{"main", "origin/main"}},
		{"tag_filtered", []string{"HEAD -> main", "tag: v1.0", "origin/main"}, []string{"main", "origin/main"}},
		{"branches_no_head", []string{"main", "feature/x"}, []string{"main", "feature/x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FilterRefs(c.in)
			// Normalise nil vs empty for comparison.
			if got == nil {
				got = []string{}
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}

func TestLinesTypicalCommit(t *testing.T) {
	loc := time.FixedZone("UTC", 0)
	d := gitcmd.CommitDetail{
		SHA:           "abcdef0123456789",
		ShortSHA:      "abcdef0",
		AuthorName:    "Whitney Beck",
		AuthorEmail:   "wb@example.com",
		AuthorDateISO: "2026-05-13T10:42:11+00:00",
		AuthorDateRel: "2 days ago",
		Refs:          []string{"HEAD -> main", "origin/main"},
		Body:          "Subject line\n\nBody paragraph.",
	}
	got := Lines(d, loc)
	want := []string{
		"abcdef0 (main, origin/main)",
		"Whitney Beck <wb@example.com>  2026-05-13 10:42:11 (2 days ago)",
		"",
		"Subject line",
		"",
		"Body paragraph.",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

func TestLinesNoRefsNoTags(t *testing.T) {
	loc := time.FixedZone("UTC", 0)
	d := gitcmd.CommitDetail{
		SHA:           "abcdef0123456789",
		ShortSHA:      "abcdef0",
		AuthorName:    "A",
		AuthorEmail:   "a@x",
		AuthorDateISO: "2026-05-13T10:42:11+00:00",
		AuthorDateRel: "now",
		Body:          "subj",
	}
	got := Lines(d, loc)
	want := []string{
		"abcdef0",
		"A <a@x>  2026-05-13 10:42:11 (now)",
		"",
		"subj",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

func TestLinesAnnotatedTag(t *testing.T) {
	loc := time.FixedZone("UTC", 0)
	d := gitcmd.CommitDetail{
		SHA:           "abcdef0123456789",
		ShortSHA:      "abcdef0",
		AuthorName:    "A",
		AuthorEmail:   "a@x",
		AuthorDateISO: "2026-05-13T10:42:11+00:00",
		AuthorDateRel: "now",
		Tags: []gitcmd.TagInfo{
			{Name: "v1.0", Annotated: true, Message: "Release v1.0\n\nNotes here"},
		},
		Body: "subj",
	}
	got := Lines(d, loc)
	want := []string{
		"abcdef0",
		"A <a@x>  2026-05-13 10:42:11 (now)",
		"Tags:       v1.0 (annotated)",
		"              Release v1.0",
		"              ",
		"              Notes here",
		"",
		"subj",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

func TestLinesEmptyBody(t *testing.T) {
	loc := time.FixedZone("UTC", 0)
	d := gitcmd.CommitDetail{
		SHA:           "abcdef0123456789",
		ShortSHA:      "abcdef0",
		AuthorName:    "A",
		AuthorEmail:   "a@x",
		AuthorDateISO: "2026-05-13T10:42:11+00:00",
		AuthorDateRel: "now",
	}
	got := Lines(d, loc)
	// Trailing "" separator with no body lines.
	if len(got) != 3 {
		t.Fatalf("expected 3 lines, got %d: %#v", len(got), got)
	}
	if got[2] != "" {
		t.Fatalf("expected blank separator at index 2, got %q", got[2])
	}
}

func TestLinesMergeCommit(t *testing.T) {
	loc := time.FixedZone("UTC", 0)
	d := gitcmd.CommitDetail{
		SHA:           "abcdef0123456789",
		ShortSHA:      "abcdef0",
		AuthorName:    "A",
		AuthorEmail:   "a@x",
		AuthorDateISO: "2026-05-13T10:42:11+00:00",
		AuthorDateRel: "now",
		Parents:       []string{"p1", "p2"},
		Body:          "Merge subj",
	}
	got := Lines(d, loc)
	want := []string{
		"abcdef0",
		"A <a@x>  2026-05-13 10:42:11 (now)",
		"",
		"Merge subj",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
	// Ensure no "Parents:" row leaked through.
	for _, l := range got {
		if strings.HasPrefix(l, "Parents:") {
			t.Fatalf("Parents row should not appear: %q", l)
		}
	}
}

func TestLinesEmptyDetail(t *testing.T) {
	if got := Lines(gitcmd.CommitDetail{}, time.UTC); got != nil {
		t.Fatalf("expected nil for empty detail, got %#v", got)
	}
}

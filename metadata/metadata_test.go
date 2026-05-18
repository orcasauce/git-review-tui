package metadata

import (
	"reflect"
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

func TestSummary(t *testing.T) {
	loc := time.FixedZone("UTC", 0)
	base := gitcmd.CommitDetail{
		SHA:           "abcdef0123456789",
		ShortSHA:      "abcdef0",
		AuthorName:    "Whitney Beck",
		AuthorEmail:   "wb@example.com",
		AuthorDateISO: "2026-05-13T10:42:11+00:00",
		AuthorDateRel: "2 days ago",
	}
	const wantAuthor = "Whitney Beck <wb@example.com>  2026-05-13 10:42:11 (2 days ago)"

	cases := []struct {
		name  string
		mutate func(*gitcmd.CommitDetail)
		want  [3]string
	}{
		{
			name:   "no_refs_no_tags",
			mutate: func(d *gitcmd.CommitDetail) {},
			want:   [3]string{"abcdef0", wantAuthor, ""},
		},
		{
			name: "refs_present",
			mutate: func(d *gitcmd.CommitDetail) {
				d.Refs = []string{"HEAD -> main", "origin/main"}
			},
			want: [3]string{"abcdef0 (main, origin/main)", wantAuthor, ""},
		},
		{
			name: "refs_with_tag_filtered_out",
			mutate: func(d *gitcmd.CommitDetail) {
				d.Refs = []string{"HEAD -> main", "tag: v1.0", "origin/main"}
			},
			want: [3]string{"abcdef0 (main, origin/main)", wantAuthor, ""},
		},
		{
			name: "single_lightweight_tag",
			mutate: func(d *gitcmd.CommitDetail) {
				d.Tags = []gitcmd.TagInfo{{Name: "v1.0"}}
			},
			want: [3]string{"abcdef0", wantAuthor, "Tags: v1.0"},
		},
		{
			name: "single_annotated_tag_no_body",
			mutate: func(d *gitcmd.CommitDetail) {
				d.Tags = []gitcmd.TagInfo{
					{Name: "v1.0", Annotated: true, Message: "Release v1.0\n\nNotes"},
				}
			},
			// Annotated bodies and the "(annotated)" suffix are not
			// rendered in the summary form.
			want: [3]string{"abcdef0", wantAuthor, "Tags: v1.0"},
		},
		{
			name: "two_tags",
			mutate: func(d *gitcmd.CommitDetail) {
				d.Tags = []gitcmd.TagInfo{{Name: "v1.0"}, {Name: "v1.0-rc1"}}
			},
			want: [3]string{"abcdef0", wantAuthor, "Tags: v1.0, v1.0-rc1"},
		},
		{
			name: "many_tags_truncated_with_plus_n_more",
			mutate: func(d *gitcmd.CommitDetail) {
				d.Tags = []gitcmd.TagInfo{
					{Name: "v1.0"},
					{Name: "v1.0-rc1"},
					{Name: "release"},
					{Name: "stable"},
					{Name: "ga"},
				}
			},
			want: [3]string{"abcdef0", wantAuthor, "Tags: v1.0, v1.0-rc1 (+3 more)"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := base
			c.mutate(&d)
			got := Summary(d, loc)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("Summary mismatch\n got: %#v\nwant: %#v", got, c.want)
			}
		})
	}
}

func TestSummaryEmptyDetail(t *testing.T) {
	got := Summary(gitcmd.CommitDetail{}, time.UTC)
	if got != ([3]string{}) {
		t.Fatalf("expected zero [3]string for empty detail, got %#v", got)
	}
}


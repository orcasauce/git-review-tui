// Package metadata builds the condensed three-row description of a
// commit (short sha + refs, author + date, tags summary) that the v5
// layout renders into the dedicated metadata panel above the file
// list. Summary is a pure function; the *time.Location is injected so
// callers can pass time.Local in production and a fixed zone in tests.
package metadata

import (
	"fmt"
	"strings"
	"time"

	"github.com/orcasauce/git-review-tui/gitcmd"
)

// Summary returns the three rows displayed in the v5 metadata panel:
//
//  1. Short sha plus filtered refs in parentheses (refs filtered via
//     FilterRefs; same rules as Lines).
//  2. Author name + email + formatted date (same renderer as Lines).
//  3. Tags summary: "" when the commit has no tags, "Tags: <name>"
//     for a single tag, "Tags: <a>, <b>" for two, and
//     "Tags: <a>, <b> (+N more)" when more than two tags exist.
//     Annotated-tag message bodies are not rendered.
//
// When d.SHA is empty the zero value [3]string{"", "", ""} is returned
// so callers can render a blank panel without a special case.
func Summary(d gitcmd.CommitDetail, loc *time.Location) [3]string {
	if d.SHA == "" {
		return [3]string{}
	}

	var out [3]string

	refs := FilterRefs(d.Refs)
	out[0] = d.ShortSHA
	if len(refs) > 0 {
		out[0] += " (" + strings.Join(refs, ", ") + ")"
	}

	out[1] = d.AuthorName + " <" + d.AuthorEmail + ">  " + formatDate(d.AuthorDateISO, d.AuthorDateRel, loc)

	switch len(d.Tags) {
	case 0:
		out[2] = ""
	case 1:
		out[2] = "Tags: " + d.Tags[0].Name
	case 2:
		out[2] = "Tags: " + d.Tags[0].Name + ", " + d.Tags[1].Name
	default:
		out[2] = fmt.Sprintf("Tags: %s, %s (+%d more)",
			d.Tags[0].Name, d.Tags[1].Name, len(d.Tags)-2)
	}

	return out
}

// FilterRefs returns the ref decorations that should appear on the
// metadata sha line: tags are dropped (they get the dedicated Tags
// block), bare HEAD is dropped, and "HEAD -> X" is rewritten to just
// "X". Token order is preserved.
func FilterRefs(refs []string) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if gitcmd.ClassifyRef(r) == gitcmd.RefTag {
			continue
		}
		if r == "HEAD" {
			continue
		}
		if rest, ok := strings.CutPrefix(r, "HEAD -> "); ok {
			out = append(out, strings.TrimSpace(rest))
			continue
		}
		out = append(out, r)
	}
	return out
}

// formatDate renders the commit's author date as "YYYY-MM-DD HH:MM:SS"
// in the commit's own wall-clock time. A " -hhmm" offset is appended
// when the commit's offset at its instant differs from loc's offset
// at the same instant. The relative date is always appended in
// parentheses.
func formatDate(iso, rel string, loc *time.Location) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		// Fall back to the raw ISO string when parsing fails — the
		// metadata block should still render something useful.
		if rel != "" {
			return iso + " (" + rel + ")"
		}
		return iso
	}

	base := t.Format("2006-01-02 15:04:05")

	_, commitOff := t.Zone()
	if loc == nil {
		loc = time.Local
	}
	_, locOff := t.In(loc).Zone()

	if commitOff != locOff {
		base += " " + formatOffset(commitOff)
	}
	if rel != "" {
		base += " (" + rel + ")"
	}
	return base
}

func formatOffset(sec int) string {
	sign := "+"
	if sec < 0 {
		sign = "-"
		sec = -sec
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	return fmt.Sprintf("%s%02d%02d", sign, h, m)
}

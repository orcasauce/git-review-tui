// Package metadata owns the commit-message header block: the short
// sha and refs line, the author+date line, the optional Tags block,
// and the blank separator before the body. Lines is a pure function;
// the *time.Location is injected so callers can pass time.Local in
// production and a fixed zone in tests.
package metadata

import (
	"fmt"
	"strings"
	"time"

	"github.com/orcasauce/git-review-tui/gitcmd"
)

// HeaderLineCount returns the number of leading lines in Lines that
// belong to the metadata header (short sha, author/date, optional tag
// block, and the blank separator). The body content starts at index
// HeaderLineCount(d). Returns 0 when Lines would return nil.
//
// Callers can use this to render the header as a sticky prefix so the
// short sha can't be scrolled off the top of the message panel.
func HeaderLineCount(d gitcmd.CommitDetail) int {
	if d.SHA == "" {
		return 0
	}
	n := 2 // sha line + author line
	for _, t := range d.Tags {
		n++ // "Tags: <name>" row
		if t.Annotated && t.Message != "" {
			n += len(strings.Split(t.Message, "\n"))
		}
	}
	n++ // blank separator
	return n
}

// Lines returns the ordered plain-text lines for the message panel:
// short sha [+ refs], author + date, optional Tags block, blank
// separator, then body lines.
func Lines(d gitcmd.CommitDetail, loc *time.Location) []string {
	if d.SHA == "" {
		return nil
	}
	var out []string

	refs := FilterRefs(d.Refs)
	line1 := d.ShortSHA
	if len(refs) > 0 {
		line1 += " (" + strings.Join(refs, ", ") + ")"
	}
	out = append(out, line1)

	out = append(out, d.AuthorName+" <"+d.AuthorEmail+">  "+formatDate(d.AuthorDateISO, d.AuthorDateRel, loc))

	for i, t := range d.Tags {
		label := "Tags:       "
		if i > 0 {
			label = "            "
		}
		name := t.Name
		if t.Annotated {
			name += " (annotated)"
		}
		out = append(out, label+name)
		if t.Annotated && t.Message != "" {
			for _, ml := range strings.Split(t.Message, "\n") {
				out = append(out, "              "+ml)
			}
		}
	}

	out = append(out, "")
	if d.Body != "" {
		out = append(out, strings.Split(d.Body, "\n")...)
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

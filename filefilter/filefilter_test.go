package filefilter

import "testing"

func TestParse_EmptyInputs(t *testing.T) {
	cases := []string{"", "   ", "\t"}
	for _, in := range cases {
		e, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q) err = %v", in, err)
		}
		if !e.IsEmpty() {
			t.Fatalf("Parse(%q) not empty", in)
		}
		if e.String() != "" {
			t.Fatalf("Parse(%q).String() = %q, want \"\"", in, e.String())
		}
	}
}

func TestParse_StoresTrimmedRaw(t *testing.T) {
	e, err := Parse("  *.go  ")
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if e.IsEmpty() {
		t.Fatalf("Parse(%q) should not be empty", "  *.go  ")
	}
	if got, want := e.String(), "*.go"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestMatch_EmptyExprMatchesEverything(t *testing.T) {
	var e Expr
	for _, p := range []string{"", "a", "deeply/nested/path.go"} {
		if !e.Match(p, "") {
			t.Errorf("empty Expr should match %q", p)
		}
	}
}

func TestMatch_GlobAnchoring(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		// No / => basename at any depth.
		{"*.go", "fizz.go", true},
		{"*.go", "foo/bar/fizz.go", true},
		{"*.go", "foo/bar/fizz.md", false},
		{"*.go", "fizz.go.bak", false},

		// Pattern with / => full path; * does not cross /.
		{"foo/*.go", "foo/x.go", true},
		{"foo/*.go", "foo/bar/x.go", false},
		{"foo/*.go", "bar/foo/x.go", false},

		// ** crosses /.
		{"**/*.go", "foo/bar/x.go", true},
		{"**/*.go", "x.go", true},
		{"foo/**/*.go", "foo/bar/baz/x.go", true},
		{"foo/**/*.go", "foo/x.go", true},
		{"foo/**/*.go", "bar/x.go", false},
	}
	for _, tc := range tests {
		e, err := Parse(tc.pattern)
		if err != nil {
			t.Fatalf("Parse(%q) err = %v", tc.pattern, err)
		}
		if got := e.Match(tc.path, ""); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestMatch_PlainStringIsSubstring(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		// No wildcards: substring against basename.
		{"main", "main.go", true},
		{"main.", "main.go", true},
		{"ain", "main.go", true},
		{"main", "loader/main.go", true},
		{"main", "loader/loader.go", false},
		{"xyz", "main.go", false},
		// No wildcards with `/`: substring against full path.
		{"loader/loader", "loader/loader.go", true},
		{"loader/loader", "main.go", false},
	}
	for _, tc := range tests {
		e, err := Parse(tc.pattern)
		if err != nil {
			t.Fatalf("Parse(%q) err = %v", tc.pattern, err)
		}
		if got := e.Match(tc.path, ""); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestMatch_PlainStringNegation(t *testing.T) {
	e, err := Parse("!main")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if e.Match("main.go", "") {
		t.Error("!main should exclude main.go")
	}
	if !e.Match("other.go", "") {
		t.Error("!main should keep other.go")
	}
}

func TestMatch_CaseInsensitive(t *testing.T) {
	cases := []struct {
		pattern, path string
	}{
		{"*.GO", "fizz.go"},
		{"*.go", "FIZZ.GO"},
		{"FOO/*.go", "foo/x.go"},
	}
	for _, c := range cases {
		e, _ := Parse(c.pattern)
		if !e.Match(c.path, "") {
			t.Errorf("expected case-insensitive match: pattern=%q path=%q", c.pattern, c.path)
		}
	}
}

func TestMatch_RenameMatchesEitherSide(t *testing.T) {
	e, _ := Parse("*.go")
	if !e.Match("README.md", "old.go") {
		t.Errorf("expected old-path match")
	}
	if !e.Match("new.go", "README.md") {
		t.Errorf("expected new-path match")
	}
	if e.Match("README.md", "other.md") {
		t.Errorf("neither side matches, want false")
	}
}

func TestMatch_OldPathEmptyIgnored(t *testing.T) {
	// An empty oldPath must not be passed to the matcher (otherwise a
	// pattern like `*` could spuriously match empty strings).
	e, _ := Parse("*")
	if !e.Match("foo.go", "") {
		t.Errorf("`*` should match the new path")
	}
}

func TestParse_MultiTerm(t *testing.T) {
	e, err := Parse("*.go,*.md")
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if e.IsEmpty() {
		t.Fatal("expected non-empty")
	}
	tests := []struct {
		path string
		want bool
	}{
		{"foo.go", true},
		{"foo.md", true},
		{"foo.txt", false},
		{"deep/nest/foo.go", true},
	}
	for _, tc := range tests {
		if got := e.Match(tc.path, ""); got != tc.want {
			t.Errorf("Match(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestParse_WhitespaceAroundTerms(t *testing.T) {
	a, _ := Parse("foo, bar")
	b, _ := Parse("foo,bar")
	for _, p := range []string{"foo", "bar", "baz", "deep/foo"} {
		if a.Match(p, "") != b.Match(p, "") {
			t.Errorf("whitespace handling diverged on %q: a=%v b=%v", p, a.Match(p, ""), b.Match(p, ""))
		}
	}
}

func TestParse_EmptyTermsDropped(t *testing.T) {
	cases := []string{",", ",,", "foo,", ",foo", "foo,,bar", "  ,  ,  "}
	for _, in := range cases {
		e, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q) err = %v", in, err)
		}
		// Just ensure it doesn't crash and isn't broken; specifics
		// covered by other tests.
		_ = e
	}

	// `foo,,bar` should match exactly as `foo,bar`.
	a, _ := Parse("foo,,bar")
	b, _ := Parse("foo,bar")
	for _, p := range []string{"foo", "bar", "baz"} {
		if a.Match(p, "") != b.Match(p, "") {
			t.Errorf("empty-term drop diverged on %q", p)
		}
	}
}

func TestMatch_Negation(t *testing.T) {
	e, err := Parse("!*_test.go")
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	tests := []struct {
		path string
		want bool
	}{
		// All-negative ⇒ "everything except".
		{"foo.go", true},
		{"foo_test.go", false},
		{"deep/nest/foo.go", true},
		{"deep/nest/foo_test.go", false},
	}
	for _, tc := range tests {
		if got := e.Match(tc.path, ""); got != tc.want {
			t.Errorf("Match(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestMatch_PositiveAndNegativeCombined(t *testing.T) {
	e, err := Parse("*.go,!*_test.go")
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	tests := []struct {
		path string
		want bool
	}{
		{"foo.go", true},
		{"foo_test.go", false},
		{"foo.md", false},
		{"deep/foo.go", true},
		{"deep/foo_test.go", false},
	}
	for _, tc := range tests {
		if got := e.Match(tc.path, ""); got != tc.want {
			t.Errorf("Match(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestMatch_NegativeRespectsRename(t *testing.T) {
	// A negative term that matches either side excludes the file.
	e, _ := Parse("!*_test.go")
	if e.Match("foo.go", "bar_test.go") {
		t.Errorf("rename whose old path matches negation should be excluded")
	}
	if e.Match("bar_test.go", "foo.go") {
		t.Errorf("rename whose new path matches negation should be excluded")
	}
	if !e.Match("foo.go", "bar.go") {
		t.Errorf("neither side matches negation, should be included")
	}
}

func TestParse_QuotedLiteralCommas(t *testing.T) {
	// A quoted term protects an embedded comma.
	e, err := Parse(`"foo,bar.go"`)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if !e.Match("foo,bar.go", "") {
		t.Error("expected literal comma to match inside quoted term")
	}
	if e.Match("foo.go", "") {
		t.Error("foo.go should not match `foo,bar.go`")
	}
}

func TestParse_QuotedTermAllowsSlashes(t *testing.T) {
	// Quoting preserves the slash as a literal path separator; with no
	// glob metacharacters the term substring-matches the full path.
	e, err := Parse(`"foo/bar.go"`)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if !e.Match("foo/bar.go", "") {
		t.Error("quoted foo/bar.go should match")
	}
	if !e.Match("baz/foo/bar.go", "") {
		t.Error("quoted foo/bar.go should substring-match anywhere in the path")
	}
	if e.Match("foo/other.go", "") {
		t.Error("quoted foo/bar.go should not match foo/other.go")
	}
}

func TestParse_QuotedTermGlobMetaActive(t *testing.T) {
	// Inside quotes, glob metacharacters are still active per the PRD.
	e, err := Parse(`"*.go"`)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if !e.Match("foo.go", "") {
		t.Error(`"*.go" should still match foo.go (glob meta active in quotes)`)
	}
}

func TestParse_NegatedQuoted(t *testing.T) {
	e, err := Parse(`!"foo,bar.go"`)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if e.Match("foo,bar.go", "") {
		t.Error("negated quoted term should exclude its literal match")
	}
	if !e.Match("foo.go", "") {
		t.Error("non-matching file should remain visible under sole negation")
	}
}

func TestParse_UnclosedQuoteReturnsError(t *testing.T) {
	if _, err := Parse(`"foo`); err == nil {
		t.Error("expected error for unclosed quote")
	}
}

func TestParse_QuotedAndUnquotedMixed(t *testing.T) {
	// Multi-term mix: quoted comma-bearing term + plain glob.
	e, err := Parse(`"weird,name.go",*.md`)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if !e.Match("weird,name.go", "") {
		t.Error("quoted comma term should match")
	}
	if !e.Match("README.md", "") {
		t.Error("plain glob should match")
	}
	if e.Match("foo.go", "") {
		t.Error("non-matching path should be excluded")
	}
}

func TestParse_RegexBasic(t *testing.T) {
	e, err := Parse(`/foo/`)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if e.IsEmpty() {
		t.Fatal("expected non-empty")
	}
	// Unanchored substring semantics.
	if !e.Match("foo.go", "") {
		t.Error("regex `foo` should match `foo.go`")
	}
	if !e.Match("a/b/foobar", "") {
		t.Error("regex `foo` should substring-match `a/b/foobar`")
	}
	if e.Match("bar.go", "") {
		t.Error("regex `foo` should not match `bar.go`")
	}
}

func TestParse_RegexCaseSensitiveByDefault(t *testing.T) {
	e, err := Parse(`/Foo/`)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if e.Match("foo.go", "") {
		t.Error("regex `/Foo/` should be case-sensitive — `foo.go` should not match")
	}
	if !e.Match("Foo.go", "") {
		t.Error("regex `/Foo/` should match `Foo.go`")
	}
}

func TestParse_RegexCaseInsensitiveOptIn(t *testing.T) {
	e, err := Parse(`/(?i)foo/`)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if !e.Match("FOO.go", "") {
		t.Error("regex `(?i)foo` should match `FOO.go`")
	}
}

func TestParse_RegexMetacharacters(t *testing.T) {
	// Demonstrates regex expressing something a glob cannot: alternation.
	e, err := Parse(`/\.(go|md)$/`)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if !e.Match("foo.go", "") {
		t.Error("regex `\\.(go|md)$` should match `foo.go`")
	}
	if !e.Match("README.md", "") {
		t.Error("regex `\\.(go|md)$` should match `README.md`")
	}
	if e.Match("foo.txt", "") {
		t.Error("regex `\\.(go|md)$` should not match `foo.txt`")
	}
}

func TestParse_RegexAllowsCommaInside(t *testing.T) {
	// Commas inside /.../ should be literal — not term separators.
	e, err := Parse(`/foo,bar/`)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if !e.Match("foo,bar.txt", "") {
		t.Error("regex `foo,bar` should match `foo,bar.txt` (literal comma)")
	}
}

func TestParse_RegexAllowsSlashInside(t *testing.T) {
	// A `/` mid-term is part of the regex; the closing `/` is the one
	// followed by comma-or-EOF.
	e, err := Parse(`/foo/bar/`)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if !e.Match("a/foo/bar/x", "") {
		t.Error("regex `foo/bar` should match `a/foo/bar/x`")
	}
	if e.Match("foo.go", "") {
		t.Error("regex `foo/bar` should not match `foo.go`")
	}
}

func TestParse_RegexNegated(t *testing.T) {
	e, err := Parse(`!/_test\.go$/`)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if e.Match("foo_test.go", "") {
		t.Error("negated regex should exclude `foo_test.go`")
	}
	if !e.Match("foo.go", "") {
		t.Error("sole negative should allow `foo.go`")
	}
}

func TestParse_RegexMatchesRenameEitherSide(t *testing.T) {
	e, _ := Parse(`/foo/`)
	if !e.Match("README.md", "foo_old.txt") {
		t.Error("regex should match against old path")
	}
	if !e.Match("foo_new.txt", "README.md") {
		t.Error("regex should match against new path")
	}
	if e.Match("README.md", "CHANGELOG.md") {
		t.Error("neither side matches, should be excluded")
	}
}

func TestParse_RegexMixedWithGlob(t *testing.T) {
	// Mixed expression: regex for tests + glob include.
	e, err := Parse(`*.go,!/_test\.go$/`)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	tests := []struct {
		path string
		want bool
	}{
		{"foo.go", true},
		{"foo_test.go", false},
		{"foo.md", false},
		{"deep/nest/foo.go", true},
		{"deep/nest/foo_test.go", false},
	}
	for _, tc := range tests {
		if got := e.Match(tc.path, ""); got != tc.want {
			t.Errorf("Match(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestParse_RegexNoClosingFallsBackToGlob(t *testing.T) {
	// `/foo` with no closing `/` is parsed as a glob (story 32). Glob
	// `/foo` is a slash-containing pattern, which never matches a path
	// that doesn't start with `/` — so its behaviour is "matches
	// nothing" in normal use, but it must not error.
	e, err := Parse(`/foo`)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if e.IsEmpty() {
		t.Fatal("expected non-empty (glob fallback term)")
	}
	if e.Match("foo.go", "") {
		t.Error("glob `/foo` should not match `foo.go`")
	}
}

func TestParse_RegexInvalidReturnsError(t *testing.T) {
	if _, err := Parse(`/[/`); err == nil {
		t.Error("expected error for invalid regex")
	}
}

func TestParse_RegexStringRoundTrip(t *testing.T) {
	// String() should preserve the raw expression including delimiters.
	e, err := Parse(`/foo/,*.md`)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if got, want := e.String(), `/foo/,*.md`; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestParsePartial_EmptyInput(t *testing.T) {
	expr, tokens, err := ParsePartial("")
	if err != nil {
		t.Fatalf("ParsePartial(\"\") err = %v", err)
	}
	if !expr.IsEmpty() {
		t.Error("empty input should yield empty Expr")
	}
	if len(tokens) != 0 {
		t.Errorf("empty input should yield no tokens, got %d", len(tokens))
	}
}

func TestParsePartial_ValidGlob(t *testing.T) {
	expr, tokens, err := ParsePartial("*.go")
	if err != nil {
		t.Fatalf("ParsePartial err = %v", err)
	}
	if len(tokens) != 1 || !tokens[0].Valid || tokens[0].Kind != KindGlob {
		t.Fatalf("tokens = %+v", tokens)
	}
	if !expr.Match("foo.go", "") {
		t.Error("Expr should still match *.go")
	}
}

func TestParsePartial_ValidRegex(t *testing.T) {
	expr, tokens, err := ParsePartial(`/foo/`)
	if err != nil {
		t.Fatalf("ParsePartial err = %v", err)
	}
	if len(tokens) != 1 || !tokens[0].Valid || tokens[0].Kind != KindRegex {
		t.Fatalf("tokens = %+v", tokens)
	}
	if tokens[0].Pattern != "foo" {
		t.Errorf("Pattern = %q, want %q", tokens[0].Pattern, "foo")
	}
	if !expr.Match("foo.txt", "") {
		t.Error("regex /foo/ should match foo.txt")
	}
}

func TestParsePartial_InvalidRegexReportedNotErrored(t *testing.T) {
	expr, tokens, err := ParsePartial(`/[/`)
	if err != nil {
		t.Fatalf("ParsePartial err = %v, want nil (invalid regex should not error)", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("tokens = %+v, want 1 token", tokens)
	}
	if tokens[0].Valid {
		t.Error("invalid regex token should report Valid=false")
	}
	if tokens[0].Kind != KindRegex {
		t.Error("invalid regex token should still report Kind=KindRegex")
	}
	if !expr.IsEmpty() {
		t.Error("Expr should be empty when the only token is invalid")
	}
}

func TestParsePartial_MixedValidGlobAndInvalidRegex(t *testing.T) {
	// `*.go` parses as a glob; `/[/` fails to compile.
	expr, tokens, err := ParsePartial(`*.go,/[/`)
	if err != nil {
		t.Fatalf("ParsePartial err = %v", err)
	}
	if len(tokens) != 2 {
		t.Fatalf("len(tokens) = %d, want 2", len(tokens))
	}
	if !tokens[0].Valid || tokens[0].Kind != KindGlob {
		t.Errorf("tokens[0] = %+v", tokens[0])
	}
	if tokens[1].Valid || tokens[1].Kind != KindRegex {
		t.Errorf("tokens[1] = %+v", tokens[1])
	}
	// The Expr still filters by *.go even though the regex was invalid.
	if !expr.Match("foo.go", "") {
		t.Error("Expr should match foo.go (the valid glob still applies)")
	}
	if expr.Match("foo.txt", "") {
		t.Error("Expr should not match foo.txt — the glob still rejects it")
	}
}

func TestParsePartial_UnclosedQuoteStillErrors(t *testing.T) {
	_, _, err := ParsePartial(`"foo`)
	if err == nil {
		t.Error("ParsePartial should still error on unclosed quote")
	}
}

func TestParsePartial_NegationFlagPreserved(t *testing.T) {
	expr, tokens, err := ParsePartial(`!*_test.go,!/(/`)
	if err != nil {
		t.Fatalf("ParsePartial err = %v", err)
	}
	if len(tokens) != 2 {
		t.Fatalf("len(tokens) = %d, want 2", len(tokens))
	}
	if !tokens[0].Negate || !tokens[1].Negate {
		t.Errorf("Negate flags not preserved: %+v", tokens)
	}
	if tokens[0].Valid != true || tokens[1].Valid != false {
		t.Errorf("expected first valid, second invalid; got %+v", tokens)
	}
	// Expr only contains the valid negative glob.
	if expr.Match("foo_test.go", "") {
		t.Error("negated glob still applies — foo_test.go should be excluded")
	}
	if !expr.Match("foo.go", "") {
		t.Error("non-test path should pass")
	}
}

func TestExprFromTokens_BuildsEquivalentExpr(t *testing.T) {
	_, tokens, err := ParsePartial("*.go,/foo/")
	if err != nil {
		t.Fatalf("ParsePartial err = %v", err)
	}
	e := ExprFromTokens("*.go,/foo/", tokens)
	if e.String() != "*.go,/foo/" {
		t.Errorf("String() = %q, want %q", e.String(), "*.go,/foo/")
	}
	if !e.Match("foo.go", "") {
		t.Error("foo.go should match (glob)")
	}
	if !e.Match("foobar.txt", "") {
		t.Error("foobar.txt should match (regex)")
	}
	if e.Match("README.md", "") {
		t.Error("README.md should not match")
	}
}

func TestExprFromTokens_OmitsInvalidTokens(t *testing.T) {
	_, tokens, _ := ParsePartial(`*.go,/[/`)
	// Manually-built Expr should drop the invalid regex.
	e := ExprFromTokens(`*.go,/[/`, tokens)
	if !e.Match("foo.go", "") {
		t.Error("valid glob should still apply")
	}
	if e.Match("foo.txt", "") {
		t.Error("invalid regex should not contribute")
	}
}

func TestExprFromTokens_AllowsCallerSubstitution(t *testing.T) {
	// Simulate the model's debounce: first parse yields a valid /foo/
	// regex; second parse yields the same position as an invalid regex.
	// The model carries the first parse's Token forward into the second
	// build to keep the prior compiled regex active.
	_, first, err := ParsePartial(`/foo/`)
	if err != nil {
		t.Fatalf("first parse err = %v", err)
	}
	if len(first) != 1 || !first[0].Valid {
		t.Fatalf("first parse invalid: %+v", first)
	}
	_, second, err := ParsePartial(`/foo[/`)
	if err != nil {
		t.Fatalf("second parse err = %v", err)
	}
	if len(second) != 1 || second[0].Valid {
		t.Fatalf("second parse should have one invalid token: %+v", second)
	}
	// Substitute the prior valid Token in for the invalid one.
	merged := []Token{first[0]}
	e := ExprFromTokens(`/foo[/`, merged)
	if !e.Match("foobar.txt", "") {
		t.Error("substituted prior regex should still match")
	}
}

func TestParse_PreservesBehaviorWhenInputIsValid(t *testing.T) {
	// Parse and ParsePartial agree on Match results for any input that
	// has no invalid regex tokens — this is the slice-3 invariant we
	// must not regress.
	for _, in := range []string{
		"*.go",
		"*.go,*.md",
		`!*_test.go`,
		`"weird,name.go"`,
		`/foo/`,
		`*.go,!/_test\.go$/`,
	} {
		a, _ := Parse(in)
		b, _, _ := ParsePartial(in)
		for _, p := range []string{"foo.go", "foo_test.go", "README.md", "weird,name.go", "deep/nest/foo.go"} {
			if a.Match(p, "") != b.Match(p, "") {
				t.Errorf("Parse vs ParsePartial diverged on input=%q path=%q", in, p)
			}
		}
	}
}

func TestParse_InvalidRegexStillErrors(t *testing.T) {
	// The strict Parse path is unchanged: invalid regex returns an
	// error. ParsePartial is the only tolerant variant.
	if _, err := Parse(`/[/`); err == nil {
		t.Error("Parse should still error on invalid regex (only ParsePartial is tolerant)")
	}
}

func TestParse_QuotedTermPreservesInnerWhitespace(t *testing.T) {
	// Whitespace inside quotes is literal; outside is trimmed.
	e, err := Parse(`  "  foo.go  "  `)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if !e.Match("  foo.go  ", "") {
		t.Error("inner whitespace should be preserved literally")
	}
	if e.Match("foo.go", "") {
		t.Error("trimmed name should not match literal-whitespace pattern")
	}
}

func TestParsePartial_TokenSpans(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []struct {
			start, end int
			negate     bool
			kind       TermKind
			valid      bool
			pattern    string
		}
	}{
		{
			name:  "bare glob",
			input: "foo",
			want: []struct {
				start, end int
				negate     bool
				kind       TermKind
				valid      bool
				pattern    string
			}{{0, 3, false, KindGlob, true, "foo"}},
		},
		{
			name:  "negated glob",
			input: "!foo",
			want: []struct {
				start, end int
				negate     bool
				kind       TermKind
				valid      bool
				pattern    string
			}{{0, 4, true, KindGlob, true, "foo"}},
		},
		{
			name:  "regex span includes slashes",
			input: "/foo/",
			want: []struct {
				start, end int
				negate     bool
				kind       TermKind
				valid      bool
				pattern    string
			}{{0, 5, false, KindRegex, true, "foo"}},
		},
		{
			name:  "negated regex",
			input: "!/foo/",
			want: []struct {
				start, end int
				negate     bool
				kind       TermKind
				valid      bool
				pattern    string
			}{{0, 6, true, KindRegex, true, "foo"}},
		},
		{
			name:  "invalid regex still spans delimiters",
			input: "/[/",
			want: []struct {
				start, end int
				negate     bool
				kind       TermKind
				valid      bool
				pattern    string
			}{{0, 3, false, KindRegex, false, "["}},
		},
		{
			name:  "two glob tokens comma-separated",
			input: "foo, bar",
			want: []struct {
				start, end int
				negate     bool
				kind       TermKind
				valid      bool
				pattern    string
			}{
				{0, 3, false, KindGlob, true, "foo"},
				{5, 8, false, KindGlob, true, "bar"},
			},
		},
		{
			name:  "glob with trailing whitespace before comma",
			input: "foo   , bar",
			want: []struct {
				start, end int
				negate     bool
				kind       TermKind
				valid      bool
				pattern    string
			}{
				{0, 3, false, KindGlob, true, "foo"},
				{8, 11, false, KindGlob, true, "bar"},
			},
		},
		{
			name:  "quoted glob token span covers quotes",
			input: `"a,b"`,
			want: []struct {
				start, end int
				negate     bool
				kind       TermKind
				valid      bool
				pattern    string
			}{{0, 5, false, KindGlob, true, "a,b"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, toks, err := ParsePartial(tc.input)
			if err != nil {
				t.Fatalf("ParsePartial(%q) err = %v", tc.input, err)
			}
			if len(toks) != len(tc.want) {
				t.Fatalf("got %d tokens, want %d (%+v)", len(toks), len(tc.want), toks)
			}
			for i, w := range tc.want {
				got := toks[i]
				if got.Start != w.start || got.End != w.end {
					t.Errorf("tok[%d] span = (%d,%d), want (%d,%d); slice=%q",
						i, got.Start, got.End, w.start, w.end, tc.input[got.Start:got.End])
				}
				if got.Negate != w.negate {
					t.Errorf("tok[%d] negate = %v, want %v", i, got.Negate, w.negate)
				}
				if got.Kind != w.kind {
					t.Errorf("tok[%d] kind = %v, want %v", i, got.Kind, w.kind)
				}
				if got.Valid != w.valid {
					t.Errorf("tok[%d] valid = %v, want %v", i, got.Valid, w.valid)
				}
				if got.Pattern != w.pattern {
					t.Errorf("tok[%d] pattern = %q, want %q", i, got.Pattern, w.pattern)
				}
			}
		})
	}
}

func TestToken_Matches(t *testing.T) {
	mustToken := func(input string) Token {
		_, toks, err := ParsePartial(input)
		if err != nil {
			t.Fatalf("ParsePartial(%q): %v", input, err)
		}
		if len(toks) != 1 {
			t.Fatalf("ParsePartial(%q) tokens = %d, want 1", input, len(toks))
		}
		return toks[0]
	}

	t.Run("glob basename match", func(t *testing.T) {
		tk := mustToken("*.go")
		if !tk.Matches("main.go", "") {
			t.Error("*.go should match main.go")
		}
		if tk.Matches("README.md", "") {
			t.Error("*.go should not match README.md")
		}
	})

	t.Run("matches ignores negate flag", func(t *testing.T) {
		tk := mustToken("!*.go")
		if !tk.Negate {
			t.Fatal("expected token to be negated")
		}
		if !tk.Matches("main.go", "") {
			t.Error("Matches must ignore Negate — pattern *.go still matches main.go")
		}
	})

	t.Run("regex valid", func(t *testing.T) {
		tk := mustToken("/^main\\.go$/")
		if !tk.Valid {
			t.Fatal("regex should be valid")
		}
		if !tk.Matches("main.go", "") {
			t.Error("regex should match main.go")
		}
		if tk.Matches("xmain.go", "") {
			t.Error("anchored regex should not match xmain.go")
		}
	})

	t.Run("invalid regex never matches", func(t *testing.T) {
		tk := mustToken("/[/")
		if tk.Valid {
			t.Fatal("regex should be invalid")
		}
		if tk.Matches("anything", "") {
			t.Error("invalid regex must not match anything")
		}
	})

	t.Run("matches oldPath too", func(t *testing.T) {
		tk := mustToken("old.go")
		if !tk.Matches("new.go", "old.go") {
			t.Error("Matches should check oldPath when path doesn't match")
		}
	})
}

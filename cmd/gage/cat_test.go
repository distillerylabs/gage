package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/denmark/gage/internal/gage"
)

// colonColumn returns the byte index of the first ':' on a line, or -1.
func colonColumn(line string) int {
	return strings.Index(line, ":")
}

// TestAlignCatOutputInsertsIDLineAfterTitle covers the headline behavior:
// an id: line, carrying the entry's short id (the same 8 hex chars ls
// would print for it), immediately follows title.
func TestAlignCatOutputInsertsIDLineAfterTitle(t *testing.T) {
	id := uuid.New()
	data, err := gage.MarshalEntry(gage.Entry{Title: "ProtonMail", Value: "v"})
	if err != nil {
		t.Fatal(err)
	}

	out := string(alignCatOutput(id, data))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("output has fewer than 2 lines:\n%s", out)
	}
	if !strings.HasPrefix(lines[0], "title") {
		t.Fatalf("line 1 = %q, want it to start with title", lines[0])
	}
	// Padding width depends on the widest top-level label present
	// (updated_by, here), not on title's own length, so match loosely
	// rather than hardcode a column.
	wantIDLine := regexp.MustCompile(`^id\s*: ` + regexp.QuoteMeta(shortEntryID(id.String())) + `$`)
	if !wantIDLine.MatchString(lines[1]) {
		t.Errorf("line 2 = %q, want it to match %s", lines[1], wantIDLine)
	}
}

// TestAlignCatOutputAlignsTopLevelColumn asserts every top-level line's
// colon lands in the same column, and that the column adapts to the
// widest label present rather than being hardcoded — an entry without
// description/fields must not reserve description's width.
func TestAlignCatOutputAlignsTopLevelColumn(t *testing.T) {
	for _, tc := range []struct {
		name string
		e    gage.Entry
	}{
		{"minimal", gage.Entry{Title: "X", Value: "v"}},
		{"with-description", gage.Entry{Title: "X", Description: "a description", Value: "v"}},
		{"with-fields", gage.Entry{Title: "X", Value: "v", Fields: map[string]string{"username": "u", "totp_seed": "t"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := uuid.New()
			data, err := gage.MarshalEntry(tc.e)
			if err != nil {
				t.Fatal(err)
			}
			out := string(alignCatOutput(id, data))

			var col = -1
			for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
				if line == "" || strings.HasPrefix(line, " ") {
					continue // fields sub-key or block-scalar content line
				}
				c := colonColumn(line)
				if c < 0 {
					t.Fatalf("top-level line %q has no colon", line)
				}
				if col == -1 {
					col = c
					continue
				}
				if c != col {
					t.Errorf("line %q: colon at column %d, want %d (misaligned)", line, c, col)
				}
			}
		})
	}
}

// TestAlignCatOutputAlignsFieldsIndependently checks fields' own keys
// align to each other, on a column independent of the top-level block —
// a short top-level label set must not force fields' alignment width (or
// vice versa).
func TestAlignCatOutputAlignsFieldsIndependently(t *testing.T) {
	id := uuid.New()
	data, err := gage.MarshalEntry(gage.Entry{
		Title: "X",
		Value: "v",
		Fields: map[string]string{
			"username":  "u",
			"totp_seed": "t",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := string(alignCatOutput(id, data))

	var fieldCol = -1
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if !strings.HasPrefix(line, " ") {
			continue
		}
		c := colonColumn(line)
		if c < 0 {
			t.Fatalf("fields sub-key line %q has no colon", line)
		}
		if fieldCol == -1 {
			fieldCol = c
			continue
		}
		if c != fieldCol {
			t.Errorf("line %q: colon at column %d, want %d", line, c, fieldCol)
		}
	}
	if fieldCol == -1 {
		t.Fatal("no fields sub-key lines found")
	}
}

// TestAlignCatOutputRoundTrips ensures the padded, id-injected output is
// still valid YAML that UnmarshalEntry reads back correctly — the
// synthetic id key lands in Extra and everything else survives.
func TestAlignCatOutputRoundTrips(t *testing.T) {
	id := uuid.New()
	want := gage.Entry{
		Title:       "ProtonMail",
		Description: "Personal email",
		UpdatedBy:   "yubikey-5c-nfc-1",
		Value:       "correcthorsebatterystaple",
		Fields:      map[string]string{"username": "me@proton.me", "totp_seed": "JBSWY3DPEHPK3PXP"},
	}
	data, err := gage.MarshalEntry(want)
	if err != nil {
		t.Fatal(err)
	}
	out := alignCatOutput(id, data)

	got, err := gage.UnmarshalEntry(out)
	if err != nil {
		t.Fatalf("output did not parse back as YAML: %v\n%s", err, out)
	}
	if got.Title != want.Title || got.Description != want.Description ||
		got.UpdatedBy != want.UpdatedBy || got.Value != want.Value {
		t.Errorf("round-tripped entry = %+v, want %+v", got, want)
	}
	if len(got.Fields) != len(want.Fields) {
		t.Errorf("fields = %v, want %v", got.Fields, want.Fields)
	}
	for k, v := range want.Fields {
		if got.Fields[k] != v {
			t.Errorf("field %q = %q, want %q", k, got.Fields[k], v)
		}
	}
}

// TestAlignCatOutputDedentsMultilineValue is the headline behavior this
// covers: a multi-line value's block-scalar content is printed flush
// left, with none of MarshalEntry's underlying YAML indentation and none
// of the label padding applied to key lines — exactly the original
// lines the value was made of.
func TestAlignCatOutputDedentsMultilineValue(t *testing.T) {
	id := uuid.New()
	value := "line one\nline two\nline three"
	data, err := gage.MarshalEntry(gage.Entry{Title: "X", Value: value})
	if err != nil {
		t.Fatal(err)
	}
	out := string(alignCatOutput(id, data))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	valueLine := findLine(t, lines, `^value\s*:\s*\|`)
	want := strings.Split(value, "\n")
	if valueLine+len(want) > len(lines) {
		t.Fatalf("output has too few lines after the value header:\n%s", out)
	}
	for i, w := range want {
		if got := lines[valueLine+1+i]; got != w {
			t.Errorf("content line %d = %q, want %q (unindented)", i, got, w)
		}
	}
}

// TestAlignCatOutputDedentsFieldsSubValue covers the same flush-left
// dedent for a multi-line value nested inside fields, which
// MarshalEntry's emitter indents twice as deep as a top-level value.
func TestAlignCatOutputDedentsFieldsSubValue(t *testing.T) {
	id := uuid.New()
	fieldValue := "a\nb\nc"
	data, err := gage.MarshalEntry(gage.Entry{
		Title: "X",
		Value: "v",
		Fields: map[string]string{
			"note": fieldValue,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := string(alignCatOutput(id, data))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	noteLine := findLine(t, lines, `^\s*note\s*:\s*\|`)
	want := strings.Split(fieldValue, "\n")
	if noteLine+len(want) > len(lines) {
		t.Fatalf("output has too few lines after the note header:\n%s", out)
	}
	for i, w := range want {
		if got := lines[noteLine+1+i]; got != w {
			t.Errorf("content line %d = %q, want %q (unindented)", i, got, w)
		}
	}
}

// TestAlignCatOutputDedentsValueFollowedByMoreKeys checks the dedent
// span's end boundary: a multi-line value that is *not* the last
// top-level key must stop dedenting exactly at its own content, and the
// key that follows (fields, here) must still be found and aligned —
// proving the span comes from the document structure, not from scanning
// for the next line that happens to look like a key.
func TestAlignCatOutputDedentsValueFollowedByMoreKeys(t *testing.T) {
	id := uuid.New()
	value := "alpha\nbeta"
	data, err := gage.MarshalEntry(gage.Entry{
		Title:  "X",
		Value:  value,
		Fields: map[string]string{"k": "v"},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := string(alignCatOutput(id, data))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	valueLine := findLine(t, lines, `^value\s*:\s*\|`)
	if lines[valueLine+1] != "alpha" || lines[valueLine+2] != "beta" {
		t.Fatalf("value content = %q, %q, want %q, %q", lines[valueLine+1], lines[valueLine+2], "alpha", "beta")
	}
	fieldsLine := findLine(t, lines, `^fields\s*:`)
	if fieldsLine != valueLine+3 {
		t.Errorf("fields: at line %d, want %d (right after the value's 2 lines)", fieldsLine, valueLine+3)
	}
}

// TestAlignCatOutputSkipsAlignmentForQuotedKeys covers the
// corruption-avoidance fallback: a field name that itself needs quoting
// (contains ": ") must not be misparsed as a plain key for alignment
// purposes. Its own line is left unpadded, and it doesn't perturb the
// width of the other (bare) field keys.
func TestAlignCatOutputSkipsAlignmentForQuotedKeys(t *testing.T) {
	id := uuid.New()
	data, err := gage.MarshalEntry(gage.Entry{
		Title: "X",
		Value: "v",
		Fields: map[string]string{
			"weird: key": "val1",
			"username":   "val2",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := alignCatOutput(id, data)

	got, err := gage.UnmarshalEntry(out)
	if err != nil {
		t.Fatalf("output did not parse back as YAML: %v\n%s", err, out)
	}
	if got.Fields["weird: key"] != "val1" || got.Fields["username"] != "val2" {
		t.Errorf("fields round-tripped wrong: %v", got.Fields)
	}

	if !strings.Contains(string(out), "'weird: key': val1") {
		t.Errorf("quoted key line was altered, want it left as-is:\n%s", out)
	}
}

// TestAlignCatOutputLeavesMultilineValueContentAlone is the key
// correctness property behind parsing the YAML tree rather than
// pattern-matching lines: a multi-line secret whose content happens to
// look exactly like "key: value" must never be mistaken for a real
// top-level or fields key and get padded or misclassified — and its
// span (where the flush-left dedented content starts and ends) must be
// read off the document structure, not guessed from indentation, so it
// doesn't bleed into the following fields: block.
//
// The output is no longer expected to round-trip through
// gage.UnmarshalEntry once a value spans multiple lines (see
// alignCatOutput's doc comment on that trade-off), so this asserts on
// the rendered lines directly instead.
func TestAlignCatOutputLeavesMultilineValueContentAlone(t *testing.T) {
	id := uuid.New()
	trickyValue := "note: this looks like yaml\nsecond line"
	data, err := gage.MarshalEntry(gage.Entry{
		Title: "X",
		Value: trickyValue,
		Fields: map[string]string{
			"username": "u",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := string(alignCatOutput(id, data))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	valueLine := findLine(t, lines, `^value\s*:`)
	if valueLine+2 >= len(lines) {
		t.Fatalf("output has no room for the value's 2 content lines after its header:\n%s", out)
	}
	if lines[valueLine+1] != "note: this looks like yaml" || lines[valueLine+2] != "second line" {
		t.Errorf("value content = %q, %q; want the tricky value's own lines, flush left and untouched",
			lines[valueLine+1], lines[valueLine+2])
	}

	fieldsLine := findLine(t, lines, `^fields\s*:`)
	if fieldsLine != valueLine+3 {
		t.Fatalf("fields: header at line %d, want it immediately after the value's 2 content lines (line %d) — "+
			"content bled past its own span:\n%s", fieldsLine, valueLine+3, out)
	}
	if fieldsLine+1 >= len(lines) || !regexp.MustCompile(`^\s+username\s*: u$`).MatchString(lines[fieldsLine+1]) {
		t.Errorf("line after fields: = %q, want a correctly rendered username sub-key", lines[fieldsLine+1])
	}
}

// findLine returns the index of the one line in lines matching pattern,
// failing the test if there isn't exactly one.
func findLine(t *testing.T, lines []string, pattern string) int {
	t.Helper()
	re := regexp.MustCompile(pattern)
	found := -1
	for i, l := range lines {
		if re.MatchString(l) {
			if found != -1 {
				t.Fatalf("pattern %s matches more than one line", pattern)
			}
			found = i
		}
	}
	if found == -1 {
		t.Fatalf("no line matches %s:\n%s", pattern, strings.Join(lines, "\n"))
	}
	return found
}

// TestCatCommandPrintsIDLineThroughTheCLI is the CLI-level regression
// test: a real `gage cat` includes the entry's short id, matching what
// `ls` prints for the same entry.
func TestCatCommandPrintsIDLineThroughTheCLI(t *testing.T) {
	isolateXDG(t)
	path := initEntryTestVault(t, "personal")

	if res, _ := runCLIWithValue(t, []string{"insert", "ProtonMail"}, "secret"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	wantID := shortEntryID(soleEntryID(t, path).String())

	cat := runCLI(t, []string{"cat", "ProtonMail"}, "")
	if cat.Code != 0 {
		t.Fatalf("cat failed: %s", cat.Stderr)
	}
	wantIDLine := regexp.MustCompile(`(?m)^id\s*: ` + regexp.QuoteMeta(wantID) + `$`)
	if !wantIDLine.MatchString(cat.Stdout) {
		t.Errorf("cat output missing id line for %s:\n%s", wantID, cat.Stdout)
	}

	e, err := gage.UnmarshalEntry([]byte(cat.Stdout))
	if err != nil {
		t.Fatalf("cat output did not round-trip: %v\n%s", err, cat.Stdout)
	}
	if e.Title != "ProtonMail" || e.Value != "secret" {
		t.Errorf("round-tripped entry = %+v", e)
	}
}

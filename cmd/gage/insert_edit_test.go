package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
)

// TestInsertEditOpensTemplateAndInsertsEditedValueAndFields.
func TestInsertEditOpensTemplateAndInsertsEditedValueAndFields(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	setFakeEditor(t, "set-value", map[string]string{"GAGE_TEST_FAKE_EDITOR_VALUE": "edited-value"})
	res := runCLI(t, []string{"insert", "ProtonMail", "--description", "personal email", "-e"}, "")
	if res.Code != 0 {
		t.Fatalf("insert -e failed: %s", res.Stderr)
	}

	got := catEntry(t, "ProtonMail")
	if got.Value != "edited-value" {
		t.Errorf("Value = %q, want %q", got.Value, "edited-value")
	}
	if got.Description != "personal email" {
		t.Errorf("Description = %q, want the pre-filled %q", got.Description, "personal email")
	}
}

// TestInsertEditOpensAPreFilledTemplateWithEmptyValueAndFields asserts
// what the editor was actually handed, which is the half of the bullet
// the round-trip tests can't see: title and description pre-filled from
// the command line, value empty, and no fields at all. The fake editor
// records the file's contents before touching it, since editYAML deletes
// the scratch file before returning.
func TestInsertEditOpensAPreFilledTemplateWithEmptyValueAndFields(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	template := filepath.Join(t.TempDir(), "template.yaml")
	setFakeEditor(t, "set-value", map[string]string{
		"GAGE_TEST_FAKE_EDITOR_VALUE": "v",
		fakeEditorTemplateEnvVar:      template,
	})
	if res := runCLI(t, []string{"insert", "ProtonMail", "--description", "personal email", "-e"}, ""); res.Code != 0 {
		t.Fatalf("insert -e failed: %s", res.Stderr)
	}

	data, err := os.ReadFile(template)
	if err != nil {
		t.Fatalf("the fake editor never recorded the template: %v", err)
	}
	seeded, err := gage.UnmarshalEntry(data)
	if err != nil {
		t.Fatalf("the template gage opened doesn't parse as an Entry: %v\n%s", err, data)
	}

	if seeded.Title != "ProtonMail" {
		t.Errorf("template title = %q, want the <title> argument %q pre-filled", seeded.Title, "ProtonMail")
	}
	if seeded.Description != "personal email" {
		t.Errorf("template description = %q, want --description %q pre-filled", seeded.Description, "personal email")
	}
	if seeded.Value != "" {
		t.Errorf("template value = %q, want it empty", seeded.Value)
	}
	if len(seeded.Fields) != 0 {
		t.Errorf("template fields = %v, want none", seeded.Fields)
	}
	// An empty `fields:` key would be noise in a file a human edits; the
	// entry format omits it entirely when empty.
	if strings.Contains(string(data), "fields:") {
		t.Errorf("template carries an empty fields: key:\n%s", data)
	}
}

// TestInsertEditSetsFieldsAtCreation: insert -e is the one insert mode
// that can populate `fields` directly at creation time — every other
// insert mode leaves fields empty until a follow-up gage edit.
func TestInsertEditSetsFieldsAtCreation(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	setFakeEditor(t, "set-value-and-field", map[string]string{
		"GAGE_TEST_FAKE_EDITOR_VALUE":       "edited-value",
		"GAGE_TEST_FAKE_EDITOR_FIELD_KEY":   "username",
		"GAGE_TEST_FAKE_EDITOR_FIELD_VALUE": "me@example.com",
	})
	res := runCLI(t, []string{"insert", "GitHub", "-e"}, "")
	if res.Code != 0 {
		t.Fatalf("insert -e failed: %s", res.Stderr)
	}

	got := catEntry(t, "GitHub")
	if got.Value != "edited-value" {
		t.Errorf("Value = %q, want %q", got.Value, "edited-value")
	}
	if got.Fields["username"] != "me@example.com" {
		t.Errorf("Fields[username] = %q, want %q (fields = %v)", got.Fields["username"], "me@example.com", got.Fields)
	}
}

// TestInsertEditAbortsWithNoEntryWrittenAndNoCommitIfUnchanged.
func TestInsertEditAbortsWithNoEntryWrittenAndNoCommitIfUnchanged(t *testing.T) {
	isolateXDG(t)
	path := initEntryTestVault(t, "personal")
	before, err := gitrepo.CommitCount(path)
	if err != nil {
		t.Fatal(err)
	}

	setFakeEditor(t, "noop", nil)
	res := runCLI(t, []string{"insert", "ProtonMail", "-e"}, "")
	if res.Code == 0 {
		t.Fatal("expected insert -e to abort on an unchanged file")
	}

	after, err := gitrepo.CommitCount(path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("commit count changed (%d -> %d) on an aborted insert -e", before, after)
	}

	cat := runCLI(t, []string{"cat", "ProtonMail"}, "")
	if cat.Code != int(exitcode.NotFound) {
		t.Errorf("an entry was written despite the abort: cat exit code = %d, want %d (NotFound)", cat.Code, exitcode.NotFound)
	}
}

// TestInsertEditAbortsIfValueAndFieldsBothStillEmpty: changing only
// description (title/description are pre-filled and can be touched
// without counting as "something to save") must still abort.
func TestInsertEditAbortsIfValueAndFieldsBothStillEmpty(t *testing.T) {
	isolateXDG(t)
	path := initEntryTestVault(t, "personal")
	before, err := gitrepo.CommitCount(path)
	if err != nil {
		t.Fatal(err)
	}

	setFakeEditor(t, "set-description-only", map[string]string{"GAGE_TEST_FAKE_EDITOR_DESCRIPTION": "changed"})
	res := runCLI(t, []string{"insert", "ProtonMail", "-e"}, "")
	if res.Code == 0 {
		t.Fatal("expected insert -e to abort when value and fields are both still empty")
	}

	after, err := gitrepo.CommitCount(path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("commit count changed (%d -> %d) on an aborted insert -e", before, after)
	}
}

// TestInsertEditChangedTitleUsedForSavedEntryAndDuplicateCheck: editing
// title inside the file must use the edited title, not the original
// <title> argument, for both the saved entry and the duplicate-title/-f
// check.
func TestInsertEditChangedTitleUsedForSavedEntryAndDuplicateCheck(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	// An existing entry under the *edited* title, so the duplicate check
	// has something to actually collide with.
	if res, _ := runCLIWithValue(t, []string{"insert", "EditedTitle"}, "existing"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	setFakeEditor(t, "set-title-and-value", map[string]string{
		"GAGE_TEST_FAKE_EDITOR_TITLE": "EditedTitle",
		"GAGE_TEST_FAKE_EDITOR_VALUE": "v",
	})
	dup := runCLI(t, []string{"insert", "OriginalTitle", "-e"}, "")
	if dup.Code != int(exitcode.Conflict) {
		t.Errorf("exit code = %d, want %d (Conflict) — the duplicate check should run against the edited title", dup.Code, exitcode.Conflict)
	}

	// The original <title> argument must never have been used.
	origCat := runCLI(t, []string{"cat", "OriginalTitle"}, "")
	if origCat.Code != int(exitcode.NotFound) {
		t.Errorf("an entry was saved under the original title argument: cat exit code = %d, want %d (NotFound)", origCat.Code, exitcode.NotFound)
	}

	forced := runCLI(t, []string{"insert", "OriginalTitle", "-e", "--force"}, "")
	if forced.Code != 0 {
		t.Fatalf("forced insert -e with a duplicate edited title failed: %s", forced.Stderr)
	}
	ls := runCLI(t, []string{"ls"}, "")
	if n := strings.Count(ls.Stdout, "EditedTitle"); n != 2 {
		t.Errorf("ls shows %d \"EditedTitle\" entries after a forced insert -e, want 2:\n%s", n, ls.Stdout)
	}
}

// TestInsertEditRejectedAlongsideMultilineOrValueStdinBeforeAnyIO extends
// M4's mutual-exclusion test to three flags.
func TestInsertEditRejectedAlongsideMultilineOrValueStdinBeforeAnyIO(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	p := &explodingPrompter{t: t}
	in := &explodingReader{t: t}

	for _, args := range [][]string{
		{"insert", "Site", "-e", "-m"},
		{"insert", "Site", "-e", "--value-stdin"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			res, _ := runCLIWithPrompterAndStdin(t, args, in, false, p)
			if res.Code != int(exitcode.Usage) {
				t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
			}
			if !strings.Contains(res.Stderr, "mutually exclusive") {
				t.Errorf("stderr = %q, want it to explain the flags are mutually exclusive", res.Stderr)
			}
		})
	}
}

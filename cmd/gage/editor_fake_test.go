package main

import (
	"os"
	"testing"

	"github.com/denmark/gage/internal/gage"
)

// setFakeEditor points $EDITOR at this same test binary (os.Args[0]) and
// arms GAGE_TEST_FAKE_EDITOR so TestMain intercepts the re-invocation as
// a fake editor instead of running tests — see TestMain's doc comment.
// mode selects runFakeEditor's behavior; extra sets any further env vars
// that mode needs (e.g. the value to write).
//
// Using the test binary itself rather than a shell script is what keeps
// these tests portable to Windows, which main_integration_test.go's own
// binary-building tests already show this package cares about.
func setFakeEditor(t *testing.T, mode string, extra map[string]string) {
	t.Helper()
	t.Setenv("EDITOR", os.Args[0])
	t.Setenv("GAGE_TEST_FAKE_EDITOR", mode)
	for k, v := range extra {
		t.Setenv(k, v)
	}
}

// fakeEditorRecordEnvVar names the env var a fake-editor invocation
// writes the scratch file's path (and the permission bits it observed on
// it) to, so the parent test process can recover them after the whole
// gage command has finished and the scratch file is gone.
const fakeEditorRecordEnvVar = "GAGE_TEST_FAKE_EDITOR_RECORD"

// runFakeEditor is what TestMain hands control to when this binary is
// invoked as $EDITOR: it never touches the real testing package, just
// reads/writes the file named by its last argument (editYAML always
// appends the scratch path as the final arg) and returns an exit code,
// exactly like a real editor would.
func runFakeEditor(mode, path string) int {
	if record := os.Getenv(fakeEditorRecordEnvVar); record != "" {
		perm := "?"
		if info, err := os.Stat(path); err == nil {
			perm = info.Mode().Perm().String()
		}
		_ = os.WriteFile(record, []byte(path+"\n"+perm+"\n"), 0o600)
	}

	switch mode {
	case "noop":
		// Leave the file exactly as written — editYAML's "unchanged" case.
		return 0
	case "fail":
		// A non-zero $EDITOR exit, before touching the file at all.
		return 1
	case "invalid-yaml":
		if err := os.WriteFile(path, []byte("title: [this is not\nvalid: yaml"), 0o600); err != nil {
			return 1
		}
		return 0
	case "set-value":
		return fakeEditField(path, func(e *gage.Entry) { e.Value = os.Getenv("GAGE_TEST_FAKE_EDITOR_VALUE") })
	case "set-title":
		return fakeEditField(path, func(e *gage.Entry) { e.Title = os.Getenv("GAGE_TEST_FAKE_EDITOR_TITLE") })
	case "set-title-and-value":
		return fakeEditField(path, func(e *gage.Entry) {
			e.Title = os.Getenv("GAGE_TEST_FAKE_EDITOR_TITLE")
			e.Value = os.Getenv("GAGE_TEST_FAKE_EDITOR_VALUE")
		})
	case "set-value-and-field":
		return fakeEditField(path, func(e *gage.Entry) {
			e.Value = os.Getenv("GAGE_TEST_FAKE_EDITOR_VALUE")
			e.Fields = map[string]string{
				os.Getenv("GAGE_TEST_FAKE_EDITOR_FIELD_KEY"): os.Getenv("GAGE_TEST_FAKE_EDITOR_FIELD_VALUE"),
			}
		})
	case "set-description-only":
		return fakeEditField(path, func(e *gage.Entry) { e.Description = os.Getenv("GAGE_TEST_FAKE_EDITOR_DESCRIPTION") })
	default:
		return 1
	}
}

// fakeEditField reads the scratch file back into an Entry, applies fn,
// and writes it back — the fake-editor equivalent of a human changing
// exactly one field and saving.
func fakeEditField(path string, fn func(e *gage.Entry)) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 1
	}
	e, err := gage.UnmarshalEntry(data)
	if err != nil {
		return 1
	}
	fn(&e)
	out, err := gage.MarshalEntry(e)
	if err != nil {
		return 1
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return 1
	}
	return 0
}

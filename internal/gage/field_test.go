package gage

import (
	"strings"
	"testing"

	"github.com/denmark/gage/internal/gage/exitcode"
)

// TestEntryFieldExtractsNamedField: `show --field NAME` reads from
// `fields`, not from `value`.
func TestEntryFieldExtractsNamedField(t *testing.T) {
	e := Entry{
		Title: "Proton",
		Value: "the-password",
		Fields: map[string]string{
			"username":  "me@example.com",
			"totp_seed": "JBSWY3DPEHPK3PXP",
		},
	}

	for name, want := range e.Fields {
		got, err := e.Field(name)
		if err != nil {
			t.Fatalf("Field(%q): %v", name, err)
		}
		if got != want {
			t.Errorf("Field(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestEntryFieldEmptyValueIsAFieldNotAMiss: a field present but set to
// the empty string is found, and returns "". Falling back to a
// not-found error on an empty value would report a field the entry
// really has as one it doesn't.
func TestEntryFieldEmptyValueIsAFieldNotAMiss(t *testing.T) {
	e := Entry{Fields: map[string]string{"note": ""}}
	got, err := e.Field("note")
	if err != nil {
		t.Fatalf("Field on a present-but-empty field: %v", err)
	}
	if got != "" {
		t.Errorf("Field = %q, want the empty string", got)
	}
}

// TestEntryFieldUnknownNameErrorsClearly: an unknown --field name is a
// NotFound, and the message names both the field asked for and the ones
// the entry actually has — the whole point being that the human can't
// see inside the entry to check.
//
// The known names are listed sorted, so the message doesn't reorder
// itself between runs over the same entry (Go map iteration is
// randomized).
func TestEntryFieldUnknownNameErrorsClearly(t *testing.T) {
	e := Entry{
		Title:  "Proton",
		Value:  "the-password",
		Fields: map[string]string{"username": "me@example.com", "totp_seed": "x"},
	}

	_, err := e.Field("passwrod")
	if err == nil {
		t.Fatal("Field on an unknown name succeeded, want an error")
	}
	if got := exitcode.CodeOf(err); got != exitcode.NotFound {
		t.Errorf("exit code = %v, want %v", got, exitcode.NotFound)
	}
	msg := err.Error()
	for _, want := range []string{"passwrod", "totp_seed", "username"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
	if i, j := strings.Index(msg, "totp_seed"), strings.Index(msg, "username"); i > j {
		t.Errorf("known field names are not listed in sorted order: %q", msg)
	}
	// The one thing the error must never do is leak what it was asked
	// to protect.
	if strings.Contains(msg, "the-password") || strings.Contains(msg, "me@example.com") {
		t.Errorf("error message leaks a field value: %q", msg)
	}
}

// TestEntryFieldOnEntryWithNoFieldsSaysSo: an entry with no `fields` at
// all gets a message that says that, rather than one listing an empty
// set.
func TestEntryFieldOnEntryWithNoFieldsSaysSo(t *testing.T) {
	e := Entry{Title: "Plain", Value: "v"}
	_, err := e.Field("username")
	if err == nil {
		t.Fatal("Field on an entry with no fields succeeded, want an error")
	}
	if got := exitcode.CodeOf(err); got != exitcode.NotFound {
		t.Errorf("exit code = %v, want %v", got, exitcode.NotFound)
	}
	if !strings.Contains(err.Error(), "no fields") {
		t.Errorf("error %q should say the entry has no fields at all", err)
	}
}

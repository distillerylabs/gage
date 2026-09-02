package devicename

import "testing"

func TestNormalizeHostname(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Andrews-MacBook-Pro.local", "andrews-macbook-pro"},
		{"laptop-1", "laptop-1"},
		{"My Computer!!!", "my-computer"},
		{"UPPER.CASE.HOST", "upper"},
	}
	for _, tc := range cases {
		got, ok := Normalize(tc.in)
		if !ok {
			t.Errorf("Normalize(%q) reported not-ok, want ok", tc.in)
			continue
		}
		if got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeCollapsesRuns(t *testing.T) {
	got, ok := Normalize("a!!!!!b")
	if !ok {
		t.Fatal("Normalize reported not-ok")
	}
	if got != "a-b" {
		t.Errorf("Normalize(%q) = %q, want %q", "a!!!!!b", got, "a-b")
	}
}

func TestNormalizeNothingUsable(t *testing.T) {
	cases := []string{"", "...", "!!!", "---"}
	for _, in := range cases {
		if got, ok := Normalize(in); ok {
			t.Errorf("Normalize(%q) = %q, ok=true, want ok=false (nothing usable)", in, got)
		}
	}
}

func TestNormalizeCapsLength(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "a"
	}
	got, ok := Normalize(long)
	if !ok {
		t.Fatal("Normalize reported not-ok")
	}
	if len(got) > MaxLength {
		t.Errorf("Normalize length = %d, want <= %d", len(got), MaxLength)
	}
}

func TestValidAccepts(t *testing.T) {
	for _, name := range []string{"laptop-1", "andrews-macbook-pro", "recovery.key_1", "a"} {
		if !Valid(name) {
			t.Errorf("Valid(%q) = false, want true", name)
		}
	}
}

func TestValidRejectsPathTraversalAndSeparators(t *testing.T) {
	cases := []string{
		"../../../etc/x",
		"/etc/passwd",
		`C:\Windows\x`,
		"a/b",
		`a\b`,
		"",
		"UpperCase",
		"has space",
	}
	for _, name := range cases {
		if Valid(name) {
			t.Errorf("Valid(%q) = true, want false", name)
		}
	}
}

func TestValidRejectsOverLength(t *testing.T) {
	long := ""
	for i := 0; i < MaxLength+1; i++ {
		long += "a"
	}
	if Valid(long) {
		t.Errorf("Valid(%d-char name) = true, want false", len(long))
	}
}

package main

import (
	"bytes"
	"strings"
	"testing"

	"rsc.io/qr"
)

// decodeRenderedQR reverses renderQR: it turns the half-block terminal
// art back into the module grid that produced it, quiet zone stripped.
//
// This is how "the QR encodes exactly this value" is asserted without
// pulling a full QR *decoder* into the module graph for one test. The
// grid it returns is compared against an independently encoded
// reference, which catches the failure that actually matters here —
// encoding the wrong thing, such as the whole entry when --field asked
// for one key — while leaving "does rsc.io/qr encode correctly" as the
// library's own responsibility rather than something gage re-derives.
func decodeRenderedQR(t *testing.T, art string) [][]bool {
	t.Helper()

	lines := strings.Split(strings.TrimRight(art, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatal("no QR output to decode")
	}

	// Each rendered line carries two module rows.
	var rows [][]bool
	for _, line := range lines {
		runes := []rune(line)
		top := make([]bool, len(runes))
		bottom := make([]bool, len(runes))
		for i, r := range runes {
			switch r {
			case qrBoth:
				top[i], bottom[i] = true, true
			case qrTop:
				top[i] = true
			case qrBottom:
				bottom[i] = true
			case qrNeither:
			default:
				t.Fatalf("unexpected glyph %q in QR output", r)
			}
		}
		rows = append(rows, top, bottom)
	}

	// Strip the quiet zone: qrQuietZone light modules on every side. The
	// bottom may carry one extra padding row when the module count is
	// odd (see renderQR), so trim by finding the symbol rather than by
	// assuming a fixed offset.
	width := len(rows[0])
	size := width - 2*qrQuietZone
	if size <= 0 {
		t.Fatalf("QR render is %d columns wide, too narrow to contain a symbol", width)
	}
	out := make([][]bool, size)
	for y := 0; y < size; y++ {
		out[y] = make([]bool, size)
		for x := 0; x < size; x++ {
			// true means a dark module, the inverse of the render.
			out[y][x] = !rows[y+qrQuietZone][x+qrQuietZone]
		}
	}
	return out
}

// referenceGrid encodes value independently and returns its dark-module
// grid.
func referenceGrid(t *testing.T, value string) [][]bool {
	t.Helper()
	code, err := qr.Encode(value, qrLevel)
	if err != nil {
		t.Fatalf("encoding reference QR: %v", err)
	}
	grid := make([][]bool, code.Size)
	for y := range grid {
		grid[y] = make([]bool, code.Size)
		for x := range grid[y] {
			grid[y][x] = code.Black(x, y)
		}
	}
	return grid
}

func gridsEqual(a, b [][]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for y := range a {
		if len(a[y]) != len(b[y]) {
			return false
		}
		for x := range a[y] {
			if a[y][x] != b[y][x] {
				return false
			}
		}
	}
	return true
}

// TestRenderQRRoundTripsToTheExactValue: what comes out of the renderer
// is the symbol for exactly the string that went in — no truncation, no
// trailing newline swept in, no padding that shifts the grid.
func TestRenderQRRoundTripsToTheExactValue(t *testing.T) {
	for _, value := range []string{
		"hunter2",
		"JBSWY3DPEHPK3PXP",
		"correct horse battery staple",
		"https://example.com/totp?secret=ABC123&issuer=gage",
	} {
		var buf bytes.Buffer
		if err := renderQR(&buf, value); err != nil {
			t.Fatalf("renderQR(%q): %v", value, err)
		}
		if !gridsEqual(decodeRenderedQR(t, buf.String()), referenceGrid(t, value)) {
			t.Errorf("rendered QR for %q does not match the symbol for that value", value)
		}
	}
}

// TestRenderQRDistinguishesValues is what gives the comparison above its
// teeth: if the round trip matched everything, it would prove nothing.
func TestRenderQRDistinguishesValues(t *testing.T) {
	var buf bytes.Buffer
	if err := renderQR(&buf, "hunter2"); err != nil {
		t.Fatalf("renderQR: %v", err)
	}
	if gridsEqual(decodeRenderedQR(t, buf.String()), referenceGrid(t, "hunter3")) {
		t.Error("the QR for \"hunter2\" matched the symbol for \"hunter3\"")
	}
}

// TestRenderQRHasAQuietZone: without the four-module light border a
// scanner has no way to find the symbol's edge against whatever else is
// on the terminal.
func TestRenderQRHasAQuietZone(t *testing.T) {
	var buf bytes.Buffer
	if err := renderQR(&buf, "hunter2"); err != nil {
		t.Fatalf("renderQR: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")

	// The top qrQuietZone/2 rendered lines are entirely quiet zone, and
	// so are the first and last qrQuietZone columns of every line.
	for i := 0; i < qrQuietZone/2; i++ {
		if strings.Trim(lines[i], string(qrBoth)) != "" {
			t.Errorf("rendered line %d is not solid quiet zone: %q", i, lines[i])
		}
	}
	for i, line := range lines {
		runes := []rune(line)
		for x := 0; x < qrQuietZone; x++ {
			if runes[x] != qrBoth || runes[len(runes)-1-x] != qrBoth {
				t.Fatalf("line %d lacks a quiet-zone margin: %q", i, line)
			}
		}
	}
}

// TestRenderQRIsSquareIshAndRectangular: every line is the same width,
// which is what a scanner needs and what a stray newline or an
// off-by-one in the half-block loop would break.
func TestRenderQRIsSquareIshAndRectangular(t *testing.T) {
	var buf bytes.Buffer
	if err := renderQR(&buf, "hunter2"); err != nil {
		t.Fatalf("renderQR: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	width := len([]rune(lines[0]))
	for i, line := range lines {
		if got := len([]rune(line)); got != width {
			t.Fatalf("line %d is %d runes wide, want %d", i, got, width)
		}
	}
	// Two module rows per line, so the render is about half as tall as
	// it is wide in characters — that is the point of the half blocks.
	if got := len(lines); got != (width+1)/2 {
		t.Errorf("render is %d lines for %d columns, want %d", got, width, (width+1)/2)
	}
}

// TestRenderQRRejectsAnEmptyValue: there is nothing to scan, and an
// empty symbol would be a silently useless answer.
func TestRenderQRRejectsAnEmptyValue(t *testing.T) {
	var buf bytes.Buffer
	if err := renderQR(&buf, ""); err == nil {
		t.Error("renderQR accepted an empty value")
	}
	if buf.Len() != 0 {
		t.Errorf("renderQR wrote %q for an empty value", buf.String())
	}
}

// TestShowQRPrintsNoPlaintextAlongsideTheCode: -q renders *instead of*
// printing the value (see "Notes on show"). A QR code that came with the
// secret written underneath it would defeat its own purpose.
func TestShowQRPrintsNoPlaintextAlongsideTheCode(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	if res, _ := runCLIWithValue(t, []string{"insert", "GitHub"}, "hunter2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"show", "GitHub", "-q"}, "")
	if res.Code != 0 {
		t.Fatalf("show -q failed: %s", res.Stderr)
	}
	if strings.Contains(res.Stdout, "hunter2") {
		t.Errorf("show -q printed the plaintext alongside the code:\n%s", res.Stdout)
	}
	if strings.Contains(res.Stderr, "hunter2") {
		t.Errorf("show -q leaked the plaintext to stderr: %q", res.Stderr)
	}
	if !gridsEqual(decodeRenderedQR(t, res.Stdout), referenceGrid(t, "hunter2")) {
		t.Error("show -q did not render the symbol for the entry's value")
	}
}

// TestShowFieldQREncodesOnlyThatField is the design doc's explicit
// requirement: a structured note (an SSH key plus metadata) must not get
// dumped whole into a QR code when the human only wanted the TOTP seed.
func TestShowFieldQREncodesOnlyThatField(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntryWithFields(t, "Proton", "the-password", map[string]string{
		"username":  "me@example.com",
		"totp_seed": "JBSWY3DPEHPK3PXP",
	})

	res := runCLI(t, []string{"show", "Proton", "--field", "totp_seed", "-q"}, "")
	if res.Code != 0 {
		t.Fatalf("show --field --qr failed: %s", res.Stderr)
	}
	if !gridsEqual(decodeRenderedQR(t, res.Stdout), referenceGrid(t, "JBSWY3DPEHPK3PXP")) {
		t.Error("--field NAME --qr did not encode exactly that field")
	}
	for _, leak := range []string{"the-password", "me@example.com", "JBSWY3DPEHPK3PXP"} {
		if strings.Contains(res.Stdout, leak) {
			t.Errorf("QR output contains plaintext %q", leak)
		}
	}
}

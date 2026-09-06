package main

import (
	"fmt"
	"io"
	"strings"

	"rsc.io/qr"

	"github.com/denmark/gage/internal/gage/exitcode"
)

// qrLevel is the error-correction level every gage QR is encoded at.
//
// Medium (~15% recoverable) rather than Low: the thing being scanned is
// a terminal window, where a partly-obscured or reflowed render is a
// routine occurrence rather than an exotic one, and the extra modules
// cost nothing but screen area. Fixed rather than configurable — a
// scanner reads the level out of the symbol, so there is nobody for the
// choice to matter to.
const qrLevel = qr.M

// qrQuietZone is the mandatory light border around a QR symbol, in
// modules. Four is the spec's minimum, and without it a scanner has no
// way to find the symbol's edge against whatever the terminal is
// showing behind it.
const qrQuietZone = 4

// Half-block glyphs. Each rendered character carries two vertically
// stacked modules, which is what keeps a QR code roughly square in a
// terminal cell grid that is about twice as tall as it is wide — a
// one-module-per-line render comes out stretched and is materially
// harder for a phone to lock onto.
const (
	qrBoth    = '█' // full block: both modules light
	qrTop     = '▀' // upper half: top module light
	qrBottom  = '▄' // lower half: bottom module light
	qrNeither = ' ' // both modules dark
)

// renderQR writes value as a terminal QR code.
//
// Light modules are drawn as block glyphs and dark modules as spaces —
// the inverse of ink on paper, and deliberately so: a terminal draws
// glyphs in the foreground colour over a darker background, so this is
// what puts light where the symbol needs light. It matches the render
// in the design doc's own session transcript, whose quiet zone is a
// solid band of blocks.
//
// The library's part of this is encoding, and the frontend's is drawing:
// see "Terminal ASCII QR rendering" in the design doc, which names this
// split explicitly and notes that a GUI would render the same bits as an
// image widget instead.
func renderQR(w io.Writer, value string) error {
	if value == "" {
		return exitcode.New(exitcode.Usage, "gage: nothing to encode as a QR code: the value is empty")
	}
	code, err := qr.Encode(value, qrLevel)
	if err != nil {
		// The realistic cause is a value too large for any QR version —
		// a multi-kilobyte note or an SSH key, where the right answer is
		// a different output mode, not a bigger symbol.
		return exitcode.Wrap(exitcode.Usage,
			fmt.Errorf("gage: this value cannot be encoded as a QR code (too long?): %w", err))
	}

	// Modules, including the quiet zone on all four sides. light(x, y)
	// is the single place the quiet-zone offset is applied, so the
	// border and the symbol can't disagree about where the symbol
	// starts.
	span := code.Size + 2*qrQuietZone
	light := func(x, y int) bool {
		cx, cy := x-qrQuietZone, y-qrQuietZone
		if cx < 0 || cy < 0 || cx >= code.Size || cy >= code.Size {
			return true // quiet zone
		}
		return !code.Black(cx, cy)
	}

	var b strings.Builder
	for y := 0; y < span; y += 2 {
		for x := 0; x < span; x++ {
			top := light(x, y)
			// An odd number of rows leaves the last line with no bottom
			// module. It reads as quiet zone (light), which extends the
			// border by one module rather than truncating the symbol.
			bottom := y+1 >= span || light(x, y+1)
			switch {
			case top && bottom:
				b.WriteRune(qrBoth)
			case top:
				b.WriteRune(qrTop)
			case bottom:
				b.WriteRune(qrBottom)
			default:
				b.WriteRune(qrNeither)
			}
		}
		b.WriteByte('\n')
	}

	if _, err := io.WriteString(w, b.String()); err != nil {
		return exitcode.Wrap(exitcode.Internal, err)
	}
	return nil
}

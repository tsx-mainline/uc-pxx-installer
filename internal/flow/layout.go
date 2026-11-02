package flow

import (
	"fmt"
	"math"
	"strings"
	"unicode"

	"uc-pxx-installer/internal/status"
)

// The console of the panel: 120 x 37 cells (ter-132b, 1920 x 1200 with
// rotate:3). p10inst flow uses the size of its terminal; this is the size
// the tests check every screen against.
const (
	PanelCols = 120
	PanelRows = 37
)

// Rows is a frame laid out on a screen of w x h cells: every row fits the
// width (long rows are wrapped at spaces), and the body has exactly the
// rows between the title and the message. Every part has a fixed place
// (owner, 2026-10-31: items must not move when rows appear): the message
// always has MsgRows rows, and the key legend is the last row, right above
// the touch keys.
//
//	title / rule / body (Room rows) / rule (Rule) / message (MsgRows) / footer
type Rows struct {
	Title  string
	Body   []status.Line // Room rows at most (drawFlow pads them)
	Room   int
	Rule   string   // the rule above the message ("" = a plain rule); it says where a scrolled view is
	Footer string   // the key legend
	Msg    []string // MsgRows rows
	// Hidden: body rows that are not on the screen (the log scrolls;
	// on every other screen this is a fault that the tests catch).
	Hidden int
	// Cut: title, footer or message rows that did not fit (a fault).
	Cut int
}

// MsgRows is the fixed number of message rows.
const MsgRows = 2

// KeyPlaces is the number of touch keys under the screen. Their order from
// left to right (uc-pxx-linux docs/hardware.md, measured with evtest):
// Teams, Headset, Speaker, Vol-, Vol+, Mic.
const KeyPlaces = 6

// The places of the keys that the flow uses (0 = the leftmost).
const (
	PlaceSpeaker = 2 // back
	PlaceVolDown = 3 // next
	PlaceVolUp   = 4 // previous
	PlaceMic     = 5 // OK
)

// Problem says what does not fit, or "".
func (r Rows) Problem(screen Screen) string {
	var p []string
	if r.Hidden > 0 && screen != ScreenLog {
		p = append(p, fmt.Sprintf("%d body rows do not fit", r.Hidden))
	}
	if r.Cut > 0 {
		p = append(p, fmt.Sprintf("%d title, footer or message rows are cut", r.Cut))
	}
	return strings.Join(p, "; ")
}

// Layout lays the frame out on w x h cells. Widths are console cells
// (Cells): an East Asian wide character takes two.
func (f Frame) Layout(w, h int) Rows {
	r := Rows{}
	r.Title = r.fit(f.Title, w)
	r.Msg = Wrap(f.Msg, w)
	if len(r.Msg) > MsgRows {
		// cut, with a sign on the screen
		r.Cut += len(r.Msg) - MsgRows
		r.Msg = r.Msg[:MsgRows]
		r.Msg[MsgRows-1] = clipCells(r.Msg[MsgRows-1], w-4) + " ..."
	}
	for len(r.Msg) < MsgRows {
		r.Msg = append(r.Msg, "")
	}
	r.Room = h - 4 - len(r.Msg)
	if r.Room < 1 {
		r.Room = 1
	}
	var body []status.Line
	for _, l := range f.Body {
		for _, t := range Wrap(l.Text, w) {
			body = append(body, status.Line{Level: l.Level, Text: t})
		}
		if l.Text == "" {
			body = append(body, l)
		}
	}
	if over := len(body) - r.Room; over > 0 && f.Clip > 0 && f.Clip <= len(f.Body) {
		// the row Clip (a long reason) gives up the rows that do not fit,
		// with a sign: only as many as needed (review L-3, owner r3)
		body = clipRow(f.Body, f.Clip-1, over, w)
	}
	if len(body) > r.Room && f.Squeeze {
		// drop empty rows (only those) from the bottom up until the body fits
		for i := len(body) - 1; i >= 0 && len(body) > r.Room; i-- {
			if body[i].Text == "" {
				body = append(body[:i], body[i+1:]...)
			}
		}
	}
	footer := f.Footer
	if over := len(body) - r.Room; over > 0 {
		r.Hidden = over
		if f.ScrollKeys == "" {
			// a screen that does not scroll: the last row says that rows
			// are missing (a fault: Problem, and the flow log)
			body = append(body[:r.Room-1], status.Line{Level: status.Bad,
				Text: clipCells(fmt.Sprintf("!! %d more rows do not fit on this screen.", over+1), w)})
		} else {
			start := f.Scroll
			if f.FromEnd {
				start = over - f.Scroll
			}
			if start < 0 {
				start = 0
			}
			if start > over {
				start = over
			}
			body = body[start : start+r.Room]
			r.Rule = fmt.Sprintf(" rows %d-%d of %d ", start+1, start+r.Room, r.Room+over)
		}
	} else if f.Tail != nil {
		// the newest lines of the child's output that fit (each whole),
		// after an empty row
		var tail []status.Line
		free := r.Room - len(body) - 1
		for i := len(f.Tail) - 1; i >= 0; i-- {
			rows := Wrap(f.Tail[i].Text, w)
			if len(rows) == 0 {
				rows = []string{""}
			}
			if len(rows) > free-len(tail) {
				break
			}
			var add []status.Line
			for _, t := range rows {
				add = append(add, status.Line{Level: f.Tail[i].Level, Text: t})
			}
			tail = append(add, tail...)
		}
		if len(tail) > 0 {
			body = append(append(body, status.Line{}), tail...)
		}
	}
	r.Body = body
	if f.Keys != ([KeyPlaces]string{}) && !f.TextKeys {
		r.Footer = f.panel().keyColumns(f.Keys, w)
	} else {
		r.Footer = r.fit(footer, w)
	}
	return r
}

// The touch keys under the screen, in pixels of the 1920 px wide panel
// (FINAL: measured by the owner on unit 2 with a test page and confirmed
// on the unit, 2026-11-01). A dead margin of KeyMarginPx at each outer
// edge; the Crestron logo exactly in the middle, LogoFromPx..LogoToPx,
// with no key; six equal keys, three on each side: KeyWidthPx =
// (LogoFromPx - KeyMarginPx) / 3 = 223 1/3 px. From the left: 1 Teams,
// 2 Headset, 3 Speaker (left of the logo), 4 Vol-, 5 Vol+, 6 Mic (right of
// it); centres 162, 385, 609, 1312, 1535, 1759 px. This is the only place
// of the geometry: the edges are computed from these constants.
const (
	PanelPx     = 1920
	KeyMarginPx = 50
	LogoFromPx  = 720
	LogoToPx    = PanelPx - LogoFromPx // 1200: the logo is centred
)

// KeyWidthPx is the width of one key in px.
const KeyWidthPx = float64(LogoFromPx-KeyMarginPx) / 3

// KeyEdgesPx is the left and right edge of key i (0 = Teams .. 5 = Mic).
func KeyEdgesPx(i int) (float64, float64) {
	left := float64(KeyMarginPx) + float64(i)*KeyWidthPx
	if i >= 3 {
		left = float64(LogoToPx) + float64(i-3)*KeyWidthPx
	}
	return left, left + KeyWidthPx
}

// Panel is the geometry of a model's screen and key row (W-P6,
// UC-P8.md §5): the width in px of the console (fbcon rotate:3), the dead
// margin at each outer edge, the Crestron logo with no key, and the console
// size with the image's font. Six equal keys, three on each side of the
// logo: (LogoFromPx - MarginPx) / 3 each.
type Panel struct {
	Model      string  // "UC-P10" or "UC-P8"
	Px         float64 // the width in px
	MarginPx   float64
	LogoFromPx float64
	LogoToPx   float64
	Cols, Rows int // the console with the font of the RAM image
}

// PanelUCP10 is the UC-P10 (1920 x 1200, ter-132b: 120 x 37), measured by
// the owner (the constants above).
var PanelUCP10 = Panel{Model: "UC-P10", Px: PanelPx, MarginPx: KeyMarginPx, LogoFromPx: LogoFromPx, LogoToPx: LogoToPx,
	Cols: PanelCols, Rows: PanelRows}

// PanelUCP8 is the UC-P8 (1280 x 800, ter-128b: 91 x 28, the font of the
// image's rcS; the owner, 2026-11-02: about the P10's letter size): the P10's key row as proportions of the width, scaled by
// 1280/1920 = 2/3 (UC-P8.md §5.3: an ESTIMATE, not measured; the third
// party checks it with a photo, the owner confirms it). Margin 33 1/3 px,
// the logo 480..800 px, keys 148 8/9 px.
var PanelUCP8 = Panel{Model: "UC-P8", Px: 1280, MarginPx: 1280.0 * KeyMarginPx / PanelPx,
	LogoFromPx: 1280.0 * LogoFromPx / PanelPx, LogoToPx: 1280 - 1280.0*LogoFromPx/PanelPx, Cols: 91, Rows: 28}

// PanelFor is the panel of the RAM image's device tree: its compatible
// list (NUL-separated, /proc/device-tree/compatible). Anything that is not
// a UC-P8 is the UC-P10 (the device mode refuses a unit that is neither).
func PanelFor(compatible string) Panel {
	for _, c := range strings.Split(compatible, "\x00") {
		if strings.TrimSpace(c) == "crestron,uc-p8" {
			return PanelUCP8
		}
	}
	return PanelUCP10
}

// KeyWidthPx is the width of one key of the panel in px.
func (p Panel) KeyWidthPx() float64 { return (p.LogoFromPx - p.MarginPx) / 3 }

// KeyEdgesPx is the left and right edge of key i (0 = Teams .. 5 = Mic).
func (p Panel) KeyEdgesPx(i int) (float64, float64) {
	left := p.MarginPx + float64(i)*p.KeyWidthPx()
	if i >= 3 {
		left = p.LogoToPx + float64(i-3)*p.KeyWidthPx()
	}
	return left, left + p.KeyWidthPx()
}

// KeyCells is the cells wholly on key i on a screen w cells wide.
func (p Panel) KeyCells(i, w int) (from, to int) {
	cell := p.Px / float64(w) // px per cell
	lpx, rpx := p.KeyEdgesPx(i)
	return int(math.Ceil(lpx / cell)), int(math.Floor(rpx / cell))
}

// shortLabels are the shorter forms of the labels, for a key that is
// narrower than the label (the UC-P8: about 12 cells at 106 columns, 10 at
// 91): the first that fits is used.
var shortLabels = map[string][]string{
	"Hold: restart": {"Hold:restart", "Restart"},
	"Hold: repair":  {"Hold:repair", "Repair"},
	"Hold: switch":  {"Hold:switch", "Switch"},
	"Hold: start":   {"Hold:start", "Start"},
	"Install again": {"Reinstall"},
	"Install here":  {"Install"},
	"Check again":   {"Recheck"},
	"Scroll down":   {"Down"},
	"Scroll up":     {"Up"},
	"Previous":      {"Prev"},
}

// LabelFor is the label as it fits n cells: the label, else its first
// short form that fits, else the label cut.
func LabelFor(l string, n int) string {
	if Cells(l) <= n {
		return l
	}
	for _, s := range shortLabels[l] {
		if Cells(s) <= n {
			return s
		}
	}
	return clipCells(l, n)
}

// keyColumns is the key legend: each label centred on its key (scaled from
// the panel's pixels to the w cells of the screen), never wider than the
// key (a short form where needed), and nothing over the logo.
func (p Panel) keyColumns(k [KeyPlaces]string, w int) string {
	row := []rune(strings.Repeat(" ", w))
	cell := p.Px / float64(w) // px per cell
	for i, l := range k {
		if l == "" {
			continue
		}
		lpx, rpx := p.KeyEdgesPx(i)
		from, to := p.KeyCells(i, w)
		l = LabelFor(l, to-from)
		start := int(math.Round((lpx+rpx)/2/cell - float64(Cells(l))/2))
		if start < from {
			start = from
		}
		if start+Cells(l) > to {
			start = to - Cells(l)
		}
		for j, r := range []rune(l) {
			row[start+j] = r
		}
	}
	return strings.TrimRight(string(row), " ")
}

// keyColumns is the UC-P10's key legend.
func keyColumns(k [KeyPlaces]string, w int) string { return PanelUCP10.keyColumns(k, w) }

// clipRow lays the body out with row i shortened by n wrapped rows (at
// least one row stays), its last row ending in the sign.
func clipRow(lines []status.Line, i, n, w int) []status.Line {
	var body []status.Line
	for j, l := range lines {
		rows := Wrap(l.Text, w)
		if j == i {
			keep := len(rows) - n - 1 // one row for the sign
			if keep < 1 {
				keep = 1
			}
			if keep < len(rows) {
				rows = append(rows[:keep:keep], "    ... (cut: the whole text is in the log)")
			}
		}
		for _, t := range rows {
			body = append(body, status.Line{Level: l.Level, Text: t})
		}
		if l.Text == "" {
			body = append(body, l)
		}
	}
	return body
}

func (r *Rows) fit(s string, w int) string {
	if Cells(s) <= w {
		return s
	}
	r.Cut++
	return clipCells(s, w)
}

// Cells is the width of s on the console, in cells: 2 for an East Asian
// wide or full-width character, 0 for a combining mark or a control
// character, 1 for any other.
func Cells(s string) int {
	n := 0
	for _, r := range s {
		n += runeCells(r)
	}
	return n
}

func runeCells(r rune) int {
	switch {
	case r < 0x20 || r == 0x7f || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || r == 0x200b:
		return 0
	case r >= 0x1100 && r <= 0x115f, r >= 0x2e80 && r <= 0x303e, r >= 0x3041 && r <= 0x33ff, r >= 0x3400 && r <= 0x4dbf,
		r >= 0x4e00 && r <= 0x9fff, r >= 0xa000 && r <= 0xa4cf, r >= 0xac00 && r <= 0xd7a3, r >= 0xf900 && r <= 0xfaff,
		r >= 0xfe30 && r <= 0xfe4f, r >= 0xff00 && r <= 0xff60, r >= 0xffe0 && r <= 0xffe6, r >= 0x1f300 && r <= 0x1f64f,
		r >= 0x1f900 && r <= 0x1f9ff, r >= 0x20000 && r <= 0x3fffd:
		return 2
	}
	return 1
}

// clipCells cuts s to at most w cells.
func clipCells(s string, w int) string {
	n := 0
	for i, r := range s {
		if n+runeCells(r) > w {
			return s[:i]
		}
		n += runeCells(r)
	}
	return s
}

// Wrap breaks a row into rows of at most w cells, at spaces. A continued
// row starts with the indent of the first row and 4 more spaces ("  | "
// rows of the child's output keep their bar). A word longer than a row is
// broken. "" gives no rows.
func Wrap(s string, w int) []string {
	if s == "" {
		return nil
	}
	if Cells(s) <= w {
		return []string{s}
	}
	rs := []rune(s)
	n := 0
	for n < len(rs) && rs[n] == ' ' {
		n++
	}
	lead := string(rs[:n])
	if strings.HasPrefix(string(rs[n:]), "| ") {
		lead += "| "
	}
	cont := lead + "    "
	if len([]rune(cont)) > w/2 {
		cont = "    "
	}
	var out []string
	cur, curW := []rune{}, 0
	min := n + 1 // the first row is not cut inside its indent
	for len(rs) > 0 {
		room := w - curW
		// fit: the runes of rs that fit into the room
		fit, fw := 0, 0
		for fit < len(rs) && fw+runeCells(rs[fit]) <= room {
			fw += runeCells(rs[fit])
			fit++
		}
		if fit == len(rs) {
			cur = append(cur, rs...)
			break
		}
		// the last space inside the room (rs[fit] itself may be one)
		cut := -1
		for i := fit; i >= min; i-- {
			if rs[i] == ' ' {
				cut = i
				break
			}
		}
		min = 1
		if cut <= 0 {
			cut = fit // a long word: break it
			if cut == 0 {
				cut = 1
			}
			cur = append(cur, rs[:cut]...)
			rs = rs[cut:]
		} else {
			cur = append(cur, rs[:cut]...)
			rs = rs[cut+1:]
		}
		out = append(out, strings.TrimRight(string(cur), " "))
		cur = []rune(cont)
		curW = Cells(cont)
		for len(rs) > 0 && rs[0] == ' ' {
			rs = rs[1:]
		}
	}
	if s := strings.TrimRight(string(cur), " "); s != "" && s != strings.TrimRight(cont, " ") {
		out = append(out, string(cur))
	}
	return out
}

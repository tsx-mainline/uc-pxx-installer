package flow

import (
	"fmt"
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
// rows between the title and the footer.
//
//	title / rule / body (Room rows) / rule / footer / message (1..3 rows)
type Rows struct {
	Title  string
	Body   []status.Line // Room rows at most (drawFlow pads them)
	Room   int
	Footer string
	Msg    []string
	// Hidden: body rows that are not on the screen (the log scrolls;
	// on every other screen this is a fault that the tests catch).
	Hidden int
	// Cut: title, footer or message rows that did not fit (a fault).
	Cut int
}

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
	if len(r.Msg) > 3 {
		// cut, with a sign on the screen
		r.Cut += len(r.Msg) - 3
		r.Msg = r.Msg[:3]
		r.Msg[2] = clipCells(r.Msg[2], w-4) + " ..."
	}
	if len(r.Msg) == 0 {
		r.Msg = []string{""}
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
			footer += fmt.Sprintf("   %s (rows %d-%d of %d)", f.ScrollKeys, start+1, start+r.Room, r.Room+over)
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
	r.Footer = r.fit(footer, w)
	return r
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

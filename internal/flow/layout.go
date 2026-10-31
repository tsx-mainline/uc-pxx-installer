package flow

import (
	"fmt"
	"strings"
	"unicode/utf8"

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

// Layout lays the frame out on w x h cells.
func (f Frame) Layout(w, h int) Rows {
	r := Rows{}
	r.Title = r.fit(f.Title, w)
	r.Msg = Wrap(f.Msg, w)
	if len(r.Msg) > 3 {
		r.Cut += len(r.Msg) - 3
		r.Msg = r.Msg[:3]
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
		// drop empty rows from the bottom up until the body fits
		for i := len(body) - 1; i >= 0 && len(body) > r.Room; i-- {
			if body[i].Text == "" {
				body = append(body[:i], body[i+1:]...)
			}
		}
	}
	footer := f.Footer
	if over := len(body) - r.Room; over > 0 {
		r.Hidden = over
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
		if f.ScrollKeys != "" {
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
	if utf8.RuneCountInString(s) <= w {
		return s
	}
	r.Cut++
	return string([]rune(s)[:w])
}

// Wrap breaks a row into rows of at most w characters, at spaces. A
// continued row starts with the indent of the first row and 4 more spaces
// ("  | " rows of the child's output keep their bar). A word longer than a
// row is broken. "" gives no rows.
func Wrap(s string, w int) []string {
	if s == "" {
		return nil
	}
	if utf8.RuneCountInString(s) <= w {
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
	cur := []rune{}
	min := n + 1 // the first row is not cut inside its indent
	for len(rs) > 0 {
		room := w - len(cur)
		if len(rs) <= room {
			cur = append(cur, rs...)
			break
		}
		// the last space inside the room
		cut := -1
		for i := room; i >= min; i-- {
			if rs[i] == ' ' {
				cut = i
				break
			}
		}
		min = 1
		if cut <= 0 {
			cut = room // a long word: break it
			cur = append(cur, rs[:cut]...)
			rs = rs[cut:]
		} else {
			cur = append(cur, rs[:cut]...)
			rs = rs[cut+1:]
		}
		out = append(out, strings.TrimRight(string(cur), " "))
		cur = []rune(cont)
		for len(rs) > 0 && rs[0] == ' ' {
			rs = rs[1:]
		}
	}
	if s := strings.TrimRight(string(cur), " "); s != "" && s != strings.TrimRight(cont, " ") {
		out = append(out, string(cur))
	}
	return out
}

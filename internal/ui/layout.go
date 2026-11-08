package ui

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"uc-pxx-installer/internal/status"
)

// The layout of the status UI is the layout of the install flow (package
// flow, layout.go; owner feedback FLOW-FEEDBACK-R3 item 5): the title, a
// rule, the body, a rule, MsgRows message rows, and the key legend as the
// last row, right above the touch keys, with each label above its key. The
// selection of the menu is in the page (its first row), not in the footer.
//
// The status build has no package flow (it is install-only:
// ci/check-no-writer.py), so the geometry of the key row is here once more.
// cmd/p10inst/keylegend_test.go (install build) checks that both give the
// same legend for the same labels.

// MsgRows is the fixed number of message rows.
const MsgRows = 2

// KeyPlaces is the number of touch keys under the screen. Their order from
// the left (uc-pxx-linux docs/hardware.md): Teams, Headset, Speaker, Vol-,
// Vol+, Mic.
const KeyPlaces = 6

// The places of the keys that the status UI uses (0 = the leftmost).
const (
	PlaceSpeaker = 2 // back (on the status page: more rows)
	PlaceVolDown = 3 // previous
	PlaceVolUp   = 4 // next
	PlaceMic     = 5 // OK
)

// Panel is the geometry of a model's key row, in px of the console width
// (fbcon rotate:3): a dead margin at each outer edge, the Crestron logo with
// no key in the middle, three equal keys on each side. The numbers are the
// ones of flow.PanelUCP10 and flow.PanelUCP8 (measured by the owner on the
// UC-P10, 2026-11-01; the UC-P8 row is the UC-P10 row scaled by 2/3, an
// estimate).
type Panel struct {
	Model                              string
	Px, MarginPx, LogoFromPx, LogoToPx float64
}

// PanelUCP10 is the UC-P10: 1920 px, margin 50 px, the logo 720..1200 px.
var PanelUCP10 = Panel{Model: "UC-P10", Px: 1920, MarginPx: 50, LogoFromPx: 720, LogoToPx: 1200}

// PanelUCP8 is the UC-P8: the UC-P10 row x 1280/1920.
var PanelUCP8 = Panel{Model: "UC-P8", Px: 1280, MarginPx: 1280.0 * 50 / 1920,
	LogoFromPx: 1280.0 * 720 / 1920, LogoToPx: 1280 - 1280.0*720/1920}

// PanelFor is the panel of the RAM image's device tree (its compatible
// list, NUL-separated): the UC-P8 for "crestron,uc-p8", else the UC-P10 (the
// rule of flow.PanelFor).
func PanelFor(compatible string) Panel {
	for _, c := range strings.Split(compatible, "\x00") {
		if strings.TrimSpace(c) == "crestron,uc-p8" {
			return PanelUCP8
		}
	}
	return PanelUCP10
}

func (p Panel) orDefault() Panel {
	if p.Px <= 0 {
		return PanelUCP10
	}
	return p
}

func (p Panel) keyWidthPx() float64 { return (p.LogoFromPx - p.MarginPx) / 3 }

// keyEdgesPx is the left and right edge of key i (0 = Teams .. 5 = Mic).
func (p Panel) keyEdgesPx(i int) (float64, float64) {
	left := p.MarginPx + float64(i)*p.keyWidthPx()
	if i >= 3 {
		left = p.LogoToPx + float64(i-3)*p.keyWidthPx()
	}
	return left, left + p.keyWidthPx()
}

// keyCells is the cells wholly on key i on a screen w cells wide.
func (p Panel) keyCells(i, w int) (from, to int) {
	cell := p.Px / float64(w)
	lpx, rpx := p.keyEdgesPx(i)
	return int(math.Ceil(lpx / cell)), int(math.Floor(rpx / cell))
}

// shortLabels are the shorter forms of a label for a narrow key (the UC-P8):
// the first that fits is used.
var shortLabels = map[string][]string{
	"Hold: restart":  {"Hold:restart", "Restart"},
	"Hold: download": {"Hold:download", "Download"},
	"Scroll down":    {"Down"},
	"Scroll up":      {"Up"},
	"Previous":       {"Prev"},
}

func cells(s string) int { return utf8.RuneCountInString(s) }

// labelFor is the label as it fits n cells.
func labelFor(l string, n int) string {
	if cells(l) <= n {
		return l
	}
	for _, s := range shortLabels[l] {
		if cells(s) <= n {
			return s
		}
	}
	return string([]rune(l)[:n])
}

// KeyLegend is the footer of the panel: each label centred on its key,
// never wider than the key, nothing over the logo.
func (p Panel) KeyLegend(k [KeyPlaces]string, w int) string {
	p = p.orDefault()
	row := []rune(strings.Repeat(" ", w))
	cell := p.Px / float64(w)
	for i, l := range k {
		if l == "" {
			continue
		}
		lpx, rpx := p.keyEdgesPx(i)
		from, to := p.keyCells(i, w)
		if to <= from {
			continue
		}
		l = labelFor(l, to-from)
		start := int(math.Round((lpx+rpx)/2/cell - float64(cells(l))/2))
		if start < from {
			start = from
		}
		if start+cells(l) > to {
			start = to - cells(l)
		}
		for j, r := range []rune(l) {
			row[start+j] = r
		}
	}
	return strings.TrimRight(string(row), " ")
}

// keys are the functions of the keys on one page ("" = none).
type keys struct {
	back, prev, next, ok string
	hold                 bool // ok is the 3 s hold (a terminal: Enter, then y)
}

// places is the label of each touch key.
func (k keys) places() [KeyPlaces]string {
	var p [KeyPlaces]string
	p[PlaceSpeaker], p[PlaceVolDown], p[PlaceVolUp], p[PlaceMic] = k.back, k.prev, k.next, k.ok
	return p
}

// terminal is the footer of a terminal: its keys as text.
func (k keys) terminal() string {
	var p []string
	if k.prev != "" {
		p = append(p, "Up = "+k.prev)
	}
	if k.next != "" {
		p = append(p, "Down = "+k.next)
	}
	if k.ok != "" {
		if k.hold {
			p = append(p, "Enter, then y = "+strings.TrimPrefix(k.ok, "Hold: "))
		} else {
			p = append(p, "Enter = "+k.ok)
		}
	}
	if k.back != "" {
		p = append(p, "b = "+k.back)
	}
	p = append(p, "q = quit")
	return strings.Join(p, "   ")
}

// wrap breaks a row into rows of at most w cells, at spaces. A continued
// row has the indent of the first row and 4 more spaces (none on a very
// narrow screen). A word longer than a row is broken. "" gives one empty
// row; a width below 1 gives the row as it is.
func wrap(s string, w int) []string {
	if cells(s) <= w || w < 1 {
		return []string{s}
	}
	rs := []rune(s)
	n := 0
	for n < len(rs) && rs[n] == ' ' {
		n++
	}
	cont := strings.Repeat(" ", n+4)
	if cells(cont) > w/2 {
		cont = "    "
	}
	if cells(cont) >= w {
		cont = "" // review R1.2 low 2: a continued row must have room
	}
	var out []string
	first := true
	for len(rs) > 0 {
		room := w
		if !first {
			room = w - cells(cont)
		}
		if len(rs) <= room {
			out = append(out, prefix(first, cont)+string(rs))
			break
		}
		cut := -1
		for i := room; i > 0; i-- {
			if rs[i] == ' ' && (!first || i > n) {
				cut = i
				break
			}
		}
		next := cut + 1
		if cut <= 0 {
			cut, next = room, room // a long word: break it
		}
		out = append(out, prefix(first, cont)+strings.TrimRight(string(rs[:cut]), " "))
		rs = rs[next:]
		for len(rs) > 0 && rs[0] == ' ' {
			rs = rs[1:]
		}
		first = false
	}
	return out
}

func prefix(first bool, cont string) string {
	if first {
		return ""
	}
	return cont
}

// wrapLines wraps every line, each wrapped row keeping the line's level.
func wrapLines(ls []status.Line, w int) []status.Line {
	var out []status.Line
	for _, l := range ls {
		for _, t := range wrap(l.Text, w) {
			out = append(out, status.Line{Level: l.Level, Text: t})
		}
	}
	return out
}

// squeeze drops empty rows, from the bottom up, until ls has at most n rows.
func squeeze(ls []status.Line, n int) []status.Line {
	out := append([]status.Line(nil), ls...)
	for i := len(out) - 1; i >= 0 && len(out) > n; i-- {
		if out[i].Text == "" {
			out = append(out[:i], out[i+1:]...)
		}
	}
	return out
}

// screen is one page laid out: the rows that stay at the top (the menu),
// the rows below them, and the keys.
type screen struct {
	head, body []status.Line
	keys       keys
	pages      bool // Status: Speaker shows the next rows when the body does not fit
	fromEnd    bool // Log: the body is shown up to its newest row
}

// rows lays the screen out on w x h cells: every row of the result is at
// most w cells, and there are exactly h rows (title, rule, body, rule,
// MsgRows message rows, the key legend). It sets m.more and clamps
// m.Scroll.
func (m *Model) rows(s screen, w, h int) []status.Line {
	room := h - 4 - MsgRows
	head := wrapLines(s.head, w)
	if len(head) > room {
		head = head[:room]
	}
	body := wrapLines(s.body, w)
	free := room - len(head)
	if len(body) > free {
		// without its empty rows it may fit; a page that then still does
		// not fit keeps them (it scrolls, or shows the sign)
		if sq := squeeze(body, free); len(sq) <= free || !(s.pages || s.fromEnd) {
			body = sq
		}
	}
	rule := ""
	m.more = false
	if over := len(body) - free; over > 0 && free > 0 {
		switch {
		case s.pages:
			// pages of free rows; the last page is full (it ends at the
			// last row)
			m.more = true
			n := (len(body) + free - 1) / free
			if m.Scroll < 0 {
				m.Scroll = 0
			}
			start := (m.Scroll % n) * free
			if start > over {
				start = over
			}
			body = body[start : start+free]
			rule = fmt.Sprintf(" rows %d-%d of %d ", start+1, start+free, free+over)
		case s.fromEnd:
			if m.Scroll > over {
				m.Scroll = over
			}
			if m.Scroll < 0 {
				m.Scroll = 0
			}
			start := over - m.Scroll
			body = body[start : start+free]
			rule = fmt.Sprintf(" rows %d-%d of %d ", start+1, start+free, free+over)
		default:
			// a page that does not scroll: the last row says that rows are
			// missing
			body = append(body[:free-1], status.Line{Level: status.Bad,
				Text: fit(fmt.Sprintf("!! %d more rows do not fit on this screen.", over+1), w)})
		}
	} else if s.fromEnd {
		m.Scroll = 0
	}
	k := s.keys
	if s.pages && m.more {
		k.back = "More"
	}
	out := []status.Line{{Level: status.Heading, Text: fit(m.Title, w)}, {Text: strings.Repeat("-", w)}}
	out = append(out, head...)
	out = append(out, body...)
	for len(out) < 2+room {
		out = append(out, status.Line{})
	}
	r := strings.Repeat("-", w)
	if rule != "" && cells(rule)+8 < w {
		r = strings.Repeat("-", w-8-cells(rule)) + rule + strings.Repeat("-", 8)
	}
	out = append(out, status.Line{Text: r})
	msg := wrap(m.Msg, w)
	if m.Msg == "" {
		msg = nil
	}
	if len(msg) > MsgRows {
		msg = msg[:MsgRows]
		msg[MsgRows-1] = fit(msg[MsgRows-1], w-4)
		msg[MsgRows-1] = strings.TrimRight(msg[MsgRows-1], " ") + " ..."
	}
	for len(msg) < MsgRows {
		msg = append(msg, "")
	}
	for _, t := range msg {
		out = append(out, status.Line{Level: status.Warn, Text: t})
	}
	footer := m.Panel.KeyLegend(k.places(), w)
	if m.Terminal {
		footer = fit(k.terminal(), w)
	}
	out = append(out, status.Line{Level: status.Heading, Text: strings.TrimRight(footer, " ")})
	return out
}

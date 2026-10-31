package flow

// Property tests of Wrap and Layout on random rows and frames. They are the
// probes R-W1 and R-L1 of the independent review of flow-r2
// (FLOW-R2-REVIEW.md, the reviewer's own reading of the spec), kept here as
// the review asked. -short runs a tenth of the cases.

import (
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"uc-pxx-installer/internal/status"
)

func propN(n int) int {
	if testing.Short() {
		return n / 10
	}
	return n
}

func propNoSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' {
			return -1
		}
		return r
	}, s)
}

// my own reading of the spec of Wrap: the continuation prefix
func propCont(s string, w int) string {
	rs := []rune(s)
	n := 0
	for n < len(rs) && rs[n] == ' ' {
		n++
	}
	lead := string(rs[:n])
	if strings.HasPrefix(string(rs[n:]), "| ") {
		lead += "| "
	}
	c := lead + "    "
	if utf8.RuneCountInString(c) > w/2 {
		c = "    "
	}
	return c
}

func propRandText(r *rand.Rand) string {
	words := []string{"a", "bb", "ccc", "Do", "not", "disconnect", "the", "power.", "|", "/run/p10inst/flow/plan.json",
		strings.Repeat("f", 64), strings.Repeat("x", 130), "é", "漢字", "", " ", "  "}
	var b strings.Builder
	if r.Intn(3) == 0 {
		b.WriteString(strings.Repeat(" ", r.Intn(12)))
	}
	if r.Intn(4) == 0 {
		b.WriteString("| ")
	}
	n := r.Intn(80)
	for i := 0; i < n; i++ {
		b.WriteString(words[r.Intn(len(words))])
		b.WriteString(strings.Repeat(" ", 1+r.Intn(2)))
	}
	return strings.TrimRight(b.String(), " ") + "z"
}

// Wrap loses no character, keeps the order, every row fits, every
// continued row starts with the continuation prefix.
func TestWrapProperty(t *testing.T) {
	r := rand.New(rand.NewSource(20261031))
	n := 0
	for i := 0; i < propN(300000); i++ {
		s := propRandText(r)
		w := 40 + r.Intn(121)
		rows := Wrap(s, w)
		cont := propCont(s, w)
		var got strings.Builder
		for j, row := range rows {
			if utf8.RuneCountInString(row) > w {
				t.Fatalf("w %d row %d too long %q (in %q)", w, j, row, s)
			}
			if j > 0 {
				if !strings.HasPrefix(row, cont) {
					t.Fatalf("w %d row %d %q lacks cont %q (in %q)", w, j, row, cont, s)
				}
				row = row[len(cont):]
			}
			if strings.TrimSpace(row) == "" {
				t.Fatalf("w %d row %d empty (in %q) rows %q", w, j, s, rows)
			}
			got.WriteString(row)
		}
		if propNoSpace(got.String()) != propNoSpace(s) {
			t.Fatalf("w %d: text changed\nin  %q\nout %q", w, s, rows)
		}
		n++
	}
	t.Logf("%d random rows wrapped: no character lost, order kept, every row fits", n)
}

func propLines(r *rand.Rand, n int) []status.Line {
	var l []status.Line
	for i := 0; i < n; i++ {
		s := ""
		if r.Intn(5) != 0 {
			s = propRandText(r)
		}
		l = append(l, status.Line{Level: r.Intn(5), Text: s})
	}
	return l
}

// Layout on random frames. Every row fits; the screen has exactly h
// rows; and when Problem is "" (on a screen that is not the log) every body
// character, the title, the footer and the message are on the screen.
func TestLayoutProperty(t *testing.T) {
	r := rand.New(rand.NewSource(31))
	silent, hiddenNoKeys, scrollShown := 0, 0, 0
	for i := 0; i < propN(60000); i++ {
		w, h := 40+r.Intn(121), 12+r.Intn(50)
		tr := []rune(propRandText(r))
		f := Frame{Title: string(tr[:min(len(tr), r.Intn(200))]), Body: propLines(r, r.Intn(70)),
			Footer: strings.Repeat("k", r.Intn(160)), Squeeze: r.Intn(2) == 0, Scroll: r.Intn(40), FromEnd: r.Intn(3) == 0}
		if r.Intn(2) == 0 {
			f.Msg = propRandText(r)
		}
		if r.Intn(2) == 0 {
			f.ScrollKeys = "Vol+ / Vol- scroll"
		}
		if r.Intn(3) == 0 {
			f.Tail = propLines(r, r.Intn(10))
		}
		lay := f.Layout(w, h)
		all := append([]string{lay.Title, lay.Footer}, lay.Msg...)
		for _, b := range lay.Body {
			all = append(all, b.Text)
		}
		for _, x := range all {
			if utf8.RuneCountInString(x) > w {
				t.Fatalf("row of %d > %d", utf8.RuneCountInString(x), w)
			}
		}
		if len(lay.Body) > lay.Room || 4+lay.Room+len(lay.Msg) != h || len(lay.Msg) < 1 || len(lay.Msg) > 3 {
			t.Fatalf("rows: body %d room %d msg %d h %d", len(lay.Body), lay.Room, len(lay.Msg), h)
		}
		if lay.Hidden > 0 && f.ScrollKeys != "" && lay.Cut == 0 {
			if !strings.Contains(lay.Footer, "rows ") {
				t.Fatalf("hidden rows and the footer does not say so: %q", lay.Footer)
			}
			scrollShown++
		}
		if lay.Hidden > 0 && f.ScrollKeys == "" {
			hiddenNoKeys++
		}
		if lay.Problem(ScreenStatus) != "" {
			continue
		}
		// nothing lost: all body text (except the tail) is on the screen
		var in, out strings.Builder
		for _, b := range f.Body {
			in.WriteString(b.Text)
		}
		for _, b := range lay.Body {
			out.WriteString(b.Text)
		}
		var tail strings.Builder
		for _, b := range f.Tail {
			tail.WriteString(b.Text)
		}
		// the continuation prefix of a "| " row adds a "|" (R-W1 checks the exact rows)
		nobar := func(s string) string { return strings.ReplaceAll(propNoSpace(s), "|", "") }
		o := nobar(out.String())
		if !strings.HasPrefix(o, nobar(in.String())) {
			t.Fatalf("Problem is empty but body text is lost:\nin  %q\nout %q", in.String(), out.String())
		}
		if lay.Title != f.Title || !strings.HasPrefix(lay.Footer, f.Footer) ||
			nobar(strings.Join(lay.Msg, "")) != nobar(f.Msg) {
			t.Fatalf("Problem is empty but the title, footer or message is cut:\ntitle %q / %q\nfooter %q / %q\nmsg %q / %q", f.Title, lay.Title, f.Footer, lay.Footer, f.Msg, lay.Msg)
		}
		silent++
	}
	t.Logf("%d frames with no problem lost nothing; %d frames hid rows with the scroll keys in the footer; %d hid rows without scroll keys (a sign in the last row, and Problem)", silent, scrollShown, hiddenNoKeys)
}

//go:build install

package main

// The status UI (package ui) has its own copy of the key row geometry: the
// status build has no package flow. Both must give the same legend for the
// same labels on both panels and at every console width the panels can have.

import (
	"testing"

	"uc-pxx-installer/internal/flow"
	"uc-pxx-installer/internal/ui"
)

func TestKeyLegendSameAsFlow(t *testing.T) {
	if ui.KeyPlaces != flow.KeyPlaces || ui.PlaceSpeaker != flow.PlaceSpeaker || ui.PlaceVolDown != flow.PlaceVolDown ||
		ui.PlaceVolUp != flow.PlaceVolUp || ui.PlaceMic != flow.PlaceMic || ui.MsgRows != flow.MsgRows {
		t.Fatal("the key places or the message rows differ")
	}
	for _, p := range []struct {
		u ui.Panel
		f flow.Panel
	}{{ui.PanelUCP10, flow.PanelUCP10}, {ui.PanelUCP8, flow.PanelUCP8}} {
		if p.u.Model != p.f.Model || p.u.Px != p.f.Px || p.u.MarginPx != p.f.MarginPx || p.u.LogoFromPx != p.f.LogoFromPx ||
			p.u.LogoToPx != p.f.LogoToPx {
			t.Fatalf("panel %s: %+v vs %+v", p.u.Model, p.u, p.f)
		}
		if ui.PanelFor("crestron,uc-p8\x00rockchip,rk3399").Model != flow.PanelFor("crestron,uc-p8\x00rockchip,rk3399").Model ||
			ui.PanelFor("crestron,uc-p10").Model != flow.PanelFor("crestron,uc-p10").Model {
			t.Fatal("PanelFor")
		}
		for _, labels := range [][4]string{
			{"", "Previous", "Next", "Open"},
			{"Back", "Scroll up", "Scroll down", "Back"},
			{"Back", "", "", "Hold: restart"},
			{"Back", "", "", ""},
			{"More", "Previous", "Next", "Open"},
		} {
			var k [flow.KeyPlaces]string
			k[flow.PlaceSpeaker], k[flow.PlaceVolDown], k[flow.PlaceVolUp], k[flow.PlaceMic] = labels[0], labels[1], labels[2], labels[3]
			for w := 70; w <= 160; w++ {
				fp := p.f
				want := flow.Frame{Keys: k, Panel: &fp}.Layout(w, 37).Footer
				if got := p.u.KeyLegend(k, w); got != want {
					t.Fatalf("%s %q at %d:\nui   %q\nflow %q", p.u.Model, labels, w, got, want)
				}
			}
		}
	}
}

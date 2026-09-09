package parse

import "testing"

func TestRenderString_refTags(t *testing.T) {
	text, edges := RenderString("Cast {@spell Testbolt} at the {@creature Test Beast|MM}.")
	if text != "Cast Testbolt at the Test Beast." {
		t.Fatalf("text=%q", text)
	}
	if len(edges) != 2 {
		t.Fatalf("edges=%d %+v", len(edges), edges)
	}
	if edges[0].Tag != "spell" || edges[0].ToKind != "spell" || edges[0].ToName != "Testbolt" {
		t.Fatalf("spell edge %+v", edges[0])
	}
	if edges[1].ToKind != "monster" || edges[1].ToName != "Test Beast" || edges[1].ToSource != "MM" {
		t.Fatalf("creature edge %+v", edges[1])
	}
}

func TestRenderString_displayName(t *testing.T) {
	text, edges := RenderString("see {@item bag of holding|DMG|that bag}")
	if text != "see that bag" {
		t.Fatalf("text=%q", text)
	}
	if len(edges) != 1 || edges[0].ToName != "bag of holding" || edges[0].ToSource != "DMG" || edges[0].Display != "that bag" {
		t.Fatalf("edge %+v", edges[0])
	}
}

func TestRenderString_nestedAndInline(t *testing.T) {
	text, edges := RenderString("in {@variantrule Darkness|XPHB} while {@condition Invisible|XPHB}, {@dc 15} or {@dice 2d6} {@damage 1d8}.")
	if want := "in Darkness while Invisible, DC 15 or 2d6 1d8."; text != want {
		t.Fatalf("text=%q", text)
	}
	if len(edges) != 2 {
		t.Fatalf("edges=%d %+v", len(edges), edges)
	}
}

func TestRenderString_unbalancedLeavesText(t *testing.T) {
	text, _ := RenderString("hello {@spell oops")
	if text != "hello {@spell oops" {
		t.Fatalf("text=%q", text)
	}
}

func TestRenderString_attackTag(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "melee weapon", in: "{@atk mw} {@hit 4} to hit", want: "Melee Weapon Attack: +4 to hit"},
		{name: "multiple", in: "{@atk mw, rw}", want: "Melee Weapon or Ranged Weapon Attack:"},
		{name: "single letter", in: "{@atk r}", want: "Ranged Attack:"},
		{name: "attack roll", in: "{@atkr r}", want: "Ranged Attack Roll:"},
		{name: "unknown", in: "{@atk xz}", want: ""},
		{name: "bare hit", in: "{@h} 5 damage", want: "Hit: 5 damage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, edges := RenderString(tt.in)
			if text != tt.want {
				t.Fatalf("text=%q, want %q", text, tt.want)
			}
			if len(edges) != 0 {
				t.Fatalf("unexpected edges: %+v", edges)
			}
		})
	}
}

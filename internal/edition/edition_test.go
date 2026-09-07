package edition

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		in      string
		want    Pref
		wantErr bool
	}{
		{"", Default, false},
		{"2024", Modern, false},
		{"2014", Classic, false},
		{"all", All, false},
		{"ALL", All, false},
		{"nope", "", true},
	}
	for _, tt := range tests {
		got, err := Parse(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("Parse(%q) want error", tt.in)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Fatalf("Parse(%q)=%q %v, want %q", tt.in, got, err, tt.want)
		}
	}
}

func TestPrefer_picksMatchingCoreReprint(t *testing.T) {
	srcs := []string{"PHB", "XPHB"}
	got := Prefer(srcs, Modern)
	if len(got) != 1 || got[0] != "XPHB" {
		t.Fatalf("2024: %v", got)
	}
	got = Prefer(srcs, Classic)
	if len(got) != 1 || got[0] != "PHB" {
		t.Fatalf("2014: %v", got)
	}
	got = Prefer(srcs, All)
	if len(got) != 2 {
		t.Fatalf("all: %v", got)
	}
	got = Prefer(srcs, "")
	if len(got) != 1 || got[0] != "XPHB" {
		t.Fatalf("default: %v", got)
	}
}

func TestPrefer_keepsUnmatchedWhenNoPreferredSource(t *testing.T) {
	srcs := []string{"TCE", "SCAG"}
	got := Prefer(srcs, Modern)
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

func TestMatch(t *testing.T) {
	if !Match("XPHB", Modern) || Match("PHB", Modern) {
		t.Fatal("2024")
	}
	if !Match("PHB", Classic) || Match("XPHB", Classic) {
		t.Fatal("2014")
	}
	if !Match("TCE", All) {
		t.Fatal("all")
	}
}

func TestFilter_entitiesBySource(t *testing.T) {
	type row struct{ Source string }
	rows := []row{{"PHB"}, {"XPHB"}}
	got := Filter(rows, func(r row) string { return r.Source }, Modern)
	if len(got) != 1 || got[0].Source != "XPHB" {
		t.Fatalf("%v", got)
	}
}

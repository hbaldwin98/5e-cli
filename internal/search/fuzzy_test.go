package search

import "testing"

func TestNameScore(t *testing.T) {
	if NameScore("testbolt", "Testbolt") != 1 {
		t.Fatal("exact")
	}
	if NameScore("testbol", "Testbolt") < 0.9 {
		t.Fatalf("prefix %v", NameScore("testbol", "Testbolt"))
	}
	if NameScore("beast", "Test Beast") < 0.7 {
		t.Fatalf("contains %v", NameScore("beast", "Test Beast"))
	}
	if NameScore("testblt", "Testbolt") == 0 {
		t.Fatal("typo should match")
	}
	if NameScore("zzzz", "Testbolt") != 0 {
		t.Fatal("unrelated")
	}
	if NameScore("firebol", "Fire Bolt") < 0.8 {
		t.Fatalf("compact name %v", NameScore("firebol", "Fire Bolt"))
	}
	if NameScore("firebol", "Copper stein with silver filigree") != 0 {
		t.Fatal("subsequence must not match long names")
	}
}

func TestFTSQuery(t *testing.T) {
	q := FTSQuery("hold breath!")
	if q != "hold* AND breath*" {
		t.Fatalf("%q", q)
	}
}

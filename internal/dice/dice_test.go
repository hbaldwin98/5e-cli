package dice

import "testing"

func seed(n int64) Query { return Query{Seed: &n} }

func TestRoll_flatAndDice(t *testing.T) {
	r, err := Roll("2d6+3", seed(1))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Terms) != 2 {
		t.Fatalf("terms: %+v", r.Terms)
	}
	if r.Terms[0].Count != 2 || r.Terms[0].Sides != 6 {
		t.Fatalf("dice term: %+v", r.Terms[0])
	}
	if !r.Terms[1].IsFlat || r.Terms[1].Flat != 3 {
		t.Fatalf("flat term: %+v", r.Terms[1])
	}
	sum := 3
	for _, v := range r.Terms[0].Rolls {
		if v < 1 || v > 6 {
			t.Fatalf("roll out of range: %d", v)
		}
		sum += v
	}
	if r.Total != sum {
		t.Fatalf("total %d != expected %d", r.Total, sum)
	}
}

func TestRoll_deterministicWithSeed(t *testing.T) {
	a, err := Roll("4d6kh3", seed(42))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Roll("4d6kh3", seed(42))
	if err != nil {
		t.Fatal(err)
	}
	if a.Total != b.Total {
		t.Fatalf("same seed produced different totals: %d vs %d", a.Total, b.Total)
	}
}

func TestRoll_keepHighestLowest(t *testing.T) {
	r, err := Roll("4d6kh3", seed(7))
	if err != nil {
		t.Fatal(err)
	}
	term := r.Terms[0]
	if len(term.Rolls) != 4 || len(term.Kept) != 3 {
		t.Fatalf("kept mismatch: %+v", term)
	}
	sum := 0
	for _, v := range term.Kept {
		sum += v
	}
	if r.Total != sum {
		t.Fatalf("total %d != kept sum %d", r.Total, sum)
	}
}

func TestRoll_advantageDisadvantage(t *testing.T) {
	adv, err := Roll("adv", seed(3))
	if err != nil {
		t.Fatal(err)
	}
	if len(adv.Terms[0].Rolls) != 2 || len(adv.Terms[0].Kept) != 1 {
		t.Fatalf("adv: %+v", adv.Terms[0])
	}
	dis, err := Roll("dis", seed(3))
	if err != nil {
		t.Fatal(err)
	}
	if dis.Terms[0].Keep != "l" {
		t.Fatalf("dis should keep lowest: %+v", dis.Terms[0])
	}
}

func TestRoll_negativeTerm(t *testing.T) {
	r, err := Roll("1d4-2", seed(5))
	if err != nil {
		t.Fatal(err)
	}
	want := r.Terms[0].Rolls[0] - 2
	if r.Total != want {
		t.Fatalf("total %d != %d", r.Total, want)
	}
}

func TestRoll_invalid(t *testing.T) {
	cases := []string{"", "d", "3d", "d6k", "2d6kh5", "notdice", "2dx"}
	for _, c := range cases {
		if _, err := Roll(c, Query{}); err == nil {
			t.Fatalf("expected error for %q", c)
		}
	}
}

func TestRoll_percentile(t *testing.T) {
	r, err := Roll("1d100", seed(9))
	if err != nil {
		t.Fatal(err)
	}
	if r.Terms[0].Sides != 100 {
		t.Fatalf("sides: %+v", r.Terms[0])
	}
}

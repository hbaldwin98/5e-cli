// Package dice parses and rolls standard tabletop dice notation, independent
// of the indexed random tables in internal/table.
package dice

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

// Query controls one roll. Seed makes it reproducible, the same way
// internal/table.Query does.
type Query struct {
	Seed *int64
}

// Report is the result of rolling a parsed dice expression.
type Report struct {
	Expression string `json:"expression"`
	Total      int    `json:"total"`
	Terms      []Term `json:"terms"`
}

// Term is one signed piece of the expression as rolled: a flat modifier, or a
// dice group with every die it rolled and (if some were dropped by keep-high
// or keep-low) which ones counted toward the total.
type Term struct {
	Sign    int    `json:"sign"` // +1 or -1
	Count   int    `json:"count,omitempty"`
	Sides   int    `json:"sides,omitempty"`
	Rolls   []int  `json:"rolls,omitempty"`
	Kept    []int  `json:"kept,omitempty"`
	Flat    int    `json:"flat,omitempty"`
	IsFlat  bool   `json:"isFlat"`
	Keep    string `json:"keep,omitempty"` // "h" or "l" when a keep-modifier applied
	KeepNum int    `json:"keepCount,omitempty"`
}

// Roll parses and rolls a dice expression such as "2d6+3", "1d20", "4d6kh3",
// "adv", or "dis". adv/dis are shorthand for 2d20kh1 / 2d20kl1, the common
// advantage/disadvantage roll.
func Roll(expr string, q Query) (Report, error) {
	terms, err := parse(expr)
	if err != nil {
		return Report{}, err
	}
	if len(terms) == 0 {
		return Report{}, fmt.Errorf("empty dice expression")
	}

	seed := time.Now().UnixNano()
	if q.Seed != nil {
		seed = *q.Seed
	}
	rng := rand.New(rand.NewSource(seed))

	total := 0
	out := make([]Term, 0, len(terms))
	for _, t := range terms {
		if t.IsFlat {
			total += t.Sign * t.Flat
			out = append(out, t)
			continue
		}
		rolls := make([]int, t.Count)
		for i := range rolls {
			rolls[i] = 1 + rng.Intn(t.Sides)
		}
		kept := rolls
		if t.Keep != "" {
			kept = keepDice(rolls, t.Keep, t.KeepNum)
		}
		sum := 0
		for _, v := range kept {
			sum += v
		}
		t.Rolls = rolls
		t.Kept = kept
		total += t.Sign * sum
		out = append(out, t)
	}
	return Report{Expression: strings.TrimSpace(expr), Total: total, Terms: out}, nil
}

func keepDice(rolls []int, mode string, n int) []int {
	if n <= 0 || n >= len(rolls) {
		cp := append([]int(nil), rolls...)
		return cp
	}
	idx := make([]int, len(rolls))
	for i := range idx {
		idx[i] = i
	}
	sort := func(less func(a, b int) bool) {
		for i := 1; i < len(idx); i++ {
			for j := i; j > 0 && less(idx[j], idx[j-1]); j-- {
				idx[j], idx[j-1] = idx[j-1], idx[j]
			}
		}
	}
	if mode == "h" {
		sort(func(a, b int) bool { return rolls[a] > rolls[b] })
	} else {
		sort(func(a, b int) bool { return rolls[a] < rolls[b] })
	}
	kept := make([]int, 0, n)
	for _, i := range idx[:n] {
		kept = append(kept, rolls[i])
	}
	return kept
}

// parse splits an expression into signed terms. adv/dis are recognized as
// whole-expression shorthand only; mixing them with other terms (e.g.
// "adv+3") is not supported.
func parse(expr string) ([]Term, error) {
	trimmed := strings.ToLower(strings.TrimSpace(expr))
	switch trimmed {
	case "adv", "advantage":
		return []Term{{Sign: 1, Count: 2, Sides: 20, Keep: "h", KeepNum: 1}}, nil
	case "dis", "disadvantage":
		return []Term{{Sign: 1, Count: 2, Sides: 20, Keep: "l", KeepNum: 1}}, nil
	}

	s := strings.ReplaceAll(trimmed, " ", "")
	if s == "" {
		return nil, fmt.Errorf("empty dice expression")
	}

	var terms []Term
	sign := 1
	i := 0
	for i < len(s) {
		switch s[i] {
		case '+':
			sign = 1
			i++
			continue
		case '-':
			sign = -1
			i++
			continue
		}
		start := i
		for i < len(s) && (isDigit(s[i]) || s[i] == 'd' || s[i] == 'k' || s[i] == 'h' || s[i] == 'l') {
			i++
		}
		chunk := s[start:i]
		if chunk == "" {
			return nil, fmt.Errorf("invalid dice expression %q", expr)
		}
		t, err := parseTerm(chunk, sign)
		if err != nil {
			return nil, fmt.Errorf("invalid dice expression %q: %w", expr, err)
		}
		terms = append(terms, t)
		sign = 1
	}
	return terms, nil
}

func parseTerm(chunk string, sign int) (Term, error) {
	di := strings.IndexByte(chunk, 'd')
	if di < 0 {
		n, err := strconv.Atoi(chunk)
		if err != nil {
			return Term{}, fmt.Errorf("expected a number or dice group, got %q", chunk)
		}
		return Term{Sign: sign, Flat: n, IsFlat: true}, nil
	}

	countStr, rest := chunk[:di], chunk[di+1:]
	count := 1
	if countStr != "" {
		n, err := strconv.Atoi(countStr)
		if err != nil || n < 1 {
			return Term{}, fmt.Errorf("invalid dice count %q", countStr)
		}
		count = n
	}
	if count > 1000 {
		return Term{}, fmt.Errorf("dice count %d is too large", count)
	}

	sidesStr := rest
	keep, keepNum := "", 0
	if ki := strings.IndexAny(rest, "kK"); ki >= 0 {
		sidesStr = rest[:ki]
		modStr := rest[ki+1:]
		if len(modStr) == 0 {
			return Term{}, fmt.Errorf("expected h or l after k in %q", chunk)
		}
		switch modStr[0] {
		case 'h':
			keep = "h"
		case 'l':
			keep = "l"
		default:
			return Term{}, fmt.Errorf("expected h or l after k in %q", chunk)
		}
		numStr := modStr[1:]
		if numStr == "" {
			keepNum = 1
		} else {
			n, err := strconv.Atoi(numStr)
			if err != nil || n < 1 {
				return Term{}, fmt.Errorf("invalid keep count in %q", chunk)
			}
			keepNum = n
		}
	}
	if sidesStr == "" {
		return Term{}, fmt.Errorf("missing die size in %q", chunk)
	}
	sides, err := strconv.Atoi(sidesStr)
	if err != nil || sides < 1 {
		return Term{}, fmt.Errorf("invalid die size %q", sidesStr)
	}
	if sides > 100000 {
		return Term{}, fmt.Errorf("die size %d is too large", sides)
	}
	if keepNum > count {
		return Term{}, fmt.Errorf("keep count %d exceeds dice count %d", keepNum, count)
	}
	return Term{Sign: sign, Count: count, Sides: sides, Keep: keep, KeepNum: keepNum}, nil
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

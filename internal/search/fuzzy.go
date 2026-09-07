package search

import (
	"strings"
	"unicode"
)

// NameScore ranks how well query matches an entity name. 0 means no match.
func NameScore(query, name string) float64 {
	q := strings.ToLower(strings.TrimSpace(query))
	n := strings.ToLower(strings.TrimSpace(name))
	if q == "" || n == "" {
		return 0
	}
	if q == n {
		return 1
	}
	if strings.HasPrefix(n, q) {
		return 0.92
	}
	if strings.Contains(n, q) {
		return 0.8
	}
	nCompact := strings.ReplaceAll(n, " ", "")
	if strings.HasPrefix(nCompact, q) || strings.Contains(nCompact, q) {
		return 0.85
	}
	qr, nr := []rune(q), []rune(n)
	if len(nr) > len(qr)+6 {
		return 0
	}
	d := levenshtein(q, n)
	maxLen := len([]rune(q))
	if l := len([]rune(n)); l > maxLen {
		maxLen = l
	}
	if maxLen == 0 {
		return 0
	}
	sim := 1 - float64(d)/float64(maxLen)
	if sim < 0.65 {
		return 0
	}
	return sim * 0.7
}

func levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	if len(ar) > 64 || len(br) > 64 {
		return 64
	}
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := cur[j-1] + 1
			sub := prev[j-1] + cost
			cur[j] = min(del, ins, sub)
		}
		prev, cur = cur, prev
	}
	return prev[len(br)]
}

// FTSQuery turns a user string into a MATCH expression.
func FTSQuery(q string) string {
	var parts []string
	for _, f := range strings.Fields(q) {
		var b strings.Builder
		for _, r := range f {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				b.WriteRune(r)
			}
		}
		if b.Len() == 0 {
			continue
		}
		parts = append(parts, b.String()+"*")
	}
	return strings.Join(parts, " AND ")
}

// Prose reports whether the query looks like a phrase rather than a name.
func Prose(q string) bool {
	return len(strings.Fields(q)) >= 2
}

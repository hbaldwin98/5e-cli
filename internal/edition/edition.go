package edition

import (
	"fmt"
	"strings"
)

// Pref is a 5e rules-edition preference for lookup and retrieval.
type Pref string

const (
	All     Pref = "all"
	Classic Pref = "2014"
	Modern  Pref = "2024"
	Default Pref = Modern
)

// Parse accepts 2014, 2024, all, or empty (Default).
func Parse(s string) (Pref, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return Default, nil
	case "all":
		return All, nil
	case "2014":
		return Classic, nil
	case "2024":
		return Modern, nil
	default:
		return "", fmt.Errorf("edition must be 2014, 2024, or all (got %q)", s)
	}
}

// Year is 2024 for the revised core books, otherwise 2014.
func Year(source string) int {
	switch strings.ToUpper(source) {
	case "XPHB", "XMM", "XDMG":
		return 2024
	default:
		return 2014
	}
}

// Prefer keeps sources in the requested edition. If none match, it returns srcs unchanged.
func Prefer(srcs []string, p Pref) []string {
	if p == "" {
		p = Default
	}
	if p == All || len(srcs) <= 1 {
		return srcs
	}
	want := 2014
	if p == Modern {
		want = 2024
	}
	var matched []string
	for _, src := range srcs {
		if Year(src) == want {
			matched = append(matched, src)
		}
	}
	if len(matched) == 0 {
		return srcs
	}
	return matched
}

// Match reports whether source belongs to the preferred edition.
func Match(source string, p Pref) bool {
	if p == "" {
		p = Default
	}
	if p == All {
		return true
	}
	if p == Modern {
		return Year(source) == 2024
	}
	return Year(source) == 2014
}

// Filter keeps items whose source is in the preferred edition.
func Filter[T any](items []T, source func(T) string, p Pref) []T {
	if p == "" {
		p = Default
	}
	if p == All || len(items) <= 1 {
		return items
	}
	srcs := make([]string, len(items))
	for i, it := range items {
		srcs[i] = source(it)
	}
	keep := Prefer(srcs, p)
	if len(keep) == len(items) {
		return items
	}
	ok := make(map[string]bool, len(keep))
	for _, s := range keep {
		ok[strings.ToUpper(s)] = true
	}
	out := make([]T, 0, len(keep))
	for _, it := range items {
		if ok[strings.ToUpper(source(it))] {
			out = append(out, it)
		}
	}
	return out
}

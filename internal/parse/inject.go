package parse

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
)

// ApplyProperties resolves 5etools property injectors — {=field} and
// {=field/modifiers} — in s against obj, mirroring Renderer.applyProperties.
// A token names one of the record's own fields, so obj is always the object
// the string came from: "{=bonusWeapon} bonus to attack" on a +1 weapon
// renders as "+1 bonus to attack", and "{=amount1/v} tablespoons" on a recipe
// calling for 0.5 renders as "½ tablespoons".
//
// A token naming a field the record doesn't carry is left untouched rather
// than blanked, so a data shape this doesn't understand degrades to the raw
// token instead of silently losing the words around it.
func ApplyProperties(s string, obj map[string]any) string {
	if !strings.Contains(s, "{=") {
		return s
	}
	var b strings.Builder
	for {
		start := strings.Index(s, "{=")
		if start < 0 {
			b.WriteString(s)
			break
		}
		end := strings.Index(s[start:], "}")
		if end < 0 {
			b.WriteString(s)
			break
		}
		end += start
		b.WriteString(s[:start])
		if out, ok := applyInjector(s[start+2:end], obj); ok {
			b.WriteString(out)
		} else {
			b.WriteString(s[start : end+1])
		}
		s = s[end+1:]
	}
	return b.String()
}

// opOrder is Renderer.applyProperties._OP_ORDER: modifiers are applied in a
// fixed order regardless of the order they were written in, so "/cxt" rounds
// up before converting to words before title-casing.
const opOrder = "rfcvxltua"

func applyInjector(token string, obj map[string]any) (string, bool) {
	path, modifiers, _ := strings.Cut(token, "/")
	value, ok := obj[path]
	if !ok || value == nil {
		return "", false
	}
	if modifiers == "" {
		return scalarString(value), true
	}
	ops := strings.Split(modifiers, "")
	sort.SliceStable(ops, func(i, j int) bool {
		return strings.Index(opOrder, ops[i]) < strings.Index(opOrder, ops[j])
	})
	for _, op := range ops {
		value = applyModifier(op, value)
	}
	return scalarString(value), true
}

func applyModifier(op string, value any) any {
	switch op {
	case "r", "f", "c":
		n, ok := floatValue(value)
		if !ok {
			return value
		}
		switch op {
		case "r":
			return math.Round(n)
		case "f":
			return math.Floor(n)
		default:
			return math.Ceil(n)
		}
	case "v":
		return numberToVulgar(value)
	case "x":
		return numberToText(value)
	case "l":
		return strings.ToLower(scalarString(value))
	case "u":
		return strings.ToUpper(scalarString(value))
	case "t":
		return titleCase(scalarString(value))
	case "a":
		s := scalarString(value)
		if s != "" && strings.ContainsRune("aeiouAEIOU", rune(s[0])) {
			return "an"
		}
		return "a"
	}
	return value
}

// vulgarFractions maps the decimal part of a number to its vulgar glyph, the
// same set Parser.numberToVulgar recognizes. A fraction outside it is left as
// a plain decimal.
var vulgarFractions = map[string]string{
	"125": "⅛", "2": "⅕", "25": "¼", "375": "⅜", "4": "⅖", "5": "½",
	"6": "⅗", "625": "⅝", "75": "¾", "8": "⅘", "875": "⅞",
}

func numberToVulgar(value any) any {
	n, ok := floatValue(value)
	if !ok {
		return value
	}
	text := strconv.FormatFloat(n, 'f', -1, 64)
	neg := strings.HasPrefix(text, "-")
	whole, frac, hasFrac := strings.Cut(strings.TrimPrefix(text, "-"), ".")
	if !hasFrac {
		return value
	}
	glyph, ok := vulgarFractions[frac]
	if !ok {
		return value
	}
	if whole == "0" {
		whole = ""
	}
	if neg {
		whole = "-" + whole
	}
	return whole + glyph
}

var smallNumbers = []string{
	"zero", "one", "two", "three", "four", "five", "six", "seven", "eight",
	"nine", "ten", "eleven", "twelve",
}

// numberToText spells out a small whole number, which is all the /x modifier
// is used for in this data. Anything larger or fractional keeps its digits.
func numberToText(value any) any {
	n, ok := floatValue(value)
	if !ok {
		return value
	}
	if n != math.Trunc(n) || n < 0 || int(n) >= len(smallNumbers) {
		return value
	}
	return smallNumbers[int(n)]
}

func titleCase(s string) string {
	words := strings.Split(s, " ")
	for i, w := range words {
		if w == "" {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

func floatValue(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	case int:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	}
	return 0, false
}

func scalarString(v any) string {
	switch n := v.(type) {
	case string:
		return n
	case json.Number:
		return n.String()
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	case int:
		return strconv.Itoa(n)
	case bool:
		return strconv.FormatBool(n)
	}
	return ""
}

// ResolveInjectors rewrites every string anywhere inside obj with its
// injectors resolved against obj itself, returning the rewritten object and
// its re-encoded JSON. ok is false when nothing referenced an injector, so a
// caller can keep the untouched original JSON rather than re-encoding every
// record in the corpus.
//
// This runs at ingest rather than at render time because an injector always
// refers to the record's own fields — see the walk below, which keeps obj as
// the resolution scope at every depth.
func ResolveInjectors(obj map[string]any) (map[string]any, json.RawMessage, bool) {
	resolved, changed := resolveValue(obj, obj)
	if !changed {
		return obj, nil, false
	}
	out, _ := resolved.(map[string]any)
	raw, err := json.Marshal(out)
	if err != nil {
		return obj, nil, false
	}
	return out, raw, true
}

func resolveValue(v any, scope map[string]any) (any, bool) {
	switch value := v.(type) {
	case string:
		out := ApplyProperties(value, scope)
		return out, out != value
	case []any:
		changed := false
		out := make([]any, len(value))
		for i, item := range value {
			resolved, c := resolveValue(item, scope)
			out[i], changed = resolved, changed || c
		}
		return out, changed
	case map[string]any:
		changed := false
		out := make(map[string]any, len(value))
		for k, item := range value {
			resolved, c := resolveValue(item, scope)
			out[k], changed = resolved, changed || c
		}
		return out, changed
	}
	return v, false
}

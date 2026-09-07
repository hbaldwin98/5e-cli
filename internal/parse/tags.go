package parse

import (
	"strings"
)

// Edge is a {@tag} reference extracted from 5etools entries.
type Edge struct {
	Tag      string `json:"tag"`
	ToKind   string `json:"toKind,omitempty"`
	ToName   string `json:"toName"`
	ToSource string `json:"toSource,omitempty"`
	Display  string `json:"display,omitempty"`
}

// tagKind maps 5etools tag names to entity kinds.
var tagKind = map[string]string{
	"spell":       "spell",
	"creature":    "monster",
	"item":        "item",
	"condition":   "condition",
	"class":       "class",
	"subclass":    "subclass",
	"feat":        "feat",
	"variantrule": "variantrule",
	"book":        "bookSection",
	"adventure":   "adventureSection",
	"race":        "race",
	"background":  "background",
	"deity":       "deity",
	"disease":     "disease",
	"skill":       "skill",
	"action":      "action",
	"sense":       "sense",
	"table":       "table",
	"trap":        "trap",
	"hazard":      "hazard",
	"vehicle":     "vehicle",
	"object":      "object",
	"psionic":     "psionic",
	"reward":      "reward",
	"optfeature":  "optionalfeature",
	"language":    "language",
	"status":      "status",
	"cult":        "cult",
	"boon":        "boon",
}

// RenderString expands 5etools tags in s to plaintext and collects references.
func RenderString(s string) (string, []Edge) {
	var b strings.Builder
	var edges []Edge
	i := 0
	for i < len(s) {
		if i+1 < len(s) && s[i] == '{' && s[i+1] == '@' {
			end := matchingBrace(s, i)
			if end < 0 {
				b.WriteByte(s[i])
				i++
				continue
			}
			text, more := renderTag(s[i+2 : end])
			b.WriteString(text)
			edges = append(edges, more...)
			i = end + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String(), edges
}

func matchingBrace(s string, i int) int {
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

func renderTag(body string) (string, []Edge) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", nil
	}
	tag, rest, ok := strings.Cut(body, " ")
	if !ok {
		return renderBareTag(tag), nil
	}
	switch tag {
	case "i", "b", "u", "s", "italic", "bold", "strike", "underline", "color", "font":
		return RenderString(rest)
	case "atk":
		return attackLabel(pipePart(rest, 0)), nil
	case "dice", "damage", "scaledice", "scaledamage", "hitYourSpellAttack":
		inner, edges := RenderString(pipePart(rest, 0))
		return inner, edges
	case "hit":
		return "+" + pipePart(rest, 0), nil
	case "dc":
		return "DC " + pipePart(rest, 0), nil
	case "recharge":
		return "(Recharge " + rest + ")", nil
	case "chance", "d20", "initiative":
		return pipePart(rest, 0), nil
	case "filter", "loader", "footnote", "link", "5etools", "quickref":
		inner, edges := RenderString(pipePart(rest, 0))
		return inner, edges
	}
	if kind, ok := tagKind[tag]; ok {
		return renderRefTag(tag, kind, rest)
	}
	inner, edges := RenderString(pipePart(rest, 0))
	return inner, edges
}

func attackLabel(code string) string {
	labels := map[string]string{
		"mw": "Melee Weapon Attack: ",
		"rw": "Ranged Weapon Attack: ",
		"ms": "Melee Spell Attack: ",
		"rs": "Ranged Spell Attack: ",
	}
	parts := strings.Split(code, ",")
	var out []string
	for _, part := range parts {
		if label := labels[strings.TrimSpace(part)]; label != "" {
			out = append(out, strings.TrimSuffix(label, ": "))
		}
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, " or ") + ":"
}

func renderBareTag(tag string) string {
	switch tag {
	case "h":
		return "Hit:"
	case "atk", "atkm", "atkr":
		return ""
	default:
		return ""
	}
}

func renderRefTag(tag, kind, rest string) (string, []Edge) {
	parts := splitPipes(rest)
	name := strings.TrimSpace(parts[0])
	source := ""
	display := ""
	if len(parts) > 1 {
		source = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		display = strings.TrimSpace(parts[2])
	}
	label := display
	if label == "" {
		label = name
	}
	text, nested := RenderString(label)
	edge := Edge{
		Tag:      tag,
		ToKind:   kind,
		ToName:   name,
		ToSource: source,
		Display:  text,
	}
	if display == "" {
		edge.Display = ""
	}
	return text, append(nested, edge)
}

func pipePart(s string, n int) string {
	parts := splitPipes(s)
	if n >= len(parts) {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(parts[n])
}

func splitPipes(s string) []string {
	var parts []string
	depth := 0
	start := 0
	for i, r := range s {
		switch r {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case '|':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, s[start:])
	return parts
}

package ask

import (
	"regexp"
	"strings"
)

// citationPattern matches the "(kind, name, source)" triples the system
// prompt asks the model to cite claims with.
var citationPattern = regexp.MustCompile(`\(([^()]+)\)`)

type citationRef struct {
	Kind, Name, Source string
}

// parseCitations extracts every "(kind, name, source)" triple the model
// wrote, in the order it wrote them. A parenthesized aside that is not a
// three-part triple (a dice roll, an aside) is not a citation and is
// skipped.
func parseCitations(answer string) []citationRef {
	var out []citationRef
	for _, m := range citationPattern.FindAllStringSubmatch(answer, -1) {
		parts := strings.Split(m[1], ",")
		if len(parts) != 3 {
			continue
		}
		out = append(out, citationRef{
			Kind:   strings.TrimSpace(parts[0]),
			Name:   strings.TrimSpace(parts[1]),
			Source: strings.TrimSpace(parts[2]),
		})
	}
	return out
}

// validatedCitations keeps only the citations the model wrote that match a
// chunk retrieval actually returned, deduplicated and in first-cited order.
// A model can write a plausible-looking (kind, name, source) triple for a
// record it invented; it cannot make that triple collide with a chunk that
// was not in `ranked`. Citing something the corpus never returned is
// therefore dropped rather than surfaced as if it were grounded.
func validatedCitations(answer string, ranked []scoredChunk) []Hit {
	byKey := make(map[string]scoredChunk, len(ranked))
	for _, r := range ranked {
		byKey[chunkKey(r.Kind, r.Name, r.Source)] = r
	}
	seen := map[string]bool{}
	var out []Hit
	for _, c := range parseCitations(answer) {
		key := chunkKey(c.Kind, c.Name, c.Source)
		r, ok := byKey[key]
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r.hit())
	}
	return out
}

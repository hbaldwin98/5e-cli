package ask

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hbaldwin98/5e-cli/internal/compare"
	"github.com/hbaldwin98/5e-cli/internal/dice"
	"github.com/hbaldwin98/5e-cli/internal/edition"
	"github.com/hbaldwin98/5e-cli/internal/encounter"
	"github.com/hbaldwin98/5e-cli/internal/search"
	"github.com/hbaldwin98/5e-cli/internal/statblock"
	"github.com/hbaldwin98/5e-cli/internal/store"
	randomtable "github.com/hbaldwin98/5e-cli/internal/table"
)

// ToolsOptions configures BuildTools' built-in tool set. It mirrors the
// filters already in effect for the conversation, so a live lookup respects
// the same edition and SRD scope the retrieved sources do.
type ToolsOptions struct {
	Edition edition.Pref
	SRD     bool
}

// BuildTools returns the tool definitions and dispatcher for live lookups
// during a conversation: get, search, encounter, roll (indexed named
// tables), dice, references, and compare. Wiring the result into
// Config.Tools/Config.ToolExecutor lets the model pull an exact number
// (a challenge rating, an AC, a roll result) instead of only paraphrasing
// retrieved prose. It mirrors internal/mcpserver's tool set so a chat
// conversation and an external MCP client see the same capabilities.
func BuildTools(st *store.Store, opt ToolsOptions) ([]Tool, ToolExecutor) {
	tools := []Tool{
		{
			Name:        "get",
			Description: "Look up one 5e entity or book section by kind and name. Pass source when several reprints match. For a class/subclass, pass full=true whenever you need the actual rules text of its features (including any time you are building or describing a character) — the default response only lists feature names by level, not what they do. A subrace's response already includes its parent race's inherited traits merged in, so one subrace lookup is self-sufficient. To fully build a character (e.g. \"Paladin Aasimar with a Sage background\"), call get separately for the class (full=true), its subclass if named (full=true), the race or subrace, and the background — do not stop after the first successful lookup.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","description":"entity kind such as spell, monster, item, or bookSection"},"name":{"type":"string","description":"entity or section name"},"source":{"type":"string","description":"optional 5etools source id. If the user said 2014/5e/classic, use a non-X source (PHB, DMG, MM, ...); if they said 2024/5.5e/one D&D/revised/new, use the matching X-prefixed source (XPHB, XDMG, XMM, ...). Set this whenever an edition was named or implied, and do not also fetch the other edition's version in the same turn."},"full":{"type":"boolean","description":"for a class/subclass, also resolve and include every referenced feature's full rules text — set this whenever the actual mechanics matter, e.g. building or describing a character"}},"required":["kind","name"]}`),
		},
		{
			Name:        "buildCharacter",
			Description: "Resolve every entity needed to build a character in one call — class, subclass, race/subrace, and background — instead of several separate get calls. Always returns class/subclass with their full feature rules text. Use this any time a request names a combination of these (e.g. \"Paladin Aasimar with a Sage background\") rather than looking each one up individually.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"class":{"type":"string","description":"class name, e.g. Paladin"},"subclass":{"type":"string","description":"subclass name, e.g. Oath of Devotion"},"race":{"type":"string","description":"race name, e.g. Aasimar (pass the race even if it's really a subrace elsewhere, such as High Elf -> race Elf, subrace High Elf)"},"subrace":{"type":"string","description":"subrace name, e.g. High Elf, Drow"},"background":{"type":"string","description":"background name, e.g. Sage"},"source":{"type":"string","description":"optional 5etools source id applied to every lookup. If the user said 2014/5e/classic, use a non-X source (PHB, ...); if 2024/5.5e/one D&D/revised/new, use the matching X-prefixed source (XPHB, ...). Set this whenever an edition was named or implied."}},"required":[]}`),
		},
		{
			Name:        "search",
			Description: "Fuzzy name and full-text search of the rules corpus (entities and book sections).",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"name or rules text to search"},"kind":{"type":"string","description":"optional entity kind filter"},"sources":{"type":"array","items":{"type":"string"},"description":"optional 5etools source ids"},"limit":{"type":"integer","description":"maximum hits"}},"required":["query"]}`),
		},
		{
			Name:        "encounter",
			Description: "Find monsters for an encounter, filtered by challenge rating, creature type, and size. Returns CR, type, size, and page for each hit.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"monster name or text to match"},"cr":{"type":"string","description":"challenge rating such as 1/4 or 5"},"type":{"type":"string","description":"creature type such as humanoid or fey"},"size":{"type":"string","description":"creature size such as small or large"},"sources":{"type":"array","items":{"type":"string"}},"limit":{"type":"integer"}},"required":[]}`),
		},
		{
			Name:        "list",
			Description: "List entity names indexed for a kind, such as every random table (kind \"table\"), every indexed encounter table (kind \"encounter\"), or any other entity kind (monster, spell, item, feat, background, ...). Call with no kind to see which kinds are indexed. With kind \"spell\", class and/or level answer \"what spells does a Wizard get\", \"what cantrips can a Cleric cast\" (class=Cleric, level=0), or \"list every 3rd-level spell\" — the spell data records which classes each spell is on, so never answer such a question from retrieved prose or say the sources don't specify. Also use this before roll or encounter when you don't already know the exact table or region name.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","description":"entity kind to list, such as table, encounter, monster, spell, or feat; omit to list available kinds instead"},"query":{"type":"string","description":"optional substring to filter names by"},"sources":{"type":"array","items":{"type":"string"},"description":"optional 5etools source ids"},"class":{"type":"string","description":"with kind spell, keep only spells on this class's spell list (including spells granted by its subclasses), e.g. Wizard, Cleric"},"level":{"type":"integer","description":"with kind spell, keep only spells of this level; 0 means cantrips"},"limit":{"type":"integer","description":"maximum entries to return, default 100 (600 for a class/level spell list)"}},"required":[]}`),
		},
		{
			Name:        "roll",
			Description: "Roll an indexed random table by name: a plain table (e.g. Wild Magic Surge, a treasure table) or a random encounter table (kind \"encounter\", e.g. \"Arctic\", \"Airborne Encounters\") split into character-level bands. For arbitrary dice notation use the dice tool instead.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","description":"table or encounter, default table"},"name":{"type":"string","description":"table name"},"source":{"type":"string","description":"optional 5etools source id"},"count":{"type":"integer","description":"number of rows to roll, default 1"},"level":{"type":"integer","description":"character level, for an encounter table with level-banded sub-tables"},"seed":{"type":"integer","description":"optional seed for a reproducible roll"}},"required":["name"]}`),
		},
		{
			Name:        "dice",
			Description: "Roll a dice expression such as 2d6+3, 4d6kh3 (ability scores), or adv/dis (2d20 keep highest/lowest). Use this for any damage roll, ability check, or saving throw instead of computing a result yourself.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"expression":{"type":"string","description":"dice notation such as 2d6+3, 4d6kh3, or adv"},"count":{"type":"integer","description":"number of times to roll the expression, default 1"},"seed":{"type":"integer","description":"optional seed for a reproducible roll"}},"required":["expression"]}`),
		},
		{
			Name:        "references",
			Description: "Show tagged references to or from one entity (e.g. every spell that references a condition).",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","description":"entity kind such as spell, monster, or item"},"name":{"type":"string","description":"entity name"},"source":{"type":"string","description":"optional 5etools source id"},"direction":{"type":"string","description":"outgoing, incoming, or both"},"tag":{"type":"string","description":"optional tag filter such as spell or creature"}},"required":["kind","name"]}`),
		},
		{
			Name:        "compare",
			Description: "Compare the source-specific versions of one entity (e.g. a 2014 spell against its 2024 reprint) and report the top-level fields that differ.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","description":"entity kind such as spell, monster, or item"},"name":{"type":"string","description":"entity name"},"sources":{"type":"array","items":{"type":"string"},"description":"optional 5etools source ids to restrict the comparison to"}},"required":["kind","name"]}`),
		},
	}

	exec := func(_ context.Context, call ToolCall) (string, error) {
		switch call.Name {
		case "get":
			return toolGet(st, opt, call.Arguments)
		case "buildCharacter":
			return toolBuildCharacter(st, opt, call.Arguments)
		case "search":
			return toolSearch(st, opt, call.Arguments)
		case "encounter":
			return toolEncounter(st, opt, call.Arguments)
		case "list":
			return toolList(st, opt, call.Arguments)
		case "roll":
			return toolRoll(st, opt, call.Arguments)
		case "dice":
			return toolDice(call.Arguments)
		case "references":
			return toolReferences(st, opt, call.Arguments)
		case "compare":
			return toolCompare(st, opt, call.Arguments)
		default:
			return "", fmt.Errorf("unknown tool %q", call.Name)
		}
	}
	return tools, exec
}

func toJSON(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// disambiguate applies the same edition/SRD narrowing internal/cli and
// internal/mcpserver use for get/roll/references, and reports the same two
// failure shapes: nothing matched, or more than one candidate remains and
// the caller must supply a source.
func disambiguate(st *store.Store, ents []store.Entity, source string, opt ToolsOptions, kind, name string) ([]store.Entity, error) {
	if source == "" {
		ents = edition.Filter(ents, func(e store.Entity) string { return e.Source }, opt.Edition)
	}
	if opt.SRD {
		ents = store.SRDOnly(ents)
	}
	if len(ents) == 0 {
		return nil, search.NotFoundError(st, kind, name)
	}
	if len(ents) > 1 {
		parts := make([]string, 0, len(ents))
		for _, e := range ents {
			parts = append(parts, fmt.Sprintf("%s %s (%s)", e.Kind, e.Name, e.Source))
		}
		return nil, fmt.Errorf("ambiguous match; pass source: %s", strings.Join(parts, ", "))
	}
	return ents, nil
}

type toolGetArgs struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Source string `json:"source"`
	Full   bool   `json:"full"`
}

func toolGet(st *store.Store, opt ToolsOptions, raw json.RawMessage) (string, error) {
	var args toolGetArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	result, err := resolveEntity(st, opt, args.Kind, args.Name, args.Source, args.Full)
	if err != nil {
		return "", err
	}
	return toJSON(result)
}

// resolveEntity is toolGet's lookup and rendering logic, factored out so
// toolBuildCharacter can resolve several entities (class, subclass, race,
// subrace, background) the same way in one call instead of the model having
// to issue and remember to fully specify several separate get calls.
func resolveEntity(st *store.Store, opt ToolsOptions, kind, name, source string, full bool) (map[string]any, error) {
	ents, err := st.Lookup(kind, name, source)
	if err != nil {
		return nil, err
	}
	ents, err = disambiguate(st, ents, source, opt, kind, name)
	if err != nil {
		return nil, err
	}
	e := ents[0]
	var body any
	if err := json.Unmarshal(e.JSON, &body); err != nil {
		body = string(e.JSON)
	}
	result := map[string]any{
		"kind":   e.Kind,
		"name":   e.Name,
		"source": e.Source,
		"page":   e.Page,
		"text":   e.Text,
		"json":   body,
		"edges":  e.Edges,
	}
	// statblock is the same clean stat block a person sees from `5e get` —
	// AC/HP/saves/skills/etc already pulled out, traits and actions already
	// stripped of {@tag} markup. Attached alongside the raw json so the
	// model reads exact numbers from prose instead of re-deriving them from
	// 5etools' tagged JSON shape itself.
	if obj, err := statblock.Decode(e.JSON); err == nil {
		if e.Kind == "subrace" {
			obj = mergeSubraceRace(st, obj)
		}
		if text := statblock.RenderString(e.Kind, obj); strings.TrimSpace(text) != "" {
			result["statblock"] = text
		}
		// A class/subclass's own JSON only names its features
		// ("Fighting Style|Fighter|XPHB|1"), not their rules text — that
		// lives in separate classFeature/subclassFeature entities. Resolve
		// and attach it (and, for a class, its subclasses) the same way
		// `5e get` does, so the model reads the actual mechanics instead of
		// a bare feature-name list.
		if e.Kind == "class" || e.Kind == "subclass" {
			if full {
				result["featureDetails"] = classFeatureDetailText(st, e.Kind, obj)
			} else {
				result["featureDetailsNote"] = "Pass full: true to resolve every referenced feature's full rules text; omitted here to keep this response short. Feature names by level are already in statblock. If you are building or describing a character, call get again with full: true instead of answering from names alone."
			}
			if e.Kind == "class" {
				if name, _ := obj["name"].(string); name != "" {
					result["subclasses"] = classSubclassNames(st, name)
				}
			}
		}
		if e.Kind == "race" {
			if rn, _ := obj["name"].(string); rn != "" {
				result["subraces"] = raceSubraceNames(st, rn)
			}
		}
	}
	return result, nil
}

type toolBuildCharacterArgs struct {
	Class      string `json:"class"`
	Subclass   string `json:"subclass"`
	Race       string `json:"race"`
	Subrace    string `json:"subrace"`
	Background string `json:"background"`
	Source     string `json:"source"`
}

// toolBuildCharacter resolves every part of a class/subclass/race-or-subrace
// /background combination in one call via resolveEntity, always with
// full=true for class and subclass — the common failure mode this exists to
// fix is a model given a combo request ("Paladin Aasimar, Sage background")
// making only one or two of the several get calls actually needed, or
// omitting full=true and answering from bare feature names. Any part left
// unresolved (not given, or not found) is reported under "errors" rather
// than failing the whole call, so a request naming just class+race still
// gets both instead of nothing.
func toolBuildCharacter(st *store.Store, opt ToolsOptions, raw json.RawMessage) (string, error) {
	var args toolBuildCharacterArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	result := map[string]any{}
	errs := map[string]string{}
	resolve := func(part, kind, name string, full bool) {
		if name == "" {
			return
		}
		r, err := resolveEntity(st, opt, kind, name, args.Source, full)
		if err != nil {
			errs[part] = err.Error()
			return
		}
		result[part] = r
	}
	resolve("class", "class", args.Class, true)
	resolve("subclass", "subclass", args.Subclass, true)
	resolve("race", "race", args.Race, false)
	resolve("subrace", "subrace", args.Subrace, false)
	resolve("background", "background", args.Background, false)
	if len(errs) > 0 {
		result["errors"] = errs
	}
	return toJSON(result)
}

// classFeatureDetailText renders a class's or subclass's referenced
// features' actual rules text via a store lookup — see writeClassFeatureDetail
// in internal/cli/render.go, which does the same resolution for the CLI's
// human output; both exist because the text lives outside the class's own
// JSON and statblock stays store-agnostic.
func classFeatureDetailText(st *store.Store, kind string, obj map[string]any) string {
	refsKey := "classFeatures"
	if kind == "subclass" {
		refsKey = "subclassFeatures"
	}
	refs := statblock.ClassFeatureRefs(obj[refsKey])
	if len(refs) == 0 {
		return ""
	}
	var b strings.Builder
	statblock.RenderFeatureDetails(&b, refs, func(kind, name, source string) (map[string]any, bool) {
		ents, err := st.Lookup(kind, name, source)
		if err != nil || len(ents) == 0 {
			return nil, false
		}
		obj, err := statblock.Decode(ents[0].JSON)
		if err != nil {
			return nil, false
		}
		return obj, true
	})
	return b.String()
}

// classSubclassNames finds every subclass name for a class the same way
// writeClassFeatureDetail's classSubclasses does — subclass rows carry no
// dedicated store filter for their parent class, so each candidate's own
// className field has to be checked.
func classSubclassNames(st *store.Store, className string) []string {
	names, err := st.FilteredNames(store.NameFilter{Kind: "subclass"})
	if err != nil {
		return nil
	}
	var out []string
	for _, n := range names {
		ents, err := st.Lookup("subclass", n.Name, n.Source)
		if err != nil || len(ents) == 0 {
			continue
		}
		obj, err := statblock.Decode(ents[0].JSON)
		if err != nil {
			continue
		}
		cn, _ := obj["className"].(string)
		if strings.EqualFold(cn, className) {
			out = append(out, fmt.Sprintf("%s (%s)", n.Name, n.Source))
		}
	}
	return out
}

// mergeSubraceRace looks up a subrace's parent race and merges it in via
// statblock.MergeSubrace — see internal/cli/render.go's identical helper,
// which does the same for the CLI's human output; both exist because
// statblock stays store-agnostic.
func mergeSubraceRace(st *store.Store, subraceObj map[string]any) map[string]any {
	raceName, _ := subraceObj["raceName"].(string)
	raceSource, _ := subraceObj["raceSource"].(string)
	if raceName == "" {
		return subraceObj
	}
	ents, err := st.Lookup("race", raceName, raceSource)
	if err != nil || len(ents) == 0 {
		return subraceObj
	}
	raceObj, err := statblock.Decode(ents[0].JSON)
	if err != nil {
		return subraceObj
	}
	return statblock.MergeSubrace(subraceObj, raceObj)
}

// raceSubraceNames finds every subrace name for a race the same way
// classSubclassNames does for a class's subclasses — subrace rows carry no
// dedicated store filter for their parent race, so each candidate's own
// raceName field has to be checked.
func raceSubraceNames(st *store.Store, raceName string) []string {
	names, err := st.FilteredNames(store.NameFilter{Kind: "subrace"})
	if err != nil {
		return nil
	}
	var out []string
	for _, n := range names {
		ents, err := st.Lookup("subrace", n.Name, n.Source)
		if err != nil || len(ents) == 0 {
			continue
		}
		obj, err := statblock.Decode(ents[0].JSON)
		if err != nil {
			continue
		}
		rn, _ := obj["raceName"].(string)
		if strings.EqualFold(rn, raceName) {
			out = append(out, fmt.Sprintf("%s (%s)", n.Name, n.Source))
		}
	}
	return out
}

type toolSearchArgs struct {
	Query   string   `json:"query"`
	Kind    string   `json:"kind"`
	Sources []string `json:"sources"`
	Limit   int      `json:"limit"`
}

func toolSearch(st *store.Store, opt ToolsOptions, raw json.RawMessage) (string, error) {
	var args toolSearchArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	hits, err := search.Search(st, search.Query{
		Text:    args.Query,
		Kind:    args.Kind,
		Sources: args.Sources,
		Limit:   args.Limit,
		Edition: opt.Edition,
		SRD:     opt.SRD,
	})
	if err != nil {
		return "", err
	}
	return toJSON(map[string]any{"hits": hits})
}

type toolEncounterArgs struct {
	Query   string   `json:"query"`
	CR      string   `json:"cr"`
	Type    string   `json:"type"`
	Size    string   `json:"size"`
	Sources []string `json:"sources"`
	Limit   int      `json:"limit"`
}

func toolEncounter(st *store.Store, opt ToolsOptions, raw json.RawMessage) (string, error) {
	var args toolEncounterArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	hits, err := encounter.Search(st, encounter.Query{
		Text:    args.Query,
		CR:      args.CR,
		Type:    args.Type,
		Size:    args.Size,
		Sources: args.Sources,
		Edition: opt.Edition,
		SRD:     opt.SRD,
		Limit:   args.Limit,
	})
	if err != nil {
		return "", err
	}
	return toJSON(map[string]any{"hits": hits})
}

type toolListArgs struct {
	Kind    string   `json:"kind"`
	Query   string   `json:"query"`
	Sources []string `json:"sources"`
	Class   string   `json:"class"`
	Level   *int     `json:"level"`
	Limit   int      `json:"limit"`
}

func toolList(st *store.Store, opt ToolsOptions, raw json.RawMessage) (string, error) {
	var args toolListArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Kind == "" {
		kinds, err := st.Kinds()
		if err != nil {
			return "", err
		}
		return toJSON(map[string]any{"kinds": kinds})
	}
	ents, err := st.FilteredNames(store.NameFilter{Kind: args.Kind, Sources: args.Sources, SRDOnly: opt.SRD})
	if err != nil {
		return "", err
	}
	if len(args.Sources) == 0 {
		ents = edition.Filter(ents, func(e store.Entity) string { return e.Source }, opt.Edition)
	}
	ents = search.FilterByQuery(ents, args.Query)
	if args.Kind == "spell" && (args.Class != "" || args.Level != nil) {
		return listSpells(st, ents, args)
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 100
	}
	total := len(ents)
	if total > limit {
		ents = ents[:limit]
	}
	names := make([]map[string]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, map[string]string{"name": e.Name, "source": e.Source})
	}
	return toJSON(map[string]any{"kind": args.Kind, "total": total, "names": names})
}

// listSpells answers "which spells does <class> get" and "which cantrips can
// it cast" from the spell rows themselves rather than from retrieved prose,
// ordered by level — a flat alphabetical dump of 300 wizard spells is not
// what anyone asking the question wants. It mirrors internal/cli's
// `5e list spell --class` and internal/mcpserver's list tool.
func listSpells(st *store.Store, ents []store.Entity, args toolListArgs) (string, error) {
	rows := search.SpellList(st, ents, args.Class, args.Level)
	limit := args.Limit
	if limit <= 0 {
		// A full class list runs to several hundred spells; the generic
		// 100-name default would silently truncate the answer.
		limit = 600
	}
	total := len(rows)
	if total > limit {
		rows = rows[:limit]
	}
	out := map[string]any{"kind": "spell", "total": total, "spells": rows}
	if args.Class != "" {
		out["class"] = args.Class
	}
	if args.Level != nil {
		out["level"] = *args.Level
	}
	return toJSON(out)
}

type toolRollArgs struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Source string `json:"source"`
	Count  int    `json:"count"`
	Level  int    `json:"level"`
	Seed   *int64 `json:"seed"`
}

func toolRoll(st *store.Store, opt ToolsOptions, raw json.RawMessage) (string, error) {
	var args toolRollArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	kind := args.Kind
	if kind == "" {
		kind = "table"
	}
	ents, err := st.Lookup(kind, args.Name, args.Source)
	if err != nil {
		return "", err
	}
	ents, err = disambiguate(st, ents, args.Source, opt, kind, args.Name)
	if err != nil {
		return "", err
	}
	report, err := randomtable.RollTable(ents[0], randomtable.Query{Count: args.Count, Seed: args.Seed, Level: args.Level})
	if err != nil {
		return "", err
	}
	return toJSON(report)
}

type toolDiceArgs struct {
	Expression string `json:"expression"`
	Count      int    `json:"count"`
	Seed       *int64 `json:"seed"`
}

func toolDice(raw json.RawMessage) (string, error) {
	var args toolDiceArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	count := args.Count
	if count <= 0 {
		count = 1
	}
	rolls := make([]dice.Report, 0, count)
	for i := range count {
		q := dice.Query{}
		if args.Seed != nil {
			s := *args.Seed + int64(i)
			q.Seed = &s
		}
		report, err := dice.Roll(args.Expression, q)
		if err != nil {
			return "", err
		}
		rolls = append(rolls, report)
	}
	return toJSON(map[string]any{"rolls": rolls})
}

type toolReferencesArgs struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Source    string `json:"source"`
	Direction string `json:"direction"`
	Tag       string `json:"tag"`
}

func toolReferences(st *store.Store, opt ToolsOptions, raw json.RawMessage) (string, error) {
	var args toolReferencesArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	ents, err := st.Lookup(args.Kind, args.Name, args.Source)
	if err != nil {
		return "", err
	}
	ents, err = disambiguate(st, ents, args.Source, opt, args.Kind, args.Name)
	if err != nil {
		return "", err
	}
	refs, err := st.References(ents[0].Kind, ents[0].Name, ents[0].Source, args.Direction, args.Tag)
	if err != nil {
		return "", err
	}
	return toJSON(map[string]any{"references": refs})
}

type toolCompareArgs struct {
	Kind    string   `json:"kind"`
	Name    string   `json:"name"`
	Sources []string `json:"sources"`
}

func toolCompare(st *store.Store, opt ToolsOptions, raw json.RawMessage) (string, error) {
	var args toolCompareArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	ents, err := st.Lookup(args.Kind, args.Name, "")
	if err != nil {
		return "", err
	}
	if len(args.Sources) > 0 {
		ents = compare.FilterSources(ents, args.Sources)
	}
	if opt.SRD {
		ents = store.SRDOnly(ents)
	}
	if len(ents) == 0 {
		return "", search.NotFoundError(st, args.Kind, args.Name)
	}
	result, err := compare.Compare(ents)
	if err != nil {
		return "", err
	}
	return toJSON(result)
}

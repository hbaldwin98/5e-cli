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
			Description: "Look up one 5e entity or book section by kind and name. Pass source when several reprints match.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","description":"entity kind such as spell, monster, item, or bookSection"},"name":{"type":"string","description":"entity or section name"},"source":{"type":"string","description":"optional 5etools source id such as PHB"}},"required":["kind","name"]}`),
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
		case "search":
			return toolSearch(st, opt, call.Arguments)
		case "encounter":
			return toolEncounter(st, opt, call.Arguments)
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
func disambiguate(ents []store.Entity, source string, opt ToolsOptions, kind, name string) ([]store.Entity, error) {
	if source == "" {
		ents = edition.Filter(ents, func(e store.Entity) string { return e.Source }, opt.Edition)
	}
	if opt.SRD {
		ents = store.SRDOnly(ents)
	}
	if len(ents) == 0 {
		return nil, fmt.Errorf("no %s named %q", kind, name)
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
}

func toolGet(st *store.Store, opt ToolsOptions, raw json.RawMessage) (string, error) {
	var args toolGetArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	ents, err := st.Lookup(args.Kind, args.Name, args.Source)
	if err != nil {
		return "", err
	}
	ents, err = disambiguate(ents, args.Source, opt, args.Kind, args.Name)
	if err != nil {
		return "", err
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
		if text := statblock.RenderString(e.Kind, obj); strings.TrimSpace(text) != "" {
			result["statblock"] = text
		}
	}
	return toJSON(result)
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
	ents, err = disambiguate(ents, args.Source, opt, kind, args.Name)
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
	ents, err = disambiguate(ents, args.Source, opt, args.Kind, args.Name)
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
		return "", fmt.Errorf("no %s named %q", args.Kind, args.Name)
	}
	result, err := compare.Compare(ents)
	if err != nil {
		return "", err
	}
	return toJSON(result)
}

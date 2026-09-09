package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hbaldwin98/5e-cli/internal/adventure"
	"github.com/hbaldwin98/5e-cli/internal/ask"
	"github.com/hbaldwin98/5e-cli/internal/compare"
	"github.com/hbaldwin98/5e-cli/internal/edition"
	"github.com/hbaldwin98/5e-cli/internal/encounter"
	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/search"
	"github.com/hbaldwin98/5e-cli/internal/store"
	randomtable "github.com/hbaldwin98/5e-cli/internal/table"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options configure the MCP server.
type Options struct {
	Ask     ask.Config
	Edition edition.Pref
	SRD     bool
}

type handler struct {
	st  *store.Store
	ask ask.Config
	ed  edition.Pref
	srd bool
}

type getInput struct {
	Kind   string `json:"kind" jsonschema:"entity kind such as spell, monster, item, or bookSection"`
	Name   string `json:"name" jsonschema:"entity or section name"`
	Source string `json:"source,omitempty" jsonschema:"optional 5etools source id such as PHB"`
}

type getOutput struct {
	Kind   string       `json:"kind"`
	Name   string       `json:"name"`
	Source string       `json:"source"`
	Page   int          `json:"page"`
	Text   string       `json:"text"`
	JSON   any          `json:"json"`
	Edges  []parse.Edge `json:"edges"`
}

// New builds an MCP server over the local 5e index.
func New(st *store.Store, opt Options) *mcp.Server {
	h := &handler{st: st, ask: opt.Ask, ed: opt.Edition, srd: opt.SRD}
	srv := mcp.NewServer(&mcp.Implementation{Name: "5e", Version: "0.1.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get",
		Description: "Look up one 5e entity or book section by kind and name. Pass source when several reprints match. IDs match 5e get.",
	}, h.get)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search",
		Description: "Fuzzy name and full-text search of the rules corpus (entities and book sections). Use adventure_search for module text and NPCs.",
	}, h.search)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "references",
		Description: "Show tagged references to or from one entity. Use get to follow a returned reference.",
	}, h.references)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "semantic_search",
		Description: "Embed the query and return ranked source chunks without calling a chat model. Use get for the full record. Module prose is skipped unless adventure names one.",
	}, h.semanticSearch)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "adventure_search",
		Description: "Search inside one adventure. Kind may be npc, location, or item. npc is every creature mentioned in the module, including MM reprints.",
	}, h.adventureSearch)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "compare",
		Description: "Compare the source-specific versions of one entity and report the top-level fields that differ. Spans editions by default.",
	}, h.compare)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "encounter",
		Description: "Find monsters for an encounter, filtered by challenge rating, creature type, and size.",
	}, h.encounter)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "roll",
		Description: "Roll an indexed random table by name. Pass seed for a reproducible result and source when several books share a table name.",
	}, h.roll)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "adventure_list",
		Description: "List an adventure's chapters, locations, and NPC/item appearances, optionally filtered by role, chapter, or location.",
	}, h.adventureList)
	return srv
}

// Run serves MCP over stdin/stdout until the client disconnects.
func Run(ctx context.Context, st *store.Store, opt Options) error {
	return New(st, opt).Run(ctx, &mcp.StdioTransport{})
}

func (h *handler) get(_ context.Context, _ *mcp.CallToolRequest, in getInput) (*mcp.CallToolResult, getOutput, error) {
	ents, err := h.st.Lookup(in.Kind, in.Name, in.Source)
	if err != nil {
		return nil, getOutput{}, err
	}
	if in.Source == "" {
		ents = edition.Filter(ents, func(e store.Entity) string { return e.Source }, h.ed)
	}
	if h.srd {
		ents = store.SRDOnly(ents)
	}
	if len(ents) == 0 {
		return nil, getOutput{}, fmt.Errorf("no %s named %q", in.Kind, in.Name)
	}
	if len(ents) > 1 {
		return nil, getOutput{}, fmt.Errorf("ambiguous match; pass source: %s", matchList(ents))
	}
	e := ents[0]
	return nil, getOutput{
		Kind:   e.Kind,
		Name:   e.Name,
		Source: e.Source,
		Page:   e.Page,
		Text:   e.Text,
		JSON:   decodeJSON(e.JSON),
		Edges:  e.Edges,
	}, nil
}

func matchList(ents []store.Entity) string {
	parts := make([]string, 0, len(ents))
	for _, e := range ents {
		parts = append(parts, fmt.Sprintf("%s %s (%s)", e.Kind, e.Name, e.Source))
	}
	return strings.Join(parts, ", ")
}

type searchInput struct {
	Query   string   `json:"query" jsonschema:"name or rules text to search"`
	Kind    string   `json:"kind,omitempty" jsonschema:"optional entity kind filter"`
	Sources []string `json:"sources,omitempty" jsonschema:"optional 5etools source ids such as PHB"`
	Limit   int      `json:"limit,omitempty" jsonschema:"maximum hits"`
}

type searchOutput struct {
	Hits []search.Hit `json:"hits"`
}

// semanticSearchInput adds adventure scoping: module prose is excluded by
// default so it cannot ground a rules question, which also puts every
// adventure-only NPC and location out of reach until an adventure is named.
type semanticSearchInput struct {
	Query     string   `json:"query" jsonschema:"name or rules text to search"`
	Kind      string   `json:"kind,omitempty" jsonschema:"optional entity kind filter"`
	Sources   []string `json:"sources,omitempty" jsonschema:"optional 5etools source ids such as PHB"`
	Limit     int      `json:"limit,omitempty" jsonschema:"maximum hits"`
	Adventure string   `json:"adventure,omitempty" jsonschema:"optional adventure id or title; required to reach module prose, NPCs, and locations"`
}

type referencesInput struct {
	Kind      string `json:"kind" jsonschema:"entity kind such as spell, monster, or item"`
	Name      string `json:"name" jsonschema:"entity name"`
	Source    string `json:"source,omitempty" jsonschema:"optional 5etools source id"`
	Direction string `json:"direction,omitempty" jsonschema:"outgoing, incoming, or both"`
	Tag       string `json:"tag,omitempty" jsonschema:"optional tag filter such as spell or creature"`
}

type referencesOutput struct {
	References []store.Reference `json:"references"`
}

func (h *handler) references(_ context.Context, _ *mcp.CallToolRequest, in referencesInput) (*mcp.CallToolResult, referencesOutput, error) {
	ents, err := h.st.Lookup(in.Kind, in.Name, in.Source)
	if err != nil {
		return nil, referencesOutput{}, err
	}
	if in.Source == "" {
		ents = edition.Filter(ents, func(e store.Entity) string { return e.Source }, h.ed)
	}
	if h.srd {
		ents = store.SRDOnly(ents)
	}
	if len(ents) == 0 {
		return nil, referencesOutput{}, fmt.Errorf("no %s named %q", in.Kind, in.Name)
	}
	if len(ents) > 1 {
		return nil, referencesOutput{}, fmt.Errorf("ambiguous match; pass source: %s", matchList(ents))
	}
	refs, err := h.st.References(ents[0].Kind, ents[0].Name, ents[0].Source, in.Direction, in.Tag)
	if err != nil {
		return nil, referencesOutput{}, err
	}
	if refs == nil {
		refs = []store.Reference{}
	}
	return nil, referencesOutput{References: refs}, nil
}

func (h *handler) search(_ context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, searchOutput, error) {
	hits, err := search.Search(h.st, search.Query{
		Text:    in.Query,
		Kind:    in.Kind,
		Sources: in.Sources,
		Limit:   in.Limit,
		Edition: h.ed,
		SRD:     h.srd,
	})
	if err != nil {
		return nil, searchOutput{}, err
	}
	if hits == nil {
		hits = []search.Hit{}
	}
	return nil, searchOutput{Hits: hits}, nil
}

func (h *handler) semanticSearch(ctx context.Context, _ *mcp.CallToolRequest, in semanticSearchInput) (*mcp.CallToolResult, searchOutput, error) {
	q := ask.Query{
		Text:    in.Query,
		Kind:    in.Kind,
		Sources: in.Sources,
		Limit:   in.Limit,
		SRD:     h.srd,
	}
	if in.Adventure != "" {
		adv, err := adventure.Resolve(h.st, in.Adventure)
		if err != nil {
			return nil, searchOutput{}, err
		}
		q.Adventure = adv.Source
	}
	hits, err := ask.Retrieve(ctx, h.st, h.ask, q)
	if err != nil {
		return nil, searchOutput{}, err
	}
	out := make([]search.Hit, 0, len(hits))
	for _, hit := range hits {
		out = append(out, search.Hit{
			Kind:    hit.Kind,
			Name:    hit.Name,
			Source:  hit.Source,
			Score:   hit.Score,
			Snippet: hit.Snippet,
		})
	}
	return nil, searchOutput{Hits: out}, nil
}

type adventureSearchInput struct {
	Adventure string `json:"adventure" jsonschema:"adventure id or catalog title such as LMoP"`
	Query     string `json:"query" jsonschema:"name or text to search inside the adventure"`
	Kind      string `json:"kind,omitempty" jsonschema:"optional role filter: npc, location, or item"`
	Limit     int    `json:"limit,omitempty" jsonschema:"maximum hits"`
}

func (h *handler) adventureSearch(_ context.Context, _ *mcp.CallToolRequest, in adventureSearchInput) (*mcp.CallToolResult, searchOutput, error) {
	adv, err := adventure.Resolve(h.st, in.Adventure)
	if err != nil {
		return nil, searchOutput{}, err
	}
	hits, err := search.Search(h.st, search.Query{
		Text:      in.Query,
		Kind:      adventure.SearchKind(in.Kind),
		Limit:     in.Limit,
		Edition:   edition.All,
		SRD:       h.srd,
		Adventure: adv.Source,
	})
	if err != nil {
		return nil, searchOutput{}, err
	}
	if hits == nil {
		hits = []search.Hit{}
	}
	return nil, searchOutput{Hits: hits}, nil
}

type adventureListInput struct {
	Adventure string `json:"adventure" jsonschema:"adventure id or catalog title such as LMoP"`
	Kind      string `json:"kind,omitempty" jsonschema:"optional role: npc, location, item, or all"`
	Chapter   string `json:"chapter,omitempty" jsonschema:"optional chapter name"`
	Location  string `json:"location,omitempty" jsonschema:"optional location name"`
}

func (h *handler) adventureList(_ context.Context, _ *mcp.CallToolRequest, in adventureListInput) (*mcp.CallToolResult, adventure.Report, error) {
	adv, err := adventure.Resolve(h.st, in.Adventure)
	if err != nil {
		return nil, adventure.Report{}, err
	}
	report, err := adventure.List(h.st, adv.Source, in.Kind, in.Chapter, in.Location)
	if err != nil {
		return nil, adventure.Report{}, err
	}
	return nil, report, nil
}

type compareInput struct {
	Kind    string   `json:"kind" jsonschema:"entity kind such as spell, monster, or item"`
	Name    string   `json:"name" jsonschema:"entity name"`
	Sources []string `json:"sources,omitempty" jsonschema:"optional 5etools source ids to restrict the comparison to"`
}

// compareOutput mirrors compare.Result with the record payload typed as any.
// compare.Record.JSON is a json.RawMessage, which the MCP schema generator
// reads as a byte array and then rejects when it marshals as an object.
type compareOutput struct {
	Kind        string               `json:"kind"`
	Name        string               `json:"name"`
	Records     []compareRecord      `json:"records"`
	Differences []compare.Difference `json:"differences"`
}

type compareRecord struct {
	Kind   string       `json:"kind"`
	Name   string       `json:"name"`
	Source string       `json:"source"`
	Page   int          `json:"page"`
	SRD    bool         `json:"srd"`
	Text   string       `json:"text"`
	JSON   any          `json:"json"`
	Edges  []parse.Edge `json:"edges"`
}

// compare spans editions unless the caller narrows it with sources: comparing
// a 2014 record against its 2024 reprint is the common case.
func (h *handler) compare(_ context.Context, _ *mcp.CallToolRequest, in compareInput) (*mcp.CallToolResult, compareOutput, error) {
	ents, err := h.st.Lookup(in.Kind, in.Name, "")
	if err != nil {
		return nil, compareOutput{}, err
	}
	if len(in.Sources) > 0 {
		ents = compare.FilterSources(ents, in.Sources)
	}
	if h.srd {
		ents = store.SRDOnly(ents)
	}
	if len(ents) == 0 {
		return nil, compareOutput{}, fmt.Errorf("no %s named %q", in.Kind, in.Name)
	}
	result, err := compare.Compare(ents)
	if err != nil {
		return nil, compareOutput{}, err
	}
	out := compareOutput{Kind: result.Kind, Name: result.Name, Differences: result.Differences}
	for _, record := range result.Records {
		out.Records = append(out.Records, compareRecord{
			Kind:   record.Kind,
			Name:   record.Name,
			Source: record.Source,
			Page:   record.Page,
			SRD:    record.SRD,
			Text:   record.Text,
			JSON:   decodeJSON(record.JSON),
			Edges:  record.Edges,
		})
	}
	return nil, out, nil
}

// decodeJSON turns a stored payload into a plain value so MCP output schemas
// describe it as an object rather than a byte array.
func decodeJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return v
}

type encounterInput struct {
	Query   string   `json:"query" jsonschema:"monster name or text to match"`
	CR      string   `json:"cr,omitempty" jsonschema:"optional challenge rating such as 1/4 or 5"`
	Type    string   `json:"type,omitempty" jsonschema:"optional creature type such as humanoid or fey"`
	Size    string   `json:"size,omitempty" jsonschema:"optional creature size such as small or large"`
	Sources []string `json:"sources,omitempty" jsonschema:"optional 5etools source ids"`
	Limit   int      `json:"limit,omitempty" jsonschema:"maximum hits"`
}

type encounterOutput struct {
	Hits []encounter.Hit `json:"hits"`
}

func (h *handler) encounter(_ context.Context, _ *mcp.CallToolRequest, in encounterInput) (*mcp.CallToolResult, encounterOutput, error) {
	hits, err := encounter.Search(h.st, encounter.Query{
		Text:    in.Query,
		CR:      in.CR,
		Type:    in.Type,
		Size:    in.Size,
		Sources: in.Sources,
		Edition: h.ed,
		SRD:     h.srd,
		Limit:   in.Limit,
	})
	if err != nil {
		return nil, encounterOutput{}, err
	}
	if hits == nil {
		hits = []encounter.Hit{}
	}
	return nil, encounterOutput{Hits: hits}, nil
}

type rollInput struct {
	Name   string `json:"name" jsonschema:"table name such as Wild Magic Surge"`
	Source string `json:"source,omitempty" jsonschema:"optional 5etools source id"`
	Count  int    `json:"count,omitempty" jsonschema:"number of rows to roll, default 1"`
	Seed   *int64 `json:"seed,omitempty" jsonschema:"optional seed for a reproducible roll"`
}

func (h *handler) roll(_ context.Context, _ *mcp.CallToolRequest, in rollInput) (*mcp.CallToolResult, randomtable.Report, error) {
	ents, err := h.st.Lookup("table", in.Name, in.Source)
	if err != nil {
		return nil, randomtable.Report{}, err
	}
	if in.Source == "" {
		ents = edition.Filter(ents, func(e store.Entity) string { return e.Source }, h.ed)
	}
	if h.srd {
		ents = store.SRDOnly(ents)
	}
	if len(ents) == 0 {
		return nil, randomtable.Report{}, fmt.Errorf("no table named %q", in.Name)
	}
	if len(ents) > 1 {
		return nil, randomtable.Report{}, fmt.Errorf("ambiguous match; pass source: %s", matchList(ents))
	}
	report, err := randomtable.RollTable(ents[0], randomtable.Query{Count: in.Count, Seed: in.Seed})
	if err != nil {
		return nil, randomtable.Report{}, err
	}
	return nil, report, nil
}

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hbaldwin98/5e-cli/internal/adventure"
	"github.com/hbaldwin98/5e-cli/internal/ask"
	"github.com/hbaldwin98/5e-cli/internal/edition"
	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/search"
	"github.com/hbaldwin98/5e-cli/internal/store"
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
		Name:        "semantic_search",
		Description: "Embed the query and return ranked source chunks without calling a chat model. Use get for the full record. Skips adventure module text.",
	}, h.semanticSearch)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "adventure_search",
		Description: "Search inside one adventure. Kind may be npc, location, or item. npc is every creature mentioned in the module, including MM reprints.",
	}, h.adventureSearch)
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
	var raw any
	if len(e.JSON) > 0 {
		if err := json.Unmarshal(e.JSON, &raw); err != nil {
			raw = json.RawMessage(e.JSON)
		}
	}
	return nil, getOutput{
		Kind:   e.Kind,
		Name:   e.Name,
		Source: e.Source,
		Page:   e.Page,
		Text:   e.Text,
		JSON:   raw,
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

func (h *handler) semanticSearch(ctx context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, searchOutput, error) {
	hits, err := ask.Retrieve(ctx, h.st, h.ask, ask.Query{
		Text:    in.Query,
		Kind:    in.Kind,
		Sources: in.Sources,
		Limit:   in.Limit,
		SRD:     h.srd,
	})
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

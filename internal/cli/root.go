package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hbaldwin98/5e-cli/internal/adventure"
	"github.com/hbaldwin98/5e-cli/internal/ask"
	"github.com/hbaldwin98/5e-cli/internal/compare"
	"github.com/hbaldwin98/5e-cli/internal/edition"
	"github.com/hbaldwin98/5e-cli/internal/encounter"
	"github.com/hbaldwin98/5e-cli/internal/ingest"
	"github.com/hbaldwin98/5e-cli/internal/mcpserver"
	"github.com/hbaldwin98/5e-cli/internal/paths"
	"github.com/hbaldwin98/5e-cli/internal/search"
	"github.com/hbaldwin98/5e-cli/internal/store"
	"github.com/spf13/cobra"
)

type options struct {
	JSON    bool
	Data    string
	Index   string
	Edition string
	SRD     bool
}

func rootCmd() *cobra.Command {
	opt := &options{}
	cmd := &cobra.Command{
		Use:           "5e",
		Short:         "Look up D&D 5e data from a local 5etools JSON tree",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.PersistentFlags().BoolVar(&opt.JSON, "json", false, "machine-readable JSON output")
	cmd.PersistentFlags().StringVar(&opt.Data, "data", "", "path to 5etools data/ directory")
	cmd.PersistentFlags().StringVar(&opt.Index, "index", "", "path to sqlite index")
	cmd.PersistentFlags().StringVar(&opt.Edition, "edition", "", "2014, 2024, or all (default 2024, or FIVE_E_EDITION)")
	cmd.PersistentFlags().BoolVar(&opt.SRD, "srd", false, "restrict to SRD / basic rules entities")
	cmd.AddCommand(ingestCmd(opt), doctorCmd(opt), getCmd(opt), searchCmd(opt), compareCmd(opt), encounterCmd(opt), refsCmd(opt), askCmd(opt), adventureCmd(opt), mcpCmd(opt))
	return cmd
}

// Execute runs the CLI.
func Execute() error {
	return rootCmd().Execute()
}

func resolve(opt *options) (data, index string, err error) {
	data = opt.Data
	if data == "" {
		data = paths.DefaultDataDir()
	}
	index = opt.Index
	if index == "" {
		index, err = paths.DefaultIndex()
		if err != nil {
			return "", "", err
		}
	}
	return data, index, nil
}

func (opt *options) editionPref() (edition.Pref, error) {
	s := opt.Edition
	if s == "" {
		s = os.Getenv("FIVE_E_EDITION")
	}
	return edition.Parse(s)
}

func ingestCmd(opt *options) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "ingest",
		Short: "Parse 5etools JSON into a local sqlite index",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			data, index, err := resolve(opt)
			if err != nil {
				return err
			}
			res, err := ingest.Run(ingest.Options{DataDir: data, Index: index, Force: force})
			if err != nil {
				return err
			}
			if opt.JSON {
				return writeJSON(cmd.OutOrStdout(), res)
			}
			if res.Skipped {
				fmt.Fprintf(cmd.OutOrStdout(), "index already current (%s)\n", res.SHA)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "indexed %d entities, %d sections → %s\n", res.Entities, res.Documents, res.Index)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "rebuild even if the data fingerprint matches")
	return cmd
}

func doctorCmd(opt *options) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check local data and index setup",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, index, err := resolve(opt)
			if err != nil {
				return err
			}
			report := paths.Inspect(data, index)
			if opt.JSON {
				if err := writeJSON(cmd.OutOrStdout(), report); err != nil {
					return err
				}
			} else if err := writeDoctorReport(cmd.OutOrStdout(), report); err != nil {
				return err
			}
			if !report.Ready {
				return errExit{code: 1, msg: report.Issue}
			}
			return nil
		},
	}
}

func getCmd(opt *options) *cobra.Command {
	var source string
	cmd := &cobra.Command{
		Use:   "get <kind> <name>",
		Short: "Look up one entity by kind and name",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, index, err := resolve(opt)
			if err != nil {
				return err
			}
			st, err := paths.OpenIndex(index, data)
			if err != nil {
				return err
			}
			defer st.Close()
			kind, name := args[0], strings.Join(args[1:], " ")
			ents, err := st.Lookup(kind, name, source)
			if err != nil {
				return err
			}
			ed, err := opt.editionPref()
			if err != nil {
				return err
			}
			if source == "" {
				ents = edition.Filter(ents, func(e store.Entity) string { return e.Source }, ed)
			}
			if opt.SRD {
				ents = store.SRDOnly(ents)
			}
			if len(ents) == 0 {
				return fmt.Errorf("no %s named %q", kind, name)
			}
			if len(ents) > 1 {
				return writeAmbiguous(cmd, opt.JSON, ents)
			}
			return writeEntity(cmd, opt.JSON, ents[0])
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "disambiguate by 5etools source id (PHB, XPHB, MM, …)")
	return cmd
}

func searchCmd(opt *options) *cobra.Command {
	var kind string
	var sources []string
	var limit int
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Fuzzy name and full-text search",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, index, err := resolve(opt)
			if err != nil {
				return err
			}
			st, err := paths.OpenIndex(index, data)
			if err != nil {
				return err
			}
			defer st.Close()
			ed, err := opt.editionPref()
			if err != nil {
				return err
			}
			hits, err := search.Search(st, search.Query{
				Text:    strings.Join(args, " "),
				Kind:    kind,
				Sources: splitSources(sources),
				Limit:   limit,
				Edition: ed,
				SRD:     opt.SRD,
			})
			if err != nil {
				return err
			}
			if opt.JSON {
				return writeJSON(cmd.OutOrStdout(), hits)
			}
			if len(hits) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no matches")
				return nil
			}
			return writeSearchResults(cmd.OutOrStdout(), hits)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "restrict to one entity kind")
	cmd.Flags().StringSliceVar(&sources, "source", nil, "restrict to source ids")
	cmd.Flags().IntVar(&limit, "limit", 10, "maximum hits")
	return cmd
}

func compareCmd(opt *options) *cobra.Command {
	var sources []string
	cmd := &cobra.Command{
		Use:   "compare <kind> <name>",
		Short: "Compare source-specific versions of one entity",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, index, err := resolve(opt)
			if err != nil {
				return err
			}
			st, err := paths.OpenIndex(index, data)
			if err != nil {
				return err
			}
			defer st.Close()
			kind, name := args[0], strings.Join(args[1:], " ")
			ents, err := st.Lookup(kind, name, "")
			if err != nil {
				return err
			}
			if wanted := splitSources(sources); len(wanted) > 0 {
				ents = filterSources(ents, wanted)
			}
			if opt.SRD {
				ents = store.SRDOnly(ents)
			}
			ed, err := comparisonEdition(opt)
			if err != nil {
				return err
			}
			if ed != edition.All {
				ents = edition.Filter(ents, func(e store.Entity) string { return e.Source }, ed)
			}
			if len(ents) == 0 {
				return fmt.Errorf("no %s named %q", kind, name)
			}
			result, err := compare.Compare(ents)
			if err != nil {
				return err
			}
			if opt.JSON {
				return writeJSON(cmd.OutOrStdout(), result)
			}
			return writeComparison(cmd.OutOrStdout(), result)
		},
	}
	cmd.Flags().StringSliceVar(&sources, "source", nil, "restrict to source ids (repeatable or comma-separated)")
	return cmd
}

func encounterCmd(opt *options) *cobra.Command {
	var cr, creatureType, size string
	var sources []string
	var limit int
	cmd := &cobra.Command{
		Use:   "encounter <query>",
		Short: "Find monsters for an encounter",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, index, err := resolve(opt)
			if err != nil {
				return err
			}
			st, err := paths.OpenIndex(index, data)
			if err != nil {
				return err
			}
			defer st.Close()
			ed, err := opt.editionPref()
			if err != nil {
				return err
			}
			hits, err := encounter.Search(st, encounter.Query{
				Text:    strings.Join(args, " "),
				CR:      cr,
				Type:    creatureType,
				Size:    size,
				Sources: splitSources(sources),
				Edition: ed,
				SRD:     opt.SRD,
				Limit:   limit,
			})
			if err != nil {
				return err
			}
			if opt.JSON {
				return writeJSON(cmd.OutOrStdout(), hits)
			}
			return writeEncounterResults(cmd.OutOrStdout(), hits)
		},
	}
	cmd.Flags().StringVar(&cr, "cr", "", "restrict to challenge rating")
	cmd.Flags().StringVar(&creatureType, "type", "", "restrict to creature type")
	cmd.Flags().StringVar(&size, "size", "", "restrict to creature size")
	cmd.Flags().StringSliceVar(&sources, "source", nil, "restrict to source ids")
	cmd.Flags().IntVar(&limit, "limit", 10, "maximum hits")
	return cmd
}

func comparisonEdition(opt *options) (edition.Pref, error) {
	if opt.Edition == "" && os.Getenv("FIVE_E_EDITION") == "" {
		return edition.All, nil
	}
	return opt.editionPref()
}

func filterSources(entities []store.Entity, sources []string) []store.Entity {
	wanted := make(map[string]bool, len(sources))
	for _, source := range sources {
		wanted[strings.ToLower(source)] = true
	}
	out := make([]store.Entity, 0, len(entities))
	for _, entity := range entities {
		if wanted[strings.ToLower(entity.Source)] {
			out = append(out, entity)
		}
	}
	return out
}

func refsCmd(opt *options) *cobra.Command {
	var source, direction, tag string
	cmd := &cobra.Command{
		Use:   "refs <kind> <name>",
		Short: "Show references to or from an entity",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, index, err := resolve(opt)
			if err != nil {
				return err
			}
			st, err := paths.OpenIndex(index, data)
			if err != nil {
				return err
			}
			defer st.Close()
			kind, name := args[0], strings.Join(args[1:], " ")
			ents, err := st.Lookup(kind, name, source)
			if err != nil {
				return err
			}
			ed, err := opt.editionPref()
			if err != nil {
				return err
			}
			if source == "" {
				ents = edition.Filter(ents, func(e store.Entity) string { return e.Source }, ed)
			}
			if opt.SRD {
				ents = store.SRDOnly(ents)
			}
			if len(ents) == 0 {
				return fmt.Errorf("no %s named %q", kind, name)
			}
			if len(ents) > 1 {
				return writeAmbiguous(cmd, opt.JSON, ents)
			}
			refs, err := st.References(ents[0].Kind, ents[0].Name, ents[0].Source, direction, tag)
			if err != nil {
				return err
			}
			if opt.JSON {
				return writeJSON(cmd.OutOrStdout(), refs)
			}
			return writeReferences(cmd.OutOrStdout(), refs)
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "disambiguate by 5etools source id")
	cmd.Flags().StringVar(&direction, "direction", "both", "outgoing, incoming, or both")
	cmd.Flags().StringVar(&tag, "tag", "", "restrict to a tag such as spell or creature")
	return cmd
}

func askCmd(opt *options) *cobra.Command {
	var retrieveOnly bool
	var kind string
	var sources []string
	var limit int
	cmd := &cobra.Command{
		Use:   "ask <query>",
		Short: "Answer a question from embedded 5e sources",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAsk(cmd, opt, args, retrieveOnly, kind, splitSources(sources), limit)
		},
	}
	cmd.Flags().BoolVar(&retrieveOnly, "retrieve-only", false, "return ranked chunks without calling a chat model")
	cmd.Flags().StringVar(&kind, "kind", "", "restrict to one entity kind")
	cmd.Flags().StringSliceVar(&sources, "source", nil, "restrict to source ids")
	cmd.Flags().IntVar(&limit, "limit", 8, "maximum retrieved chunks")
	return cmd
}

func runAsk(cmd *cobra.Command, opt *options, args []string, retrieveOnly bool, kind string, sources []string, limit int) error {
	data, index, err := resolve(opt)
	if err != nil {
		return err
	}
	st, err := paths.OpenIndex(index, data)
	if err != nil {
		return err
	}
	defer st.Close()
	cfg := ask.ConfigFromEnv()
	cfg.CachePath = paths.EmbeddingsForIndex(index)
	cfg.Progress = cmd.ErrOrStderr()
	q := ask.Query{
		Text:    strings.Join(args, " "),
		Kind:    kind,
		Sources: sources,
		Limit:   limit,
		SRD:     opt.SRD,
	}
	if retrieveOnly {
		return writeRetrieve(cmd, opt.JSON, st, cfg, q)
	}
	return writeAskResult(cmd, opt.JSON, st, cfg, q)
}

func writeRetrieve(cmd *cobra.Command, asJSON bool, st *store.Store, cfg ask.Config, q ask.Query) error {
	hits, err := ask.Retrieve(cmd.Context(), st, cfg, q)
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSON(cmd.OutOrStdout(), hits)
	}
	return writeAskHits(cmd.OutOrStdout(), hits)
}

func writeAskResult(cmd *cobra.Command, asJSON bool, st *store.Store, cfg ask.Config, q ask.Query) error {
	res, err := ask.Ask(cmd.Context(), st, cfg, q)
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSON(cmd.OutOrStdout(), res)
	}
	fmt.Fprintln(cmd.OutOrStdout(), res.Answer)
	if len(res.Citations) > 0 {
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), "Sources:")
		return writeAskHits(cmd.OutOrStdout(), res.Citations)
	}
	return nil
}

func adventureCmd(opt *options) *cobra.Command {
	var kind, chapter, location string
	var limit int
	cmd := &cobra.Command{
		Use:   "adventure <id-or-name> <search|get> ...",
		Short: "Search and look up inside one adventure",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAdventure(cmd, opt, args, kind, chapter, location, limit)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "npc, location, item, or a store kind")
	cmd.Flags().StringVar(&chapter, "chapter", "", "restrict a list to one chapter")
	cmd.Flags().StringVar(&location, "location", "", "restrict a list to one location")
	cmd.Flags().IntVar(&limit, "limit", 10, "maximum search hits")
	return cmd
}

func runAdventure(cmd *cobra.Command, opt *options, args []string, kind, chapter, location string, limit int) error {
	data, index, err := resolve(opt)
	if err != nil {
		return err
	}
	st, err := paths.OpenIndex(index, data)
	if err != nil {
		return err
	}
	defer st.Close()
	adv, err := adventure.Resolve(st, args[0])
	if err != nil {
		return err
	}
	return dispatchAdventure(cmd, opt, st, adv, args, kind, chapter, location, limit)
}

func dispatchAdventure(cmd *cobra.Command, opt *options, st *store.Store, adv store.Entity, args []string, kind, chapter, location string, limit int) error {
	switch args[1] {
	case "list":
		return runAdventureList(cmd, opt, st, adv, kind, chapter, location)
	case "search":
		return runAdventureSearch(cmd, opt, st, adv, strings.Join(args[2:], " "), kind, limit)
	case "get":
		return runAdventureGet(cmd, opt, st, adv, args[2:])
	default:
		return fmt.Errorf("adventure expected search or get, got %q", args[1])
	}
}

func runAdventureList(cmd *cobra.Command, opt *options, st *store.Store, adv store.Entity, kind, chapter, location string) error {
	report, err := adventure.List(st, adv.Source, kind, chapter, location)
	if err != nil {
		return err
	}
	if opt.JSON {
		return writeJSON(cmd.OutOrStdout(), report)
	}
	return writeAdventureReport(cmd.OutOrStdout(), report)
}

func runAdventureSearch(cmd *cobra.Command, opt *options, st *store.Store, adv store.Entity, query, kind string, limit int) error {
	if strings.TrimSpace(query) == "" {
		return fmt.Errorf("adventure search requires a query")
	}
	hits, err := search.Search(st, search.Query{
		Text:      query,
		Kind:      adventure.SearchKind(kind),
		Limit:     limit,
		Edition:   edition.All,
		SRD:       opt.SRD,
		Adventure: adv.Source,
	})
	if err != nil {
		return err
	}
	if opt.JSON {
		return writeJSON(cmd.OutOrStdout(), hits)
	}
	if len(hits) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no matches")
		return nil
	}
	return writeSearchResults(cmd.OutOrStdout(), hits)
}

func runAdventureGet(cmd *cobra.Command, opt *options, st *store.Store, adv store.Entity, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("adventure get <role> <name>")
	}
	name := strings.Join(args[1:], " ")
	ents, err := adventure.Lookup(st, args[0], name, adv.Source)
	if err != nil {
		return err
	}
	if opt.SRD {
		ents = store.SRDOnly(ents)
	}
	if len(ents) == 0 {
		return fmt.Errorf("no %s named %q in %s", args[0], name, adv.Source)
	}
	if len(ents) > 1 {
		return writeAmbiguous(cmd, opt.JSON, ents)
	}
	return writeEntity(cmd, opt.JSON, ents[0])
}

func mcpCmd(opt *options) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run an MCP stdio server over the local index",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runMCP(cmd.Context(), opt, cmd.ErrOrStderr())
		},
	}
}

func runMCP(ctx context.Context, opt *options, errw io.Writer) error {
	data, index, err := resolve(opt)
	if err != nil {
		return err
	}
	st, err := paths.OpenIndex(index, data)
	if err != nil {
		return err
	}
	defer st.Close()
	cfg := ask.ConfigFromEnv()
	cfg.CachePath = paths.EmbeddingsForIndex(index)
	cfg.Progress = errw
	ed, err := opt.editionPref()
	if err != nil {
		return err
	}
	return mcpserver.Run(ctx, st, mcpserver.Options{Ask: cfg, Edition: ed, SRD: opt.SRD})
}

func writeAskHits(w io.Writer, hits []ask.Hit) error {
	if len(hits) == 0 {
		fmt.Fprintln(w, "no matches")
		return nil
	}
	for _, h := range hits {
		fmt.Fprintf(w, "- %s  %s  (%s)\n", h.Kind, h.Name, h.Source)
	}
	return nil
}

func writeReferences(w io.Writer, refs []store.Reference) error {
	if len(refs) == 0 {
		fmt.Fprintln(w, "no references")
		return nil
	}
	var markdown bytes.Buffer
	fmt.Fprintln(&markdown, "| Direction | Tag | From | To |")
	fmt.Fprintln(&markdown, "| --- | --- | --- | --- |")
	for _, ref := range refs {
		from := fmt.Sprintf("%s %s (%s)", ref.From.Kind, ref.From.Name, ref.From.Source)
		to := fmt.Sprintf("%s %s (%s)", ref.To.Kind, ref.To.Name, ref.To.Source)
		fmt.Fprintf(&markdown, "| %s | %s | %s | %s |\n", markdownCell(ref.Direction), markdownCell(ref.Tag), markdownCell(from), markdownCell(to))
	}
	return renderMarkdown(w, markdown.String())
}

func writeDoctorReport(w io.Writer, report paths.Inspection) error {
	dataStatus := "missing"
	if report.DataExists {
		dataStatus = "ready"
	}
	indexStatus := "missing"
	if report.IndexExists {
		indexStatus = "found"
	}
	status := "not ready"
	if report.Ready {
		status = "ready"
	} else if report.DataExists && report.IndexExists {
		status = "stale or invalid"
	}
	fmt.Fprintf(w, "Data:  %s [%s]\n", report.DataPath, dataStatus)
	fmt.Fprintf(w, "Index: %s [%s]\n", report.IndexPath, indexStatus)
	if report.DataFingerprint != "" {
		fmt.Fprintf(w, "Data fingerprint:  %s\n", shortFingerprint(report.DataFingerprint))
	}
	if report.IndexFingerprint != "" {
		fmt.Fprintf(w, "Index fingerprint: %s\n", shortFingerprint(report.IndexFingerprint))
	}
	fmt.Fprintf(w, "Status: %s\n", status)
	if report.Issue != "" {
		fmt.Fprintf(w, "Issue:  %s\n", report.Issue)
	}
	return nil
}

func shortFingerprint(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func writeAdventureReport(w io.Writer, report adventure.Report) error {
	var markdown bytes.Buffer
	writeSections := func(heading string, sections []adventure.Section) {
		if len(sections) == 0 {
			return
		}
		fmt.Fprintf(&markdown, "## %s\n\n", heading)
		fmt.Fprintln(&markdown, "| Name | Source |")
		fmt.Fprintln(&markdown, "| --- | --- |")
		for _, section := range sections {
			fmt.Fprintf(&markdown, "| %s | %s |\n", markdownCell(section.Name), markdownCell(section.Source))
		}
		fmt.Fprintln(&markdown)
	}
	writeSections("Chapters", report.Chapters)
	writeSections("Locations", report.Locations)
	if len(report.Appearances) > 0 {
		fmt.Fprintln(&markdown, "## Appearances")
		fmt.Fprintln(&markdown)
		fmt.Fprintln(&markdown, "| Role | Name | Source | Chapter | Location |")
		fmt.Fprintln(&markdown, "| --- | --- | --- | --- | --- |")
		for _, appearance := range report.Appearances {
			fmt.Fprintf(&markdown, "| %s | %s | %s | %s | %s |\n",
				markdownCell(appearance.Role), markdownCell(appearance.Name), markdownCell(appearance.Source),
				markdownCell(appearance.Chapter), markdownCell(appearance.Location))
		}
	}
	if markdown.Len() == 0 {
		fmt.Fprintln(w, "no matches")
		return nil
	}
	return renderMarkdown(w, markdown.String())
}

func writeComparison(w io.Writer, result compare.Result) error {
	var markdown bytes.Buffer
	fmt.Fprintf(&markdown, "# Compare %s %s\n\n", result.Kind, result.Name)
	fmt.Fprint(&markdown, "*Sources: ")
	for i, record := range result.Records {
		if i > 0 {
			fmt.Fprint(&markdown, ", ")
		}
		fmt.Fprint(&markdown, markdownCell(record.Source))
	}
	fmt.Fprint(&markdown, "*\n\n")
	if len(result.Differences) == 0 {
		fmt.Fprintln(&markdown, "No top-level differences.")
		return renderMarkdown(w, markdown.String())
	}
	fmt.Fprint(&markdown, "| Field |")
	for _, record := range result.Records {
		fmt.Fprintf(&markdown, " %s |", markdownCell(record.Source))
	}
	fmt.Fprintln(&markdown)
	fmt.Fprint(&markdown, "| --- |")
	for range result.Records {
		fmt.Fprint(&markdown, " --- |")
	}
	fmt.Fprintln(&markdown)
	for _, difference := range result.Differences {
		values := make(map[string]compare.Value, len(difference.Values))
		for _, value := range difference.Values {
			values[strings.ToLower(value.Source)] = value
		}
		fmt.Fprintf(&markdown, "| %s |", markdownCell(difference.Field))
		for _, record := range result.Records {
			value := values[strings.ToLower(record.Source)]
			fmt.Fprintf(&markdown, " %s |", markdownCell(comparisonValue(value)))
		}
		fmt.Fprintln(&markdown)
	}
	return renderMarkdown(w, markdown.String())
}

func writeEncounterResults(w io.Writer, hits []encounter.Hit) error {
	if len(hits) == 0 {
		fmt.Fprintln(w, "no encounter matches")
		return nil
	}
	var markdown bytes.Buffer
	fmt.Fprintln(&markdown, "| Name | CR | Type | Size | Source | Match |")
	fmt.Fprintln(&markdown, "| --- | --- | --- | --- | --- | ---: |")
	for _, hit := range hits {
		fmt.Fprintf(&markdown, "| %s | %s | %s | %s | %s | %.2f |\n",
			markdownCell(hit.Name), markdownCell(hit.CR), markdownCell(hit.Type), markdownCell(hit.Size), markdownCell(hit.Source), hit.Score)
	}
	return renderMarkdown(w, markdown.String())
}

func comparisonValue(value compare.Value) string {
	if !value.Present {
		return "(missing)"
	}
	if value.Value == nil {
		return "null"
	}
	raw, err := json.Marshal(value.Value)
	if err != nil {
		return fmt.Sprint(value.Value)
	}
	return string(raw)
}

func splitSources(in []string) []string {
	var out []string
	for _, s := range in {
		for _, p := range strings.Split(s, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func writeEntity(cmd *cobra.Command, asJSON bool, e store.Entity) error {
	if asJSON {
		out := map[string]any{
			"kind":   e.Kind,
			"name":   e.Name,
			"source": e.Source,
			"page":   e.Page,
			"text":   e.Text,
			"json":   e.JSON,
			"edges":  e.Edges,
		}
		return writeJSON(cmd.OutOrStdout(), out)
	}
	return writeHumanEntity(cmd.OutOrStdout(), e)
}

func writeAmbiguous(cmd *cobra.Command, asJSON bool, ents []store.Entity) error {
	type hit struct {
		Kind   string `json:"kind"`
		Name   string `json:"name"`
		Source string `json:"source"`
	}
	var hits []hit
	for _, e := range ents {
		hits = append(hits, hit{Kind: e.Kind, Name: e.Name, Source: e.Source})
	}
	if asJSON {
		_ = writeJSON(cmd.OutOrStdout(), map[string]any{"error": "ambiguous", "matches": hits})
	} else {
		fmt.Fprintf(cmd.ErrOrStderr(), "multiple matches; pass --source:\n")
		for _, e := range ents {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s  %s  (%s)\n", e.Kind, e.Name, e.Source)
		}
	}
	return errExit{code: 2, msg: "ambiguous match"}
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

type errExit struct {
	code int
	msg  string
}

func (e errExit) Error() string { return e.msg }
func (e errExit) ExitCode() int { return e.code }

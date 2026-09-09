package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"

	"github.com/hbaldwin98/5e-cli/internal/adventure"
	"github.com/hbaldwin98/5e-cli/internal/ask"
	"github.com/hbaldwin98/5e-cli/internal/compare"
	"github.com/hbaldwin98/5e-cli/internal/dice"
	"github.com/hbaldwin98/5e-cli/internal/edition"
	"github.com/hbaldwin98/5e-cli/internal/encounter"
	"github.com/hbaldwin98/5e-cli/internal/ingest"
	"github.com/hbaldwin98/5e-cli/internal/mcpserver"
	"github.com/hbaldwin98/5e-cli/internal/paths"
	"github.com/hbaldwin98/5e-cli/internal/search"
	"github.com/hbaldwin98/5e-cli/internal/statblock"
	"github.com/hbaldwin98/5e-cli/internal/store"
	randomtable "github.com/hbaldwin98/5e-cli/internal/table"
	"github.com/spf13/cobra"
)

type options struct {
	JSON     bool
	Data     string
	Index    string
	Edition  string
	SRD      bool
	Provider string
	Model    string
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
	cmd.PersistentFlags().StringVar(&opt.Provider, "provider", "", "use this configured provider (see `5e auth list`) instead of the active one")
	cmd.PersistentFlags().StringVar(&opt.Model, "model", "", "override the chat model for this run")
	cmd.AddCommand(ingestCmd(opt), doctorCmd(opt), getCmd(opt), searchCmd(opt), compareCmd(opt), encounterCmd(opt), rollCmd(opt), diceCmd(opt), refsCmd(opt), listCmd(opt), askCmd(opt), chatCmd(opt), adventureCmd(opt), mcpCmd(opt), authCmd())
	return cmd
}

// Execute runs the CLI. Ctrl-C (SIGINT) cancels cmd.Context() rather than
// killing the process outright, so an in-flight HTTP request (a chat
// completion, an embedding batch) gets a chance to abort cleanly and any
// deferred cleanup — the embedding cache's temp-file removal in
// particular — still runs. A second Ctrl-C falls back to the normal
// immediate-exit behavior, so a genuinely stuck operation can still be
// killed outright.
func Execute() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return rootCmd().ExecuteContext(ctx)
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
			return writeEntity(cmd, st, opt.JSON, ents[0])
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
				ents = compare.FilterSources(ents, wanted)
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

func listCmd(opt *options) *cobra.Command {
	var query string
	var sources []string
	var limit int
	var class string
	cmd := &cobra.Command{
		Use:   "list [kind]",
		Short: "List indexed names for a kind (table, encounter, monster, ...), or the kinds themselves if none is given",
		Args:  cobra.MaximumNArgs(1),
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
			if len(args) == 0 {
				kinds, err := st.Kinds()
				if err != nil {
					return err
				}
				if opt.JSON {
					return writeJSON(cmd.OutOrStdout(), map[string]any{"kinds": kinds})
				}
				if len(kinds) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "no kinds indexed")
					return nil
				}
				rows := make([][]string, len(kinds))
				for i, k := range kinds {
					rows[i] = []string{k}
				}
				return writeTable(cmd.OutOrStdout(), []tableColumn{{Header: "Kind", Width: 20}}, rows)
			}
			kind := args[0]
			ed, err := opt.editionPref()
			if err != nil {
				return err
			}
			ents, err := st.FilteredNames(store.NameFilter{Kind: kind, Sources: splitSources(sources), SRDOnly: opt.SRD})
			if err != nil {
				return err
			}
			if len(sources) == 0 {
				ents = edition.Filter(ents, func(e store.Entity) string { return e.Source }, ed)
			}
			if kind == "spell" && class != "" {
				// A DM asking for a class's whole spell list wants the whole
				// list, not the same 100-name default that makes sense for
				// browsing every spell in the index — so it's unlimited
				// unless --limit was actually passed.
				classLimit := limit
				if !cmd.Flags().Changed("limit") {
					classLimit = 0
				}
				return listSpellsForClass(cmd, st, ents, class, opt.JSON, classLimit)
			}
			if query != "" {
				q := strings.ToLower(query)
				filtered := ents[:0]
				for _, e := range ents {
					if strings.Contains(strings.ToLower(e.Name), q) {
						filtered = append(filtered, e)
					}
				}
				ents = filtered
			}
			total := len(ents)
			if limit > 0 && total > limit {
				ents = ents[:limit]
			}
			if opt.JSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{"kind": kind, "total": total, "names": ents})
			}
			if total == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "no %s entries indexed\n", kind)
				return nil
			}
			rows := make([][]string, len(ents))
			for i, e := range ents {
				rows[i] = []string{e.Name, e.Source}
			}
			cols := []tableColumn{{Header: "Name", Width: 30}, {Header: "Source", Width: 8}}
			if err := writeTable(cmd.OutOrStdout(), cols, rows); err != nil {
				return err
			}
			if total > len(ents) {
				fmt.Fprintf(cmd.OutOrStdout(), "... %d more (raise --limit)\n", total-len(ents))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&query, "query", "", "filter names by substring")
	cmd.Flags().StringSliceVar(&sources, "source", nil, "restrict to source ids")
	cmd.Flags().IntVar(&limit, "limit", 100, "maximum names to print, 0 for unlimited")
	cmd.Flags().StringVar(&class, "class", "", "with kind spell, list only spells on this class's list, grouped by level")
	return cmd
}

// listSpellsForClass filters ents (already kind=spell) down to the spells
// granted to class (from each spell's classes field, populated at ingest
// time from 5etools' generated spell/class lookup) and prints them grouped
// by level — the shape a DM prepping a caster actually wants, not a flat
// alphabetical dump.
func listSpellsForClass(cmd *cobra.Command, st *store.Store, ents []store.Entity, class string, asJSON bool, limit int) error {
	type row struct {
		Level  int
		Name   string
		Source string
	}
	var rows []row
	for _, e := range ents {
		found, err := st.Lookup("spell", e.Name, e.Source)
		if err != nil || len(found) == 0 {
			continue
		}
		obj, err := statblock.Decode(found[0].JSON)
		if err != nil {
			continue
		}
		if !statblock.SpellGrantedToClass(obj, class) {
			continue
		}
		rows = append(rows, row{Level: statblock.SpellLevel(obj), Name: e.Name, Source: e.Source})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Level != rows[j].Level {
			return rows[i].Level < rows[j].Level
		}
		return rows[i].Name < rows[j].Name
	})
	if asJSON {
		return writeJSON(cmd.OutOrStdout(), map[string]any{"class": class, "total": len(rows), "spells": rows})
	}
	if len(rows) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "no spells found for class %q\n", class)
		return nil
	}
	total := len(rows)
	if limit > 0 && total > limit {
		rows = rows[:limit]
	}
	tableRows := make([][]string, len(rows))
	for i, r := range rows {
		level := "Cantrip"
		if r.Level > 0 {
			level = strconv.Itoa(r.Level)
		}
		tableRows[i] = []string{level, r.Name, r.Source}
	}
	cols := []tableColumn{{Header: "Level", Width: 8}, {Header: "Name", Width: 30}, {Header: "Source", Width: 8}}
	if err := writeTable(cmd.OutOrStdout(), cols, tableRows); err != nil {
		return err
	}
	if total > len(rows) {
		fmt.Fprintf(cmd.OutOrStdout(), "... %d more (raise --limit)\n", total-len(rows))
	}
	return nil
}

func rollCmd(opt *options) *cobra.Command {
	var kind, source string
	var count, level int
	var seed int64
	cmd := &cobra.Command{
		Use:   "roll <table name>",
		Short: "Roll an indexed random table or random encounter table",
		Long: `Roll an indexed table by name: a plain random table (--kind table, the
default) or a random encounter table (--kind encounter), whose sub-tables are
banded by character level (--level picks the band; the first band is used
when omitted).`,
		Example: `  5e roll Wild Magic Surge
  5e roll "Arctic" --kind encounter --level 7
  5e roll Weather --count 3 --seed 11`,
		Args: cobra.MinimumNArgs(1),
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
			if kind == "" {
				kind = "table"
			}
			name := strings.Join(args, " ")
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
			var seedPtr *int64
			if cmd.Flags().Changed("seed") {
				seedPtr = &seed
			}
			report, err := randomtable.RollTable(ents[0], randomtable.Query{Count: count, Seed: seedPtr, Level: level})
			if err != nil {
				return err
			}
			if opt.JSON {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			return writeRandomTable(cmd.OutOrStdout(), report)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "table", "table or encounter")
	cmd.Flags().StringVar(&source, "source", "", "disambiguate by 5etools source id")
	cmd.Flags().IntVar(&count, "count", 1, "number of rows to roll")
	cmd.Flags().IntVar(&level, "level", 0, "character level, for an encounter table with level-banded sub-tables")
	cmd.Flags().Int64Var(&seed, "seed", 0, "random seed for reproducible rolls")
	return cmd
}

func diceCmd(opt *options) *cobra.Command {
	var count int
	var seed int64
	cmd := &cobra.Command{
		Use:   "dice <expression>",
		Short: "Roll a dice expression such as 2d6+3, 4d6kh3, or adv",
		Long: `Roll standard dice notation: NdM (` + "`2d6`" + `), an added or subtracted
modifier (` + "`2d6+3`" + `), and keep-highest/keep-lowest (` + "`4d6kh3`" + ` for ability
scores). ` + "`adv`" + ` and ` + "`dis`" + ` are shorthand for 2d20kh1 and 2d20kl1.`,
		Example: `  5e dice 2d6+3
  5e dice 4d6kh3 --count 6
  5e dice adv
  5e dice 1d20+5 --seed 11`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			expr := strings.Join(args, " ")
			if count <= 0 {
				count = 1
			}
			var seedPtr *int64
			if cmd.Flags().Changed("seed") {
				seedPtr = &seed
			}
			reports := make([]dice.Report, 0, count)
			for i := range count {
				q := dice.Query{}
				if seedPtr != nil {
					s := *seedPtr + int64(i)
					q.Seed = &s
				}
				report, err := dice.Roll(expr, q)
				if err != nil {
					return err
				}
				reports = append(reports, report)
			}
			if opt.JSON {
				if count == 1 {
					return writeJSON(cmd.OutOrStdout(), reports[0])
				}
				return writeJSON(cmd.OutOrStdout(), reports)
			}
			return writeDiceReports(cmd.OutOrStdout(), reports)
		},
	}
	cmd.Flags().IntVar(&count, "count", 1, "number of times to roll the expression")
	cmd.Flags().Int64Var(&seed, "seed", 0, "random seed for a reproducible roll")
	return cmd
}

// diceReportsText is writeDiceReports' output as a string. Dice rolls have
// no Markdown in them (no headers, no tables), so this needs no rendering
// decision the way renderRandomTable/renderEncounterResults do — it
// exists purely so a caller building a larger block (the chat workspace)
// doesn't have to stand up an io.Writer just to capture this.
func diceReportsText(reports []dice.Report) string {
	var b strings.Builder
	for _, r := range reports {
		fmt.Fprintf(&b, "%s = %d", r.Expression, r.Total)
		parts := make([]string, 0, len(r.Terms))
		for _, t := range r.Terms {
			if t.IsFlat {
				continue
			}
			piece := fmt.Sprint(t.Rolls)
			if t.Keep != "" {
				piece += fmt.Sprintf(" keep %v", t.Kept)
			}
			parts = append(parts, piece)
		}
		if len(parts) > 0 {
			fmt.Fprintf(&b, "  (%s)", strings.Join(parts, ", "))
		}
		fmt.Fprintln(&b)
	}
	return b.String()
}

func writeDiceReports(w io.Writer, reports []dice.Report) error {
	_, err := io.WriteString(w, diceReportsText(reports))
	return err
}

func comparisonEdition(opt *options) (edition.Pref, error) {
	if opt.Edition == "" && os.Getenv("FIVE_E_EDITION") == "" {
		return edition.All, nil
	}
	return opt.editionPref()
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
	var retrieveOnly, adventureOnly bool
	var kind, adventure string
	var sources []string
	var limit int
	cmd := &cobra.Command{
		Use:   "ask <query>",
		Short: "Answer a question from embedded 5e sources",
		Args:  cobra.MinimumNArgs(1),
		Example: `  5e ask "how much damage does fireball do"
  5e ask --adventure LMoP "who is Gundren Rockseeker"
  5e ask --adventure LMoP --adventure-only "what is in the Cragmaw hideout"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if adventureOnly && adventure == "" {
				return fmt.Errorf("--adventure-only needs --adventure")
			}
			return runAsk(cmd, opt, args, askFlags{
				RetrieveOnly:  retrieveOnly,
				Kind:          kind,
				Sources:       splitSources(sources),
				Limit:         limit,
				Adventure:     adventure,
				AdventureOnly: adventureOnly,
			})
		},
	}
	cmd.Flags().BoolVar(&retrieveOnly, "retrieve-only", false, "return ranked chunks without calling a chat model")
	cmd.Flags().StringVar(&kind, "kind", "", "restrict to one entity kind")
	cmd.Flags().StringSliceVar(&sources, "source", nil, "restrict to source ids")
	cmd.Flags().StringVar(&adventure, "adventure", "", "add one adventure's prose to the corpus (id or title)")
	cmd.Flags().BoolVar(&adventureOnly, "adventure-only", false, "answer from that adventure alone, without the rulebooks")
	cmd.Flags().IntVar(&limit, "limit", 8, "maximum retrieved chunks")
	return cmd
}

// askFlags groups the ask options; runAsk had grown past a readable parameter
// list.
type askFlags struct {
	RetrieveOnly  bool
	Kind          string
	Sources       []string
	Limit         int
	Adventure     string
	AdventureOnly bool
}

func runAsk(cmd *cobra.Command, opt *options, args []string, flags askFlags) error {
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
	cfg := ask.ConfigFromEnv()
	if err := applyProviderOverride(&cfg, opt); err != nil {
		return err
	}
	cfg.CachePath = paths.EmbeddingsForIndex(index)
	cfg.Progress = cmd.ErrOrStderr()
	cfg.OnProgress = embedProgressRenderer(cfg.Progress)
	q := ask.Query{
		Text:    strings.Join(args, " "),
		Kind:    flags.Kind,
		Sources: flags.Sources,
		Limit:   flags.Limit,
		Edition: ed,
		SRD:     opt.SRD,
	}
	if flags.Adventure != "" {
		adv, err := adventure.Resolve(st, flags.Adventure)
		if err != nil {
			return err
		}
		q.Adventure = adv.Source
		q.AdventureOnly = flags.AdventureOnly
	}
	if flags.RetrieveOnly {
		return writeRetrieve(cmd, opt.JSON, st, cfg, q)
	}
	return writeAskResult(cmd, opt.JSON, st, cfg, q)
}

func writeRetrieve(cmd *cobra.Command, asJSON bool, st *store.Store, cfg ask.Config, q ask.Query) error {
	out := cmd.OutOrStdout()
	if asJSON {
		hits, err := ask.Retrieve(cmd.Context(), st, cfg, q)
		if err != nil {
			return err
		}
		return writeJSON(out, hits)
	}
	var hits []ask.Hit
	err := runWithStatus(cmd.Context(), out, "retrieving", func(ctx context.Context, _ func(string)) error {
		var err error
		hits, err = ask.Retrieve(ctx, st, cfg, q)
		return err
	})
	if err != nil {
		return err
	}
	return writeAskHits(out, hits)
}

// writeAskResult prints an answer either whole or streamed as it generates,
// depending on the destination. --json needs the complete Result to encode
// one valid object, and a redirected or piped stdout gets one deterministic
// write rather than a stream of partial writes a script would have to
// reassemble; only an interactive TTY streams tokens as the model produces
// them.
func writeAskResult(cmd *cobra.Command, asJSON bool, st *store.Store, cfg ask.Config, q ask.Query) error {
	out := cmd.OutOrStdout()
	if asJSON {
		res, err := ask.Ask(cmd.Context(), st, cfg, q)
		if err != nil {
			return err
		}
		return writeJSON(out, res)
	}

	var res ask.Result
	var err error
	if isTTY(out) {
		// A spinner covers retrieval and the wait for the first token; once
		// a delta arrives, stop() clears it and every following delta is
		// the streamed answer itself, so the two never compete for the line.
		stop := runSpinner(out, "thinking")
		var streamed bool
		res, err = ask.AskStream(cmd.Context(), st, cfg, q, func(delta string) error {
			stop()
			streamed = true
			_, werr := io.WriteString(out, delta)
			return werr
		})
		stop() // no-op if a delta already stopped it; guards the no-matches path
		if err != nil {
			return err
		}
		if streamed {
			fmt.Fprintln(out)
		} else {
			// The "no matching sources" short-circuit never calls onDelta.
			fmt.Fprintln(out, res.Answer)
		}
	} else {
		res, err = ask.Ask(cmd.Context(), st, cfg, q)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, res.Answer)
	}

	if len(res.Citations) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Sources:")
		return writeAskHits(out, res.Citations)
	}
	return nil
}

func adventureCmd(opt *options) *cobra.Command {
	var kind, chapter, location string
	var limit int
	cmd := &cobra.Command{
		Use:   "adventure <id-or-name> <list|search|get> ...",
		Short: "List, search, and look up inside one adventure",
		Example: `  5e adventure LMoP list --kind npc
  5e adventure LMoP search goblin
  5e adventure LMoP get npc "Sildar Hallwinter"`,
		Args: cobra.MinimumNArgs(2),
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
		return fmt.Errorf("adventure expected list, search, or get, got %q", args[1])
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
	return writeEntity(cmd, st, opt.JSON, ents[0])
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
	if err := applyProviderOverride(&cfg, opt); err != nil {
		return err
	}
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
	sty := styles(w)
	for _, h := range hits {
		name := sty.Heading.Render(fmt.Sprintf("%s  %s", h.Kind, h.Name))
		meta := sty.Muted.Render(fmt.Sprintf("(%s)  score=%.2f", h.Source, h.Score))
		fmt.Fprintf(w, "- %s  %s\n", name, meta)
		if h.Snippet != "" {
			fmt.Fprintf(w, "    %s\n", sty.Muted.Render(h.Snippet))
		}
	}
	return nil
}

func writeReferences(w io.Writer, refs []store.Reference) error {
	if len(refs) == 0 {
		fmt.Fprintln(w, "no references")
		return nil
	}
	cols := []tableColumn{
		{Header: "Direction", Width: 10},
		{Header: "Tag", Width: 14},
		{Header: "From", Width: 26},
		{Header: "To", Width: 26},
	}
	rows := make([][]string, len(refs))
	for i, ref := range refs {
		from := fmt.Sprintf("%s %s (%s)", ref.From.Kind, ref.From.Name, ref.From.Source)
		to := fmt.Sprintf("%s %s (%s)", ref.To.Kind, ref.To.Name, ref.To.Source)
		rows[i] = []string{ref.Direction, ref.Tag, from, to}
	}
	return writeTable(w, cols, rows)
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
	rows := [][]string{
		{"Data", fmt.Sprintf("%s [%s]", report.DataPath, dataStatus)},
		{"Index", fmt.Sprintf("%s [%s]", report.IndexPath, indexStatus)},
	}
	if report.DataFingerprint != "" {
		rows = append(rows, []string{"Data fingerprint", shortFingerprint(report.DataFingerprint)})
	}
	if report.IndexFingerprint != "" {
		rows = append(rows, []string{"Index fingerprint", shortFingerprint(report.IndexFingerprint)})
	}
	rows = append(rows, []string{"Status", status})
	if report.Issue != "" {
		rows = append(rows, []string{"Issue", report.Issue})
	}
	cols := []tableColumn{{Header: "Field", Width: 18}, {Header: "Value", Width: 60}}
	return writeTable(w, cols, rows)
}

func shortFingerprint(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func writeAdventureReport(w io.Writer, report adventure.Report) error {
	sty := styles(w)
	sectionCols := []tableColumn{{Header: "Name", Width: 30}, {Header: "Source", Width: 8}}
	var wrote bool
	writeSections := func(heading string, sections []adventure.Section) error {
		if len(sections) == 0 {
			return nil
		}
		wrote = true
		fmt.Fprintln(w, sty.Heading.Render(heading))
		rows := make([][]string, len(sections))
		for i, section := range sections {
			rows[i] = []string{section.Name, section.Source}
		}
		if err := writeTable(w, sectionCols, rows); err != nil {
			return err
		}
		fmt.Fprintln(w)
		return nil
	}
	if err := writeSections("Chapters", report.Chapters); err != nil {
		return err
	}
	if err := writeSections("Locations", report.Locations); err != nil {
		return err
	}
	if len(report.Appearances) > 0 {
		wrote = true
		fmt.Fprintln(w, sty.Heading.Render("Appearances"))
		cols := []tableColumn{
			{Header: "Role", Width: 10},
			{Header: "Name", Width: 24},
			{Header: "Source", Width: 8},
			{Header: "Chapter", Width: 18},
			{Header: "Location", Width: 18},
		}
		rows := make([][]string, len(report.Appearances))
		for i, appearance := range report.Appearances {
			rows[i] = []string{appearance.Role, appearance.Name, appearance.Source, appearance.Chapter, appearance.Location}
		}
		if err := writeTable(w, cols, rows); err != nil {
			return err
		}
	}
	if !wrote {
		fmt.Fprintln(w, "no matches")
	}
	return nil
}

func writeComparison(w io.Writer, result compare.Result) error {
	sty := styles(w)
	sources := make([]string, len(result.Records))
	for i, record := range result.Records {
		sources[i] = record.Source
	}
	fmt.Fprintln(w, sty.Heading.Render(fmt.Sprintf("Compare %s %s", result.Kind, result.Name)))
	fmt.Fprintln(w, sty.Muted.Render("Sources: "+strings.Join(sources, ", ")))
	fmt.Fprintln(w)
	if len(result.Differences) == 0 {
		fmt.Fprintln(w, "No top-level differences.")
		return nil
	}
	cols := make([]tableColumn, 0, len(result.Records)+1)
	cols = append(cols, tableColumn{Header: "Field", Width: 18})
	for _, record := range result.Records {
		cols = append(cols, tableColumn{Header: record.Source, Width: 24})
	}
	rows := make([][]string, len(result.Differences))
	for i, difference := range result.Differences {
		values := make(map[string]compare.Value, len(difference.Values))
		for _, value := range difference.Values {
			values[strings.ToLower(value.Source)] = value
		}
		row := make([]string, 0, len(cols))
		row = append(row, difference.Field)
		for _, record := range result.Records {
			row = append(row, comparisonValue(values[strings.ToLower(record.Source)]))
		}
		rows[i] = row
	}
	return writeTable(w, cols, rows)
}

// encounterResultsMarkdown is encounter search hits as Markdown, or "" when
// there are none — see randomTableMarkdown for why building the text is
// kept separate from deciding how to render it.
// renderEncounterResults renders encounter search hits as an already-styled
// (or plain) table, or "" when there are none — see renderRandomTable for
// why building the display text is kept separate from writing it to w.
func renderEncounterResults(hits []encounter.Hit, styled bool) string {
	if len(hits) == 0 {
		return ""
	}
	cols := []tableColumn{
		{Header: "Name", Width: 26},
		{Header: "CR", Width: 6},
		{Header: "Type", Width: 14},
		{Header: "Size", Width: 8},
		{Header: "Source", Width: 8},
		{Header: "Match", Width: 6, Right: true},
	}
	rows := make([][]string, len(hits))
	for i, hit := range hits {
		rows[i] = []string{hit.Name, hit.CR, hit.Type, hit.Size, hit.Source, fmt.Sprintf("%.2f", hit.Score)}
	}
	return renderTable(cols, rows, styled)
}

func writeEncounterResults(w io.Writer, hits []encounter.Hit) error {
	if len(hits) == 0 {
		fmt.Fprintln(w, "no encounter matches")
		return nil
	}
	_, err := io.WriteString(w, renderEncounterResults(hits, stylingEnabled(w))+"\n")
	return err
}

// renderRandomTable builds a rolled table's heading and results as an
// already-rendered display string, without deciding how (or whether) to
// write it — that decision belongs to the caller, since a plain writer (the
// single `5e roll` command), a chat turn writing straight to a real
// terminal, and the Bubble Tea workspace writing into an in-memory
// transcript block each need a different styling policy.
func renderRandomTable(report randomtable.Report, styled bool) string {
	sty := newStyles(styled)
	var b strings.Builder
	fmt.Fprintln(&b, sty.Heading.Render(report.Name))
	fmt.Fprintln(&b, sty.Muted.Render("table | "+report.Source))
	fmt.Fprintln(&b)
	headers := report.Headers
	if len(headers) == 0 {
		headers = []string{"Result"}
	}
	cols := make([]tableColumn, 0, len(headers)+1)
	cols = append(cols, tableColumn{Header: "Roll", Width: 6, Right: true})
	for _, header := range headers {
		cols = append(cols, tableColumn{Header: header, Width: 36})
	}
	rows := make([][]string, len(report.Rolls))
	for i, roll := range report.Rolls {
		row := make([]string, 0, len(cols))
		row = append(row, fmt.Sprintf("%d", roll.Roll))
		row = append(row, roll.Values...)
		rows[i] = row
	}
	b.WriteString(renderTable(cols, rows, styled))
	return b.String()
}

func writeRandomTable(w io.Writer, report randomtable.Report) error {
	_, err := io.WriteString(w, renderRandomTable(report, stylingEnabled(w))+"\n")
	return err
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

func writeEntity(cmd *cobra.Command, st *store.Store, asJSON bool, e store.Entity) error {
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
	return writeHumanEntity(cmd.OutOrStdout(), st, e)
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
		if err := writeJSON(cmd.OutOrStdout(), map[string]any{"error": "ambiguous", "matches": hits}); err != nil {
			return err
		}
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

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
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
	cmd.AddCommand(ingestCmd(opt), doctorCmd(opt), getCmd(opt), searchCmd(opt), compareCmd(opt), encounterCmd(opt), rollCmd(opt), diceCmd(opt), refsCmd(opt), askCmd(opt), chatCmd(opt), adventureCmd(opt), mcpCmd(opt), authCmd())
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
// decision the way randomTableMarkdown/encounterResultsMarkdown do — it
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

// encounterResultsMarkdown is encounter search hits as Markdown, or "" when
// there are none — see randomTableMarkdown for why building the text is
// kept separate from deciding how to render it.
func encounterResultsMarkdown(hits []encounter.Hit) string {
	if len(hits) == 0 {
		return ""
	}
	var markdown bytes.Buffer
	fmt.Fprintln(&markdown, "| Name | CR | Type | Size | Source | Match |")
	fmt.Fprintln(&markdown, "| --- | --- | --- | --- | --- | ---: |")
	for _, hit := range hits {
		fmt.Fprintf(&markdown, "| %s | %s | %s | %s | %s | %.2f |\n",
			markdownCell(hit.Name), markdownCell(hit.CR), markdownCell(hit.Type), markdownCell(hit.Size), markdownCell(hit.Source), hit.Score)
	}
	return markdown.String()
}

func writeEncounterResults(w io.Writer, hits []encounter.Hit) error {
	if len(hits) == 0 {
		fmt.Fprintln(w, "no encounter matches")
		return nil
	}
	return renderMarkdown(w, encounterResultsMarkdown(hits))
}

// randomTableMarkdown builds a rolled table's Markdown without deciding how
// (or whether) to render it — that decision belongs to the caller, since a
// plain writer (the single `5e roll` command), a chat turn writing straight
// to a real terminal, and the Bubble Tea workspace writing into an
// in-memory transcript block each need a different rendering policy, and
// only writeRandomTable's own isTTY(w) check is right for the first two.
func randomTableMarkdown(report randomtable.Report) string {
	var markdown bytes.Buffer
	fmt.Fprintf(&markdown, "# %s\n\n*table | %s*\n\n", report.Name, report.Source)
	headers := report.Headers
	if len(headers) == 0 {
		headers = []string{"Result"}
	}
	fmt.Fprint(&markdown, "| Roll |")
	for _, header := range headers {
		fmt.Fprintf(&markdown, " %s |", markdownCell(header))
	}
	fmt.Fprintln(&markdown)
	fmt.Fprint(&markdown, "| ---: |")
	for range headers {
		fmt.Fprint(&markdown, " --- |")
	}
	fmt.Fprintln(&markdown)
	for _, roll := range report.Rolls {
		fmt.Fprintf(&markdown, "| %d |", roll.Roll)
		for _, value := range roll.Values {
			fmt.Fprintf(&markdown, " %s |", markdownCell(value))
		}
		fmt.Fprintln(&markdown)
	}
	return markdown.String()
}

func writeRandomTable(w io.Writer, report randomtable.Report) error {
	return renderMarkdown(w, randomTableMarkdown(report))
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

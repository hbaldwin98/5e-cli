package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/hbaldwin98/5e-cli/internal/ask"
	"github.com/hbaldwin98/5e-cli/internal/ingest"
	"github.com/hbaldwin98/5e-cli/internal/paths"
	"github.com/hbaldwin98/5e-cli/internal/search"
	"github.com/hbaldwin98/5e-cli/internal/store"
	"github.com/spf13/cobra"
)

type options struct {
	JSON  bool
	Data  string
	Index string
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
	cmd.AddCommand(ingestCmd(opt), getCmd(opt), searchCmd(opt), askCmd(opt))
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
			ents, err := st.Get(kind, name, source)
			if err != nil {
				return err
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
			hits, err := search.Search(st, search.Query{
				Text:    strings.Join(args, " "),
				Kind:    kind,
				Sources: splitSources(sources),
				Limit:   limit,
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
				Sources: splitSources(sources),
				Limit:   limit,
			}
			if retrieveOnly {
				hits, err := ask.Retrieve(cmd.Context(), st, cfg, q)
				if err != nil {
					return err
				}
				if opt.JSON {
					return writeJSON(cmd.OutOrStdout(), hits)
				}
				return writeAskHits(cmd.OutOrStdout(), hits)
			}
			res, err := ask.Ask(cmd.Context(), st, cfg, q)
			if err != nil {
				return err
			}
			if opt.JSON {
				return writeJSON(cmd.OutOrStdout(), res)
			}
			fmt.Fprintln(cmd.OutOrStdout(), res.Answer)
			if len(res.Citations) > 0 {
				fmt.Fprintln(cmd.OutOrStdout())
				fmt.Fprintln(cmd.OutOrStdout(), "Sources:")
				return writeAskHits(cmd.OutOrStdout(), res.Citations)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&retrieveOnly, "retrieve-only", false, "return ranked chunks without calling a chat model")
	cmd.Flags().StringVar(&kind, "kind", "", "restrict to one entity kind")
	cmd.Flags().StringSliceVar(&sources, "source", nil, "restrict to source ids")
	cmd.Flags().IntVar(&limit, "limit", 8, "maximum retrieved chunks")
	return cmd
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

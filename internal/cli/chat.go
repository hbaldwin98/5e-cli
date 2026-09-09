package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hbaldwin98/5e-cli/internal/adventure"
	"github.com/hbaldwin98/5e-cli/internal/ask"
	"github.com/hbaldwin98/5e-cli/internal/chat"
	"github.com/hbaldwin98/5e-cli/internal/dice"
	"github.com/hbaldwin98/5e-cli/internal/encounter"
	"github.com/hbaldwin98/5e-cli/internal/paths"
	"github.com/hbaldwin98/5e-cli/internal/statblock"
	"github.com/hbaldwin98/5e-cli/internal/store"
	randomtable "github.com/hbaldwin98/5e-cli/internal/table"
	"github.com/spf13/cobra"
)

// shownEntity is one record the get tool actually fetched during a turn,
// kept so its stat block can be rendered as a card in the transcript
// alongside the model's own prose — the model's answer paraphrases what it
// read, but a DM asking "show me a goblin" wants the real stat block, not a
// paraphrase of one.
type shownEntity struct {
	Kind, Name, Source string
	Obj                map[string]any
}

// turnDisplays is everything a turn's tool calls produced that gets
// rendered verbatim in the transcript rather than left to the model to
// paraphrase: entities fetched by get, table/encounter rolls, dice rolls,
// and encounter (monster-search) results. A roll or a dice total is exactly
// the kind of thing a model can misreport when asked to restate it, so
// these render from the tool's own result, not from the model's answer
// text, the same reasoning that already applies to a get card.
type turnDisplays struct {
	Entities   []shownEntity
	Rolls      []randomtable.Report
	DiceRolls  []dice.Report
	Encounters [][]encounter.Hit
}

func (d turnDisplays) empty() bool {
	return len(d.Entities) == 0 && len(d.Rolls) == 0 && len(d.DiceRolls) == 0 && len(d.Encounters) == 0
}

// wrapToolExecutorWithDisplays wraps a tool executor so every get, roll,
// dice, or encounter call's result is also recorded for direct rendering,
// in addition to being returned to the model as text. drain returns and
// clears everything recorded since the last call, so each turn only shows
// what that turn actually did.
func wrapToolExecutorWithDisplays(base ask.ToolExecutor) (wrapped ask.ToolExecutor, drain func() turnDisplays) {
	var mu sync.Mutex
	var d turnDisplays
	wrapped = func(ctx context.Context, call ask.ToolCall) (string, error) {
		result, err := base(ctx, call)
		if err != nil {
			return result, err
		}
		switch call.Name {
		case "get":
			var parsed struct {
				Kind   string `json:"kind"`
				Name   string `json:"name"`
				Source string `json:"source"`
				JSON   any    `json:"json"`
			}
			if jsonErr := json.Unmarshal([]byte(result), &parsed); jsonErr == nil {
				if obj, ok := parsed.JSON.(map[string]any); ok {
					mu.Lock()
					d.Entities = append(d.Entities, shownEntity{Kind: parsed.Kind, Name: parsed.Name, Source: parsed.Source, Obj: obj})
					mu.Unlock()
				}
			}
		case "roll":
			var report randomtable.Report
			if jsonErr := json.Unmarshal([]byte(result), &report); jsonErr == nil {
				mu.Lock()
				d.Rolls = append(d.Rolls, report)
				mu.Unlock()
			}
		case "dice":
			var parsed struct {
				Rolls []dice.Report `json:"rolls"`
			}
			if jsonErr := json.Unmarshal([]byte(result), &parsed); jsonErr == nil {
				mu.Lock()
				d.DiceRolls = append(d.DiceRolls, parsed.Rolls...)
				mu.Unlock()
			}
		case "encounter":
			var parsed struct {
				Hits []encounter.Hit `json:"hits"`
			}
			if jsonErr := json.Unmarshal([]byte(result), &parsed); jsonErr == nil && len(parsed.Hits) > 0 {
				mu.Lock()
				d.Encounters = append(d.Encounters, parsed.Hits)
				mu.Unlock()
			}
		}
		return result, nil
	}
	drain = func() turnDisplays {
		mu.Lock()
		defer mu.Unlock()
		out := d
		d = turnDisplays{}
		return out
	}
	return wrapped, drain
}

// writeTurnDisplays renders everything a turn's tool calls produced:
// entity cards (ANSI, RenderCard), then table/encounter rolls, dice rolls,
// and monster-search results using the same Markdown renderers `5e roll`,
// `5e dice`, and `5e encounter` already use. Only for a color-capable
// terminal — a script or --json consumer already gets the same data as
// each tool's own JSON result.
func writeTurnDisplays(w io.Writer, d turnDisplays, cardWidth int) {
	for _, e := range d.Entities {
		fmt.Fprintln(w, statblock.RenderCard(e.Kind, e.Name, e.Source, e.Obj, cardWidth))
		fmt.Fprintln(w)
	}
	for _, r := range d.Rolls {
		_ = writeRandomTable(w, r)
		fmt.Fprintln(w)
	}
	if len(d.DiceRolls) > 0 {
		_ = writeDiceReports(w, d.DiceRolls)
		fmt.Fprintln(w)
	}
	for _, hits := range d.Encounters {
		_ = writeEncounterResults(w, hits)
		fmt.Fprintln(w)
	}
}

// dedupShown drops repeats by (kind, name, source), keeping the first
// occurrence — a turn can call get on the same entity more than once (a
// retry, or two different tools both resolving to it), and it should only
// get one card.
func dedupShown(entities []shownEntity) []shownEntity {
	seen := make(map[string]bool, len(entities))
	out := make([]shownEntity, 0, len(entities))
	for _, e := range entities {
		key := e.Kind + "|" + e.Name + "|" + e.Source
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	return out
}

// mergeCitations adds one citation per shown entity that isn't already
// among citations, so the "Sources:" list still names what a get call
// fetched even when the model's reply is a short acknowledgment with no
// "(kind, name, source)" triple of its own — citations are otherwise parsed
// only from the model's own answer text (see ask.parseCitations), and the
// prompt now deliberately asks for a minimal reply after a card is shown,
// which would otherwise leave the source line empty for exactly the turns
// most worth citing. Score 1.0 marks these as a direct lookup, not a
// ranked retrieval match.
func mergeCitations(citations []ask.Hit, shown []shownEntity) []ask.Hit {
	seen := make(map[string]bool, len(citations))
	for _, c := range citations {
		seen[c.Kind+"|"+c.Name+"|"+c.Source] = true
	}
	out := citations
	for _, e := range shown {
		key := e.Kind + "|" + e.Name + "|" + e.Source
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ask.Hit{Kind: e.Kind, Name: e.Name, Source: e.Source, Score: 1})
	}
	return out
}

// chatOptions are the chat command's own flags. --chat-dir is shared by the
// subcommands so every one of them reads the same session directory.
type chatOptions struct {
	Dir       string
	Session   string
	Kind      string
	Adventure string
	// AdventureOnly is stored on the session, not applied per question: a
	// conversation that excludes the rulebooks should keep excluding them.
	AdventureOnly bool
	Sources       []string
	Limit         int
}

func chatCmd(opt *options) *cobra.Command {
	copt := &chatOptions{}
	cmd := &cobra.Command{
		Use:   "chat [question]",
		Short: "Hold a saved conversation grounded in the indexed sources",
		Long: `Ask questions in an ongoing conversation. Each question retrieves from the
same embedded corpus as ` + "`ask`" + `, and the transcript and any notes you record are
saved, so a later run continues where this one stopped.`,
		Example: `  5e chat
  5e chat --session curse-of-strahd --adventure CoS
  5e chat "how does grappling work"
  5e chat note "the party sold the Sunsword in Vallaki"
  5e chat clear --session curse-of-strahd
  5e chat list`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChat(cmd, opt, copt, args)
		},
	}
	cmd.PersistentFlags().StringVar(&copt.Dir, "chat-dir", "", "directory holding saved sessions")
	cmd.PersistentFlags().StringVar(&copt.Session, "session", "", "session name (default \"default\")")
	cmd.Flags().StringVar(&copt.Kind, "kind", "", "restrict retrieval to one entity kind")
	cmd.Flags().StringSliceVar(&copt.Sources, "source", nil, "restrict retrieval to source ids")
	cmd.Flags().StringVar(&copt.Adventure, "adventure", "", "add one adventure's prose to the session (id or title; \"none\" clears it)")
	cmd.Flags().BoolVar(&copt.AdventureOnly, "adventure-only", false, "answer from that adventure alone, without the rulebooks")
	cmd.Flags().IntVar(&copt.Limit, "limit", chat.DefaultLimit, "maximum retrieved chunks per question")
	cmd.AddCommand(chatListCmd(opt, copt), chatShowCmd(opt, copt), chatNoteCmd(opt, copt), chatClearCmd(opt, copt), chatRemoveCmd(opt, copt), chatRenameCmd(opt, copt), chatExportCmd(opt, copt), chatImportCmd(opt, copt))
	return cmd
}

func chatListCmd(opt *options, copt *chatOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List saved chat sessions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cs, err := openChatStore(copt)
			if err != nil {
				return err
			}
			sessions, corrupt, err := cs.List()
			if err != nil {
				return err
			}
			for _, c := range corrupt {
				fmt.Fprintf(cmd.ErrOrStderr(), "skipping corrupt session file %s: %s\n", c.File, c.Err)
			}
			if opt.JSON {
				return writeJSON(cmd.OutOrStdout(), sessions)
			}
			if len(sessions) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "no sessions in %s\n", cs.Dir())
				return nil
			}
			for _, s := range sessions {
				line := fmt.Sprintf("- %s  %s, %s", s.Name, plural(s.Turns, "turn"), plural(s.Notes, "note"))
				if s.Adventure != "" {
					scope := s.Adventure
					if s.AdventureOnly {
						scope += " only"
					}
					line += "  [" + scope + "]"
				}
				if when := shortTime(s.Updated); when != "" {
					line += "  " + when
				}
				fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			return nil
		},
	}
}

func chatShowCmd(opt *options, copt *chatOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "show [session]",
		Short: "Print a session's notes and transcript",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cs, err := openChatStore(copt)
			if err != nil {
				return err
			}
			sess, err := cs.LoadExisting(sessionName(copt, args))
			if err != nil {
				return err
			}
			if opt.JSON {
				return writeJSON(cmd.OutOrStdout(), sess)
			}
			out := cmd.OutOrStdout()
			return writeChatSession(out, sess, ttyAnswerRenderer(out))
		},
	}
}

func chatNoteCmd(opt *options, copt *chatOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "note <text>",
		Short: "Record a fact the session keeps in context",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cs, err := openChatStore(copt)
			if err != nil {
				return err
			}
			sess, err := cs.Load(sessionName(copt, nil))
			if err != nil {
				return err
			}
			if !sess.AddNote(strings.Join(args, " ")) {
				return fmt.Errorf("empty note")
			}
			if err := cs.Save(sess); err != nil {
				return err
			}
			if opt.JSON {
				return writeJSON(cmd.OutOrStdout(), sess)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "noted (%d in %s)\n", len(sess.Notes), sess.Name)
			return nil
		},
	}
	cmd.AddCommand(chatNoteRmCmd(opt, copt), chatNoteEditCmd(opt, copt))
	return cmd
}

func chatNoteRmCmd(opt *options, copt *chatOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "rm <n>",
		Short: "Remove one recorded note by its number in `chat note` or /notes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := parseNoteIndex(args[0])
			if err != nil {
				return err
			}
			cs, err := openChatStore(copt)
			if err != nil {
				return err
			}
			sess, err := cs.LoadExisting(sessionName(copt, nil))
			if err != nil {
				return err
			}
			if !sess.RemoveNote(n) {
				return fmt.Errorf("no note numbered %d in %s (has %d)", n, sess.Name, len(sess.Notes))
			}
			if err := cs.Save(sess); err != nil {
				return err
			}
			if opt.JSON {
				return writeJSON(cmd.OutOrStdout(), sess)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed note %d (%d left in %s)\n", n, len(sess.Notes), sess.Name)
			return nil
		},
	}
}

func chatNoteEditCmd(opt *options, copt *chatOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "edit <n> <text>",
		Short: "Replace the text of one recorded note",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := parseNoteIndex(args[0])
			if err != nil {
				return err
			}
			cs, err := openChatStore(copt)
			if err != nil {
				return err
			}
			sess, err := cs.LoadExisting(sessionName(copt, nil))
			if err != nil {
				return err
			}
			if !sess.EditNote(n, strings.Join(args[1:], " ")) {
				return fmt.Errorf("no note numbered %d in %s (has %d)", n, sess.Name, len(sess.Notes))
			}
			if err := cs.Save(sess); err != nil {
				return err
			}
			if opt.JSON {
				return writeJSON(cmd.OutOrStdout(), sess)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "edited note %d in %s\n", n, sess.Name)
			return nil
		},
	}
}

func parseNoteIndex(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 {
		return 0, fmt.Errorf("note number must be a positive integer, got %q", s)
	}
	return n, nil
}

// chatClearCmd empties a session rather than deleting it. Dropping the
// transcript is how you start a new subject without losing the notes, which
// are the part of a session worth keeping.
func chatClearCmd(opt *options, copt *chatOptions) *cobra.Command {
	var withNotes bool
	cmd := &cobra.Command{
		Use:   "clear [session]",
		Short: "Empty a session's transcript, keeping the session and its notes",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cs, err := openChatStore(copt)
			if err != nil {
				return err
			}
			sess, err := cs.LoadExisting(sessionName(copt, args))
			if err != nil {
				return err
			}
			turns, notes := sess.Clear(withNotes)
			if err := cs.Save(sess); err != nil {
				return err
			}
			if opt.JSON {
				return writeJSON(cmd.OutOrStdout(), sess)
			}
			writeCleared(cmd.OutOrStdout(), sess, turns, notes)
			return nil
		},
	}
	cmd.Flags().BoolVar(&withNotes, "notes", false, "clear the recorded notes too")
	return cmd
}

func writeCleared(w io.Writer, sess *chat.Session, turns, notes int) {
	msg := fmt.Sprintf("cleared %s from %s", plural(turns, "turn"), sess.Name)
	switch {
	case notes > 0:
		msg += fmt.Sprintf(" and %s", plural(notes, "note"))
	case len(sess.Notes) > 0:
		msg += fmt.Sprintf(" (%s kept)", plural(len(sess.Notes), "note"))
	}
	fmt.Fprintln(w, styles(w).Success.Render(msg))
}

func chatRemoveCmd(opt *options, copt *chatOptions) *cobra.Command {
	return &cobra.Command{
		Use:     "rm <session>",
		Aliases: []string{"delete"},
		Short:   "Delete a saved session",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cs, err := openChatStore(copt)
			if err != nil {
				return err
			}
			if err := cs.Delete(args[0]); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, styles(out).Success.Render(fmt.Sprintf("deleted %s", args[0])))
			return nil
		},
	}
}

func chatRenameCmd(opt *options, copt *chatOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "rename <session> <new-name>",
		Short: "Rename a saved session, keeping its transcript and notes",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cs, err := openChatStore(copt)
			if err != nil {
				return err
			}
			sess, err := cs.Rename(args[0], args[1])
			if err != nil {
				return err
			}
			if opt.JSON {
				return writeJSON(cmd.OutOrStdout(), sess)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, styles(out).Success.Render(fmt.Sprintf("renamed %q to %q", args[0], sess.Name)))
			return nil
		},
	}
}

func chatExportCmd(opt *options, copt *chatOptions) *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "export <session>",
		Short: "Write a session as JSON, for backup or sharing",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cs, err := openChatStore(copt)
			if err != nil {
				return err
			}
			raw, err := cs.Export(args[0])
			if err != nil {
				return err
			}
			if out == "" {
				_, err := cmd.OutOrStdout().Write(raw)
				return err
			}
			if err := os.WriteFile(out, raw, 0o644); err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			fmt.Fprintln(w, styles(w).Success.Render(fmt.Sprintf("exported %s to %s", args[0], out)))
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "write to this file instead of stdout")
	return cmd
}

func chatImportCmd(opt *options, copt *chatOptions) *cobra.Command {
	var name string
	var force bool
	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Load a session previously written by `chat export`",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cs, err := openChatStore(copt)
			if err != nil {
				return err
			}
			raw, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			sess, err := cs.Import(raw, name, force)
			if err != nil {
				return err
			}
			if opt.JSON {
				return writeJSON(cmd.OutOrStdout(), sess)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, styles(out).Success.Render(fmt.Sprintf("imported %s as %q", args[0], sess.Name)))
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "import under this name instead of the one in the file")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing session with this name")
	return cmd
}

func openChatStore(copt *chatOptions) (*chat.Store, error) {
	dir := copt.Dir
	if dir == "" {
		var err error
		if dir, err = paths.DefaultChatDir(); err != nil {
			return nil, err
		}
	}
	return chat.OpenStore(dir)
}

func sessionName(copt *chatOptions, args []string) string {
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		return args[0]
	}
	if strings.TrimSpace(copt.Session) != "" {
		return copt.Session
	}
	return chat.DefaultName
}

func runChat(cmd *cobra.Command, opt *options, copt *chatOptions, args []string) error {
	data, index, err := resolve(opt)
	if err != nil {
		return err
	}
	st, err := paths.OpenIndex(index, data)
	if err != nil {
		return err
	}
	defer st.Close()
	cs, err := openChatStore(copt)
	if err != nil {
		return err
	}
	ed, err := opt.editionPref()
	if err != nil {
		return err
	}
	sess, err := cs.Load(sessionName(copt, nil))
	if err != nil {
		return err
	}
	if copt.AdventureOnly && copt.Adventure == "" {
		return fmt.Errorf("--adventure-only needs --adventure")
	}
	if copt.Adventure != "" {
		if err := scopeAdventure(st, sess, copt.Adventure, copt.AdventureOnly); err != nil {
			return err
		}
		if err := cs.Save(sess); err != nil {
			return err
		}
	}
	cfg := ask.ConfigFromEnv()
	if err := applyProviderOverride(&cfg, opt); err != nil {
		return err
	}
	cfg.CachePath = paths.EmbeddingsForIndex(index)
	// Tools let the model pull an exact number (challenge rating, AC, a dice
	// or table roll) mid-conversation instead of only paraphrasing retrieved
	// text — the point of a DM companion chat over plain ask. Wiring them in
	// disables true token-by-token streaming for this session (a tool round
	// has to be inspected for tool_calls before there is anything to show),
	// which is judged worth it for live rolls and lookups.
	tools, exec := ask.BuildTools(st, ask.ToolsOptions{Edition: ed, SRD: opt.SRD})
	wrappedExec, drainShown := wrapToolExecutorWithDisplays(exec)
	cfg.Tools, cfg.ToolExecutor = tools, wrappedExec
	inWorkspace := len(args) == 0 && chatWorkspaceAvailable(cmd, opt.JSON)
	if opt.JSON || inWorkspace {
		// The Bubble Tea workspace owns the whole terminal (alt screen,
		// its own render loop); a raw progress bar written straight to
		// stderr underneath it doesn't get composited in, it corrupts the
		// screen the program just drew. The workspace shows its own
		// "thinking" spinner for a slow embedding build instead, so no
		// progress output is written at all — same as --json.
		cfg.Progress = io.Discard
	} else {
		cfg.Progress = cmd.ErrOrStderr()
	}
	cfg.OnProgress = embedProgressRenderer(cfg.Progress)
	opts := chat.Options{Kind: copt.Kind, Sources: splitSources(copt.Sources), Limit: copt.Limit, Edition: ed, SRD: opt.SRD}
	providerName := effectiveProviderName(opt)

	if len(args) > 0 {
		return chatTurn(cmd, opt.JSON, st, cs, cfg, sess, strings.Join(args, " "), opts, drainShown)
	}
	if inWorkspace {
		return runChatWorkspace(cmd, st, cs, sess, cfg, opts, providerName, drainShown)
	}
	return chatREPL(cmd, opt, st, cs, cfg, sess, opts, providerName, drainShown)
}

// scopeSummary is every retrieval filter in effect for a session's next
// question: the adventure scope, which is saved with the session, and the
// per-invocation knobs (--kind, --source, --limit, --edition, --srd), which
// are not. Grouping them is what makes them all visible together in one
// place instead of only at REPL startup (adventure scope) or only after
// changing them (/limit's own confirmation).
type scopeSummary struct {
	Adventure     string   `json:"adventure,omitempty"`
	AdventureOnly bool     `json:"adventureOnly,omitempty"`
	Kind          string   `json:"kind,omitempty"`
	Sources       []string `json:"sources,omitempty"`
	Limit         int      `json:"limit"`
	Edition       string   `json:"edition"`
	SRD           bool     `json:"srd,omitempty"`
}

func scopeFields(sess *chat.Session, opts *chat.Options) scopeSummary {
	return scopeSummary{
		Adventure:     sess.Adventure,
		AdventureOnly: sess.AdventureOnly,
		Kind:          opts.Kind,
		Sources:       opts.Sources,
		Limit:         opts.Limit,
		Edition:       string(opts.Edition),
		SRD:           opts.SRD,
	}
}

func writeScopeFields(w io.Writer, s scopeSummary) {
	adventure := "none"
	if s.Adventure != "" {
		adventure = s.Adventure
		if s.AdventureOnly {
			adventure += " only"
		}
	}
	kind := s.Kind
	if kind == "" {
		kind = "any"
	}
	sources := "any"
	if len(s.Sources) > 0 {
		sources = strings.Join(s.Sources, ", ")
	}
	fmt.Fprintf(w, "adventure: %s\n", adventure)
	fmt.Fprintf(w, "kind: %s\n", kind)
	fmt.Fprintf(w, "sources: %s\n", sources)
	fmt.Fprintf(w, "limit: %d\n", s.Limit)
	fmt.Fprintf(w, "edition: %s\n", s.Edition)
	fmt.Fprintf(w, "srd: %v\n", s.SRD)
}

// scopeAdventure sets or clears the session's adventure scope. The scope is
// stored with the session rather than passed per question, since a
// conversation about one module is about it for every follow-up.
//
// Scoping adds the module's prose to the corpus; the rulebooks stay unless
// only is set. A question asked while running an adventure is usually still a
// rules question.
func scopeAdventure(st *store.Store, sess *chat.Session, name string, only bool) error {
	if strings.EqualFold(name, "none") || strings.EqualFold(name, "clear") {
		sess.Adventure = ""
		sess.AdventureOnly = false
		return nil
	}
	adv, err := adventure.Resolve(st, name)
	if err != nil {
		return err
	}
	sess.Adventure = adv.Source
	sess.AdventureOnly = only
	return nil
}

// chatTurn answers one question and saves the session before returning, so an
// interrupted run never loses an answer the user already read.
//
// On an interactive TTY in human mode, the answer streams to stdout as the
// model generates it rather than appearing all at once; --json and a
// redirected or piped stdout stay on the whole-answer path, since --json
// needs one complete object and a script reading stdout should get one
// deterministic write.
func chatTurn(cmd *cobra.Command, asJSON bool, st *store.Store, cs *chat.Store, cfg ask.Config, sess *chat.Session, question string, opts chat.Options, drainShown func() turnDisplays) error {
	out := cmd.OutOrStdout()
	var res ask.Result
	var err error
	streamed := false
	switch {
	case !asJSON && isTTY(out) && cfg.HasTools():
		// A tool-calling answer is delivered as one finished flush, not real
		// token-by-token deltas (see ask.runToolLoop) — there is nothing to
		// inspect for tool calls until the whole round trip is done. Treat
		// it like any other non-streaming answer so it still gets rendered
		// as Markdown below: a stat block pulled in from a tool result is
		// exactly the kind of answer that carries bold labels and tables,
		// and writing it as raw deltas would print literal "**" and "|"
		// instead of a rendered block.
		stop := runSpinner(out, "thinking")
		res, err = chat.Ask(cmd.Context(), st, cfg, sess, question, opts)
		stop()
	case !asJSON && isTTY(out):
		// A spinner covers retrieval and the wait for the first token; once
		// a delta arrives, stop() clears it so the spinner and the streamed
		// answer never compete for the line.
		stop := runSpinner(out, "thinking")
		res, err = chat.AskStream(cmd.Context(), st, cfg, sess, question, opts, func(delta string) error {
			stop()
			streamed = true
			_, werr := io.WriteString(out, delta)
			return werr
		})
		stop() // no-op if a delta already stopped it; guards the no-matches path
	default:
		res, err = chat.Ask(cmd.Context(), st, cfg, sess, question, opts)
	}
	if err != nil {
		return err
	}
	if err := cs.Save(sess); err != nil {
		return err
	}
	// Displays come before the model's own prose, and are driven only by
	// actual tool calls — not by matching citations against the retrieved
	// sources — so they appear exactly when the model chose to look
	// something up or roll something, never as a second, possibly
	// redundant rendering of something it already answered from source
	// text. ANSI, so only on a color-capable terminal; a script gets the
	// same data from each tool's own JSON result.
	displays := drainShown()
	displays.Entities = dedupShown(displays.Entities)
	if !asJSON && stylingEnabled(out) {
		writeTurnDisplays(out, displays, statblockCardWidth)
	}
	res.Citations = mergeCitations(res.Citations, displays.Entities)
	return writeChatTurn(cmd, asJSON, sess, question, res, streamed)
}

// writeChatTurn prints the answer and its citations. When streamed is true,
// the answer's own text already reached stdout as raw deltas during
// generation; only the trailing newline and citations are left to print, and
// re-rendering it as Markdown here would duplicate the answer on screen.
func writeChatTurn(cmd *cobra.Command, asJSON bool, sess *chat.Session, question string, res ask.Result, streamed bool) error {
	out := cmd.OutOrStdout()
	if asJSON {
		return writeJSON(out, chatTurnResult{
			Type:      "turn",
			Session:   sess.Name,
			Question:  question,
			Answer:    res.Answer,
			Citations: res.Citations,
		})
	}
	if streamed {
		fmt.Fprintln(out)
	} else if err := renderMarkdown(out, res.Answer+"\n"); err != nil {
		return err
	}
	if len(res.Citations) > 0 {
		fmt.Fprintln(out, "Sources:")
		if err := writeAskHits(out, res.Citations); err != nil {
			return err
		}
	}
	fmt.Fprintln(out)
	return nil
}

// chatTurnResult is one exchange in --json mode. It names the session so a
// caller piping turns can tell which conversation an answer belongs to.
type chatTurnResult struct {
	Type      string    `json:"type"`
	Session   string    `json:"session"`
	Question  string    `json:"question"`
	Answer    string    `json:"answer"`
	Citations []ask.Hit `json:"citations,omitempty"`
}

// chatCommandResult is one successful slash-command event in a JSON REPL.
// Data contains the command-specific result without making consumers parse
// human-oriented output.
type chatCommandResult struct {
	Type    string `json:"type"`
	Session string `json:"session"`
	Command string `json:"command"`
	Data    any    `json:"data,omitempty"`
}

// chatErrorResult keeps failures in the JSON stream. A failed question has a
// question; a failed slash command has a command.
type chatErrorResult struct {
	Type     string `json:"type"`
	Session  string `json:"session,omitempty"`
	Command  string `json:"command,omitempty"`
	Question string `json:"question,omitempty"`
	Message  string `json:"message"`
}

// chatSlashCommands is the canonical list of slash commands: chatHelp below
// is generated from it, and the interactive workspace uses it to suggest
// and Tab-complete commands as they're typed (see chattui.go's
// matchingSlashCommands). Keep this the single source of truth for command
// names rather than letting chatHelp's text and chatCommand's switch drift
// apart from what the workspace suggests.
var chatSlashCommands = []struct{ Name, Usage string }{
	{"/note", "<text>        record a fact this session keeps in context"},
	{"/notes", "             list the recorded notes"},
	{"/sources", "           citations for the last answer"},
	{"/adventure", "<id-or-title>   add an adventure's prose (append \" only\" to drop the rulebooks, or use \"none\" to clear the scope)"},
	{"/scope", "             show the active adventure scope and retrieval filters"},
	{"/limit", "<n>          retrieved chunks per question"},
	{"/kind", "[kind|none]   restrict retrieval to one entity kind, or clear it"},
	{"/source", "[ids|none]  restrict retrieval to source ids (comma-separated), or clear it"},
	{"/srd", "[on|off]       show or set whether retrieval is SRD-only"},
	{"/history", "           print the transcript"},
	{"/clear", "[all]        drop the transcript, or \"all\" to drop the notes too"},
	{"/provider", "[name]    switch to a configured provider (see 5e auth list), or show the current model/base url"},
	{"/model", "[name]       change and persist that provider's chat model, or show the current one"},
	{"/help", "               this list"},
	{"/exit", "               leave (Ctrl-D also works)"},
}

var chatHelp = buildChatHelp()

func buildChatHelp() string {
	var b strings.Builder
	b.WriteString("Commands:\n")
	for _, c := range chatSlashCommands {
		fmt.Fprintf(&b, "  %s %s\n", c.Name, c.Usage)
	}
	b.WriteString("  /note rm <n>        remove the note numbered <n> in /notes\n")
	b.WriteString("  /note edit <n> <text>   replace the text of note <n>\n")
	b.WriteString("In the interactive workspace: PgUp/PgDn scroll the transcript, Tab\n")
	b.WriteString("completes a slash command, Ctrl-T toggles mouse-wheel scrolling\n")
	b.WriteString("(off by default so click-drag select/copy works natively), Ctrl-G\n")
	b.WriteString("toggles the key reference.\n")
	b.WriteString("Anything else is a question.")
	return b.String()
}

func chatREPL(cmd *cobra.Command, opt *options, st *store.Store, cs *chat.Store, cfg ask.Config, sess *chat.Session, opts chat.Options, providerName string, drainShown func() turnDisplays) error {
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()
	// In --json mode the loop is a stream of turn objects, so the banner and
	// the prompt would be noise in the middle of the JSON.
	if !opt.JSON {
		fmt.Fprintf(out, "session %s (%s, %s)", sess.Name, plural(len(sess.Turns), "turn"), plural(len(sess.Notes), "note"))
		if scope := sess.Scope(); scope != "" {
			fmt.Fprintf(out, " scoped to %s", scope)
		}
		if opts.Kind != "" || len(opts.Sources) > 0 || opts.SRD {
			fmt.Fprint(out, "; /scope for active filters")
		}
		fmt.Fprintf(out, "\n/help for commands, /exit to leave\n\n")
	}

	renderAnswer := ttyAnswerRenderer(out)
	lines := bufio.NewScanner(cmd.InOrStdin())
	lines.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for {
		if !opt.JSON {
			fmt.Fprint(out, "> ")
		}
		if !lines.Scan() {
			break
		}
		line := strings.TrimSpace(lines.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			quit, event, err := chatCommand(cmd, st, cs, sess, &cfg, &opts, &providerName, renderAnswer, line, opt.JSON)
			if err != nil {
				if opt.JSON {
					if writeErr := writeChatError(out, sess, event.Command, "", err); writeErr != nil {
						return writeErr
					}
				} else {
					writeREPLError(errOut, err)
				}
				if quit {
					return err
				}
				continue
			}
			if opt.JSON {
				if err := writeJSON(out, event); err != nil {
					return err
				}
			}
			if quit {
				return nil
			}
			continue
		}
		// A failed question must not end the conversation: a rate limit or a
		// dropped connection should cost one turn, not the session.
		if err := chatTurn(cmd, opt.JSON, st, cs, cfg, sess, line, opts, drainShown); err != nil {
			if opt.JSON {
				if writeErr := writeChatError(out, sess, "", line, err); writeErr != nil {
					return writeErr
				}
			} else {
				writeREPLError(errOut, err)
			}
		}
	}
	if err := lines.Err(); err != nil && err != io.EOF {
		if opt.JSON {
			if writeErr := writeChatError(out, sess, "", "", err); writeErr != nil {
				return writeErr
			}
		}
		return err
	}
	if !opt.JSON {
		fmt.Fprintln(out)
	}
	if err := cs.Save(sess); err != nil {
		if opt.JSON {
			if writeErr := writeChatError(out, sess, "", "", err); writeErr != nil {
				return writeErr
			}
		}
		return err
	}
	return nil
}

// chatCommand runs one slash command, reporting whether the session should
// end. Anything it changes is saved immediately, since the REPL is the thing
// people leave open and then close the terminal on.
func chatCommand(cmd *cobra.Command, st *store.Store, cs *chat.Store, sess *chat.Session, cfg *ask.Config, opts *chat.Options, providerName *string, renderAnswer func(string) string, line string, asJSON bool) (bool, chatCommandResult, error) {
	out := cmd.OutOrStdout()
	name, rest, _ := strings.Cut(line, " ")
	rest = strings.TrimSpace(rest)
	event := chatCommandResult{Type: "command", Session: sess.Name, Command: name}
	switch strings.ToLower(name) {
	case "/exit", "/quit":
		return true, event, cs.Save(sess)
	case "/help":
		event.Data = map[string]any{"help": chatHelp}
		if !asJSON {
			fmt.Fprintln(out, chatHelp)
		}
	case "/note":
		// "/note rm <n>" and "/note edit <n> <text>" manage an existing note;
		// anything else is the text of a new one, so a note that happens to
		// start with the word "rm" or "edit" is the one case this shadows.
		if sub, arg, found := strings.Cut(rest, " "); found && strings.EqualFold(sub, "rm") {
			n, err := parseNoteIndex(strings.TrimSpace(arg))
			if err != nil {
				return false, event, err
			}
			if !sess.RemoveNote(n) {
				return false, event, fmt.Errorf("no note numbered %d (has %d)", n, len(sess.Notes))
			}
			if err := cs.Save(sess); err != nil {
				return false, event, err
			}
			event.Data = map[string]any{"removed": n, "notes": len(sess.Notes)}
			if !asJSON {
				fmt.Fprintf(out, "removed note %d (%d left)\n", n, len(sess.Notes))
			}
			break
		}
		if sub, arg, found := strings.Cut(rest, " "); found && strings.EqualFold(sub, "edit") {
			nStr, text, _ := strings.Cut(strings.TrimSpace(arg), " ")
			n, err := parseNoteIndex(nStr)
			if err != nil {
				return false, event, err
			}
			if !sess.EditNote(n, text) {
				return false, event, fmt.Errorf("no note numbered %d (has %d), or the replacement text was empty", n, len(sess.Notes))
			}
			if err := cs.Save(sess); err != nil {
				return false, event, err
			}
			event.Data = map[string]any{"edited": n}
			if !asJSON {
				fmt.Fprintf(out, "edited note %d\n", n)
			}
			break
		}
		if !sess.AddNote(rest) {
			return false, event, fmt.Errorf("usage: /note <text>")
		}
		if err := cs.Save(sess); err != nil {
			return false, event, err
		}
		event.Data = map[string]any{"notes": len(sess.Notes)}
		if !asJSON {
			fmt.Fprintf(out, "noted (%d)\n", len(sess.Notes))
		}
	case "/notes":
		event.Data = map[string]any{"notes": sess.Notes}
		if len(sess.Notes) == 0 {
			if !asJSON {
				fmt.Fprintln(out, "no notes")
			}
			break
		}
		if !asJSON {
			for i, n := range sess.Notes {
				fmt.Fprintf(out, "%d. %s\n", i+1, n.Text)
			}
		}
	case "/sources":
		var citations []ask.Hit
		if len(sess.Turns) == 0 {
			if !asJSON {
				fmt.Fprintln(out, "no answers yet")
			}
			break
		}
		last := sess.Turns[len(sess.Turns)-1]
		citations = last.Citations
		event.Data = map[string]any{"citations": citations}
		if !asJSON {
			if err := writeAskHits(out, citations); err != nil {
				return false, event, err
			}
		}
	case "/adventure":
		if rest == "" {
			event.Data = map[string]any{"scope": sess.Scope()}
			if scope := sess.Scope(); scope == "" {
				if !asJSON {
					fmt.Fprintln(out, "not scoped to an adventure")
				}
			} else {
				if !asJSON {
					fmt.Fprintln(out, scope)
				}
			}
			break
		}
		// "LMoP only" drops the rulebooks; plain "LMoP" adds the module to
		// them, which is what a question asked at the table usually needs.
		// The trailing "only" is checked against the last word, not the text
		// after the first space, so a multiword title like "Lost Mine of
		// Phandelver only" is not split into "Lost" plus a bogus tail.
		name, only := rest, false
		if fields := strings.Fields(rest); len(fields) > 1 && strings.EqualFold(fields[len(fields)-1], "only") {
			name = strings.TrimSpace(strings.Join(fields[:len(fields)-1], " "))
			only = true
		}
		if err := scopeAdventure(st, sess, name, only); err != nil {
			return false, event, err
		}
		if err := cs.Save(sess); err != nil {
			return false, event, err
		}
		event.Data = map[string]any{"scope": sess.Scope()}
		if scope := sess.Scope(); scope == "" {
			if !asJSON {
				fmt.Fprintln(out, "adventure scope cleared")
			}
		} else {
			if !asJSON {
				fmt.Fprintf(out, "scoped to %s\n", scope)
			}
		}
	case "/scope":
		fields := scopeFields(sess, opts)
		event.Data = map[string]any{"scope": fields}
		if !asJSON {
			writeScopeFields(out, fields)
		}
	case "/limit":
		n, err := parseChatLimit(rest)
		if err != nil {
			return false, event, err
		}
		opts.Limit = n
		event.Data = map[string]any{"limit": n}
		if !asJSON {
			fmt.Fprintf(out, "limit %d\n", n)
		}
	case "/kind":
		if strings.EqualFold(rest, "none") || strings.EqualFold(rest, "any") {
			rest = ""
		}
		opts.Kind = rest
		event.Data = map[string]any{"kind": opts.Kind}
		if !asJSON {
			if opts.Kind == "" {
				fmt.Fprintln(out, "kind cleared")
			} else {
				fmt.Fprintf(out, "kind %s\n", opts.Kind)
			}
		}
	case "/source":
		if strings.EqualFold(rest, "none") || strings.EqualFold(rest, "any") {
			rest = ""
		}
		opts.Sources = splitSources([]string{rest})
		event.Data = map[string]any{"sources": opts.Sources}
		if !asJSON {
			if len(opts.Sources) == 0 {
				fmt.Fprintln(out, "sources cleared")
			} else {
				fmt.Fprintf(out, "sources %s\n", strings.Join(opts.Sources, ", "))
			}
		}
	case "/srd":
		on, err := parseChatToggle(rest, opts.SRD)
		if err != nil {
			return false, event, err
		}
		opts.SRD = on
		event.Data = map[string]any{"srd": opts.SRD}
		if !asJSON {
			fmt.Fprintf(out, "srd %v\n", opts.SRD)
		}
	case "/clear":
		withNotes := false
		switch strings.ToLower(rest) {
		case "", "turns", "history":
		case "all", "notes":
			withNotes = true
		default:
			return false, event, fmt.Errorf("usage: /clear [all]")
		}
		turns, notes := sess.Clear(withNotes)
		if err := cs.Save(sess); err != nil {
			return false, event, err
		}
		event.Data = map[string]any{"turns": turns, "notes": notes}
		if !asJSON {
			writeCleared(out, sess, turns, notes)
		}
	case "/history":
		event.Data = sess
		if !asJSON {
			if err := writeChatSession(out, sess, renderAnswer); err != nil {
				return false, event, err
			}
		}
	case "/provider":
		if rest == "" {
			event.Data = map[string]any{"model": cfg.AskModel, "embed_model": cfg.EmbedModel, "base_url": cfg.BaseURL}
			if !asJSON {
				fmt.Fprintf(out, "model %s (embed %s)\nbase url %s\n", cfg.AskModel, cfg.EmbedModel, cfg.BaseURL)
			}
			break
		}
		name := strings.ToLower(rest)
		cred, err := loadProviderCredential(name)
		if err != nil {
			return false, event, err
		}
		applyCredential(cfg, cred)
		*providerName = name
		event.Data = map[string]any{"provider": rest, "model": cfg.AskModel, "embed_model": cfg.EmbedModel}
		if !asJSON {
			fmt.Fprintf(out, "provider %s (model %s, embed %s)\n", rest, cfg.AskModel, cfg.EmbedModel)
		}
	case "/model":
		if rest == "" {
			event.Data = map[string]any{"model": cfg.AskModel}
			if !asJSON {
				fmt.Fprintf(out, "model %s\n", cfg.AskModel)
			}
			break
		}
		cfg.AskModel = rest
		event.Data = map[string]any{"model": cfg.AskModel}
		// /model persists, unlike --model/one-shot session state: the whole
		// point of a slash command over the flag is "change this and keep
		// it changed" without a separate `5e auth set-model` step.
		if *providerName == "" {
			return false, event, fmt.Errorf("model %s applied for this session, but there is no active stored provider to save it to; run `5e auth login <provider>` or set FIVE_E_ASK_MODEL to persist a choice", rest)
		}
		if err := saveProviderModel(*providerName, rest); err != nil {
			return false, event, fmt.Errorf("model %s applied for this session, but saving it failed: %w", rest, err)
		}
		if !asJSON {
			fmt.Fprintf(out, "model %s (saved to %s)\n", rest, *providerName)
		}
	default:
		return false, event, fmt.Errorf("unknown command %s; /help for the list", name)
	}
	return false, event, nil
}

// writeREPLError prints a failed slash command or turn to the REPL's error
// stream in human mode. It never ends the session by itself — a failed
// question or command costs one turn, not the conversation.
func writeREPLError(w io.Writer, err error) {
	sty := styles(w)
	fmt.Fprintln(w, sty.Error.Render(err.Error()))
}

func writeChatError(w io.Writer, sess *chat.Session, command, question string, err error) error {
	return writeJSON(w, chatErrorResult{
		Type:     "error",
		Session:  sess.Name,
		Command:  command,
		Question: question,
		Message:  err.Error(),
	})
}

// writeChatSession prints a session's notes and transcript. renderAnswer, if
// not nil, is applied to each turn's answer before it's printed — the
// answer text is Markdown (the source material routinely carries
// bold/italics/tables), and without rendering it shows up as literal
// `**`/`#`/table-pipe syntax. A nil renderAnswer prints the raw text
// unchanged, for a destination (a script, a pipe) that shouldn't see ANSI.
func writeChatSession(w io.Writer, sess *chat.Session, renderAnswer func(string) string) error {
	fmt.Fprintf(w, "session %s", sess.Name)
	if scope := sess.Scope(); scope != "" {
		fmt.Fprintf(w, " [%s]", scope)
	}
	fmt.Fprintln(w)
	if len(sess.Notes) > 0 {
		fmt.Fprintln(w, "\nNotes:")
		for i, n := range sess.Notes {
			fmt.Fprintf(w, "%d. %s\n", i+1, n.Text)
		}
	}
	if len(sess.Turns) == 0 {
		fmt.Fprintln(w, "\nno turns yet")
		return nil
	}
	for _, t := range sess.Turns {
		answer := t.Answer
		if renderAnswer != nil {
			answer = renderAnswer(answer)
		}
		fmt.Fprintf(w, "\n> %s\n%s\n", t.Question, answer)
		if len(t.Citations) > 0 {
			if err := writeAskHits(w, t.Citations); err != nil {
				return err
			}
		}
	}
	return nil
}

func parseChatLimit(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("usage: /limit <positive number>")
	}
	return n, nil
}

// parseChatToggle reads a boolean argument for a flag-like slash command.
// No argument at all reports the current value rather than erroring, so
// "/srd" alone is a status check and "/srd on" or "/srd off" is a change.
func parseChatToggle(s string, current bool) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return current, nil
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	default:
		return false, fmt.Errorf("usage: /srd [on|off]")
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// shortTime trims a stored timestamp to what a session list needs. The stored
// form carries microseconds so sessions sort; nobody reads those.
func shortTime(s string) string {
	t, err := time.Parse(chat.Timestamp, s)
	if err != nil {
		return s
	}
	return t.Local().Format("2006-01-02 15:04")
}

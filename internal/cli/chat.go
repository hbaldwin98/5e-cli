package cli

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/hbaldwin98/5e-cli/internal/adventure"
	"github.com/hbaldwin98/5e-cli/internal/ask"
	"github.com/hbaldwin98/5e-cli/internal/chat"
	"github.com/hbaldwin98/5e-cli/internal/paths"
	"github.com/hbaldwin98/5e-cli/internal/store"
	"github.com/spf13/cobra"
)

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
	cmd.AddCommand(chatListCmd(opt, copt), chatShowCmd(opt, copt), chatNoteCmd(opt, copt), chatClearCmd(opt, copt), chatRemoveCmd(opt, copt))
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
			sessions, err := cs.List()
			if err != nil {
				return err
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
			sess, err := cs.Load(sessionName(copt, args))
			if err != nil {
				return err
			}
			if opt.JSON {
				return writeJSON(cmd.OutOrStdout(), sess)
			}
			return writeChatSession(cmd.OutOrStdout(), sess)
		},
	}
}

func chatNoteCmd(opt *options, copt *chatOptions) *cobra.Command {
	return &cobra.Command{
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
			sess, err := cs.Load(sessionName(copt, args))
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
	fmt.Fprintln(w, msg)
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
			fmt.Fprintf(cmd.OutOrStdout(), "deleted %s\n", args[0])
			return nil
		},
	}
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
	cfg.CachePath = paths.EmbeddingsForIndex(index)
	cfg.Progress = cmd.ErrOrStderr()
	opts := chat.Options{Kind: copt.Kind, Sources: splitSources(copt.Sources), Limit: copt.Limit, SRD: opt.SRD}

	if len(args) > 0 {
		return chatTurn(cmd, opt.JSON, st, cs, cfg, sess, strings.Join(args, " "), opts)
	}
	return chatREPL(cmd, opt, st, cs, cfg, sess, opts)
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
func chatTurn(cmd *cobra.Command, asJSON bool, st *store.Store, cs *chat.Store, cfg ask.Config, sess *chat.Session, question string, opts chat.Options) error {
	res, err := chat.Ask(cmd.Context(), st, cfg, sess, question, opts)
	if err != nil {
		return err
	}
	if err := cs.Save(sess); err != nil {
		return err
	}
	return writeChatTurn(cmd, asJSON, sess, question, res)
}

func writeChatTurn(cmd *cobra.Command, asJSON bool, sess *chat.Session, question string, res ask.Result) error {
	out := cmd.OutOrStdout()
	if asJSON {
		return writeJSON(out, chatTurnResult{
			Session:   sess.Name,
			Question:  question,
			Answer:    res.Answer,
			Citations: res.Citations,
		})
	}
	if err := renderMarkdown(out, res.Answer+"\n"); err != nil {
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
	Session   string    `json:"session"`
	Question  string    `json:"question"`
	Answer    string    `json:"answer"`
	Citations []ask.Hit `json:"citations,omitempty"`
}

const chatHelp = `Commands:
  /note <text>        record a fact this session keeps in context
  /notes              list the recorded notes
  /sources            citations for the last answer
  /adventure <id>     add an adventure's prose ("<id> only" drops the
                      rulebooks, "none" clears the scope)
  /limit <n>          retrieved chunks per question
  /history            print the transcript
  /clear [all]        drop the transcript, or "all" to drop the notes too
  /help               this list
  /exit               leave (Ctrl-D also works)
Anything else is a question.`

func chatREPL(cmd *cobra.Command, opt *options, st *store.Store, cs *chat.Store, cfg ask.Config, sess *chat.Session, opts chat.Options) error {
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()
	// In --json mode the loop is a stream of turn objects, so the banner and
	// the prompt would be noise in the middle of the JSON.
	if !opt.JSON {
		fmt.Fprintf(out, "session %s (%s, %s)", sess.Name, plural(len(sess.Turns), "turn"), plural(len(sess.Notes), "note"))
		if scope := sess.Scope(); scope != "" {
			fmt.Fprintf(out, " scoped to %s", scope)
		}
		fmt.Fprintf(out, "\n/help for commands, /exit to leave\n\n")
	}

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
			quit, err := chatCommand(cmd, st, cs, sess, &opts, line)
			if err != nil {
				fmt.Fprintf(errOut, "%v\n", err)
			}
			if quit {
				return nil
			}
			continue
		}
		// A failed question must not end the conversation: a rate limit or a
		// dropped connection should cost one turn, not the session.
		if err := chatTurn(cmd, opt.JSON, st, cs, cfg, sess, line, opts); err != nil {
			fmt.Fprintf(errOut, "%v\n", err)
		}
	}
	if err := lines.Err(); err != nil && err != io.EOF {
		return err
	}
	if !opt.JSON {
		fmt.Fprintln(out)
	}
	return cs.Save(sess)
}

// chatCommand runs one slash command, reporting whether the session should
// end. Anything it changes is saved immediately, since the REPL is the thing
// people leave open and then close the terminal on.
func chatCommand(cmd *cobra.Command, st *store.Store, cs *chat.Store, sess *chat.Session, opts *chat.Options, line string) (bool, error) {
	out := cmd.OutOrStdout()
	name, rest, _ := strings.Cut(line, " ")
	rest = strings.TrimSpace(rest)
	switch strings.ToLower(name) {
	case "/exit", "/quit":
		return true, cs.Save(sess)
	case "/help":
		fmt.Fprintln(out, chatHelp)
	case "/note":
		if !sess.AddNote(rest) {
			return false, fmt.Errorf("usage: /note <text>")
		}
		if err := cs.Save(sess); err != nil {
			return false, err
		}
		fmt.Fprintf(out, "noted (%d)\n", len(sess.Notes))
	case "/notes":
		if len(sess.Notes) == 0 {
			fmt.Fprintln(out, "no notes")
			break
		}
		for i, n := range sess.Notes {
			fmt.Fprintf(out, "%d. %s\n", i+1, n.Text)
		}
	case "/sources":
		if len(sess.Turns) == 0 {
			fmt.Fprintln(out, "no answers yet")
			break
		}
		last := sess.Turns[len(sess.Turns)-1]
		if err := writeAskHits(out, last.Citations); err != nil {
			return false, err
		}
	case "/adventure":
		if rest == "" {
			if scope := sess.Scope(); scope == "" {
				fmt.Fprintln(out, "not scoped to an adventure")
			} else {
				fmt.Fprintln(out, scope)
			}
			break
		}
		// "LMoP only" drops the rulebooks; plain "LMoP" adds the module to
		// them, which is what a question asked at the table usually needs.
		name, only := rest, false
		if head, tail, found := strings.Cut(rest, " "); found {
			if strings.EqualFold(strings.TrimSpace(tail), "only") {
				name, only = strings.TrimSpace(head), true
			}
		}
		if err := scopeAdventure(st, sess, name, only); err != nil {
			return false, err
		}
		if err := cs.Save(sess); err != nil {
			return false, err
		}
		if scope := sess.Scope(); scope == "" {
			fmt.Fprintln(out, "adventure scope cleared")
		} else {
			fmt.Fprintf(out, "scoped to %s\n", scope)
		}
	case "/limit":
		n, err := parseChatLimit(rest)
		if err != nil {
			return false, err
		}
		opts.Limit = n
		fmt.Fprintf(out, "limit %d\n", n)
	case "/clear":
		withNotes := false
		switch strings.ToLower(rest) {
		case "", "turns", "history":
		case "all", "notes":
			withNotes = true
		default:
			return false, fmt.Errorf("usage: /clear [all]")
		}
		turns, notes := sess.Clear(withNotes)
		if err := cs.Save(sess); err != nil {
			return false, err
		}
		writeCleared(out, sess, turns, notes)
	case "/history":
		if err := writeChatSession(out, sess); err != nil {
			return false, err
		}
	default:
		return false, fmt.Errorf("unknown command %s; /help for the list", name)
	}
	return false, nil
}

func writeChatSession(w io.Writer, sess *chat.Session) error {
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
		fmt.Fprintf(w, "\n> %s\n%s\n", t.Question, t.Answer)
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

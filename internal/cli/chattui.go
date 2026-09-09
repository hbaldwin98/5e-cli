package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/hbaldwin98/5e-cli/internal/ask"
	"github.com/hbaldwin98/5e-cli/internal/chat"
	"github.com/hbaldwin98/5e-cli/internal/statblock"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

// cardWidth picks a card width from the viewport's own width, so a card
// fits the pane instead of using the CLI's fixed statblockCardWidth (there
// is no fixed terminal width in the workspace — it already tracks resizes).
// It falls back to statblockCardWidth before the first WindowSizeMsg, when
// the viewport hasn't been sized yet.
func cardWidth(viewportWidth int) int {
	if viewportWidth <= 0 {
		return statblockCardWidth
	}
	return viewportWidth
}

// chatWorkspaceAvailable reports whether the interactive Bubble Tea chat
// workspace (#44) can run: both stdin and stdout must be a real interactive
// terminal — piped or redirected input would hang waiting for terminal
// keystrokes that never come, or feed the program garbage — and only in
// human mode. --json stays on chatREPL's plain, line-oriented path, which is
// a stream of parseable events, not a terminal UI, and a scripted question
// (chat "text") never reaches either REPL path at all.
func chatWorkspaceAvailable(cmd *cobra.Command, asJSON bool) bool {
	return !asJSON && isTTY(cmd.OutOrStdout()) && isTTYReader(cmd.InOrStdin())
}

// runChatWorkspace runs the interactive chat workspace until the user quits.
// It reuses chatCommand for every slash command and chat.AskStream for every
// question — the same domain logic chatREPL uses — so behavior (including
// which turns get persisted) stays identical between the two REPL paths;
// only presentation differs.
func runChatWorkspace(cmd *cobra.Command, st *store.Store, cs *chat.Store, sess *chat.Session, cfg ask.Config, opts chat.Options, providerName string, drainShown func() turnDisplays) error {
	m := newChatModel(cmd, st, cs, sess, cfg, opts, providerName, drainShown)
	p := tea.NewProgram(m,
		tea.WithContext(cmd.Context()),
		tea.WithInput(cmd.InOrStdin()),
		tea.WithOutput(cmd.OutOrStdout()),
		// Ctrl-C is handled entirely through the normal raw-mode key-event
		// path below (cancel an in-flight turn, or quit when idle); Bubble
		// Tea's own SIGINT handler would otherwise force-quit the program
		// on the same keystroke before that contextual choice is made.
		tea.WithoutSignalHandler(),
	)
	_, err := p.Run()
	return err
}

type chatKeyMap struct {
	Submit      key.Binding
	Newline     key.Binding
	Up          key.Binding
	Down        key.Binding
	ScrollUp    key.Binding
	ScrollDown  key.Binding
	Complete    key.Binding
	ToggleMouse key.Binding
	Cancel      key.Binding
	Quit        key.Binding
	Help        key.Binding
}

func defaultChatKeyMap() chatKeyMap {
	return chatKeyMap{
		Submit:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
		Newline: key.NewBinding(key.WithKeys("ctrl+j", "alt+enter"), key.WithHelp("ctrl+j", "newline")),
		Up:      key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "history")),
		Down:    key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "history")),
		// ctrl+b is deliberately not bound here: it's tmux's default prefix
		// key, so under tmux it would never reach this program at all.
		ScrollUp:    key.NewBinding(key.WithKeys("pgup", "ctrl+u"), key.WithHelp("pgup", "scroll up")),
		ScrollDown:  key.NewBinding(key.WithKeys("pgdown", "ctrl+f"), key.WithHelp("pgdn", "scroll down")),
		Complete:    key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "complete command")),
		ToggleMouse: key.NewBinding(key.WithKeys("ctrl+t"), key.WithHelp("ctrl+t", "toggle mouse")),
		Cancel:      key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "cancel turn / quit")),
		Quit:        key.NewBinding(key.WithKeys("ctrl+d", "esc"), key.WithHelp("ctrl+d", "quit")),
		Help:        key.NewBinding(key.WithKeys("ctrl+g"), key.WithHelp("ctrl+g", "toggle help")),
	}
}

func (k chatKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Submit, k.Newline, k.Up, k.ScrollUp, k.Complete, k.ToggleMouse, k.Cancel, k.Quit, k.Help}
}

func (k chatKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.ShortHelp()}
}

// deltaMsg is one streamed token of the in-flight answer.
type deltaMsg string

// toolCallMsg reports that a tool-calling turn just invoked one tool, so the
// spinner can say what's happening instead of a generic "thinking" for
// however many round trips runToolLoop takes (it can't stream tokens — see
// ask.Config.HasTools' doc comment).
type toolCallMsg string

// turnDoneMsg is the final result of a streamed question — success or
// failure (including cancellation, which arrives as context.Canceled).
type turnDoneMsg struct {
	res ask.Result
	err error
}

type chatModel struct {
	cmd          *cobra.Command
	st           *store.Store
	cs           *chat.Store
	sess         *chat.Session
	cfg          ask.Config
	opts         chat.Options
	providerName string
	// drainShown returns and clears the entities a turn's get tool calls
	// fetched, so finishTurn can render their stat blocks as cards.
	drainShown func() turnDisplays

	keys chatKeyMap
	help help.Model

	viewport viewport.Model
	input    textarea.Model
	spin     spinner.Model

	// transcript is everything already committed to the scrollback: the
	// banner, past questions and answers, and slash-command output. pending
	// is the current turn's streamed text, shown appended below transcript
	// but not yet part of it until the turn finishes.
	transcript []transcriptBlock
	pending    strings.Builder
	streaming  bool
	turnCh     <-chan any
	cancel     context.CancelFunc
	// toolStatus is the most recent tool name a tool-calling turn invoked,
	// shown next to the spinner in place of a bare "thinking" while the
	// round-trip loop runs. Cleared at the start of each turn.
	toolStatus string

	// history is every line submitted (questions and slash commands alike),
	// most recent last, recalled with Up/Down the way a shell history does.
	// historyIdx == len(history) means "not currently recalling"; draft
	// holds what was being typed before the first Up press so Down can
	// return to it.
	history    []string
	historyIdx int
	draft      string

	width, height int
	ready         bool
	showHelp      bool
	quitting      bool
	// mouseEnabled toggles tea.View.MouseMode (Ctrl-T). Off by default: a
	// terminal that requests mouse reporting is exactly what stops most
	// terminals offering their own click-drag text selection, and Shift+drag
	// (the usual override) isn't reliable across every terminal — so the
	// default favors copy/paste working out of the box, with the wheel a
	// keystroke away for whoever wants it instead.
	mouseEnabled bool
}

func newChatModel(cmd *cobra.Command, st *store.Store, cs *chat.Store, sess *chat.Session, cfg ask.Config, opts chat.Options, providerName string, drainShown func() turnDisplays) *chatModel {
	ta := textarea.New()
	ta.Placeholder = "Ask a question, or /help for commands"
	ta.ShowLineNumbers = false
	ta.Focus()

	m := &chatModel{
		cmd:          cmd,
		st:           st,
		cs:           cs,
		sess:         sess,
		cfg:          cfg,
		opts:         opts,
		providerName: providerName,
		drainShown:   drainShown,
		keys:         defaultChatKeyMap(),
		help:         help.New(),
		spin:         spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		viewport:     viewport.New(),
		input:        ta,
	}
	m.writeLine(sessionBanner(sess, opts))
	return m
}

func sessionBanner(sess *chat.Session, opts chat.Options) string {
	var b strings.Builder
	fmt.Fprintf(&b, "session %s (%s, %s)", sess.Name, plural(len(sess.Turns), "turn"), plural(len(sess.Notes), "note"))
	if scope := sess.Scope(); scope != "" {
		fmt.Fprintf(&b, " scoped to %s", scope)
	}
	if opts.Kind != "" || len(opts.Sources) > 0 || opts.SRD {
		b.WriteString("; /scope for active filters")
	}
	return b.String()
}

// transcriptBlock is one committed piece of scrollback. preWrapped marks
// content (a glamour-rendered answer) that already carries its own
// word-wrap and ANSI styling baked in at a fixed width — refreshViewport
// must not run lipgloss's word-wrap over it a second time, since re-wrapping
// already-wrapped, already-styled ANSI text is exactly the kind of thing
// that silently mangles it (a lipgloss Width-render pads and re-breaks
// lines without understanding glamour's own layout decisions).
type transcriptBlock struct {
	text       string
	preWrapped bool
}

func (m *chatModel) writeLine(s string) {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return
	}
	m.transcript = append(m.transcript, transcriptBlock{text: s})
}

// transcriptText joins every committed block's raw text (unwrapped), for
// callers that just want to check what's in the scrollback — tests, mostly
// — without caring about the wrapping refreshViewport applies at render
// time.
func (m *chatModel) transcriptText() string {
	texts := make([]string, len(m.transcript))
	for i, block := range m.transcript {
		texts[i] = block.text
	}
	return strings.Join(texts, "\n\n")
}

// writeRendered is writeLine for content that must reach the screen exactly
// as rendered — currently just glamour-rendered answers (see renderAnswer).
func (m *chatModel) writeRendered(s string) {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return
	}
	m.transcript = append(m.transcript, transcriptBlock{text: s, preWrapped: true})
}

func (m *chatModel) refreshViewport() {
	var b strings.Builder
	width := m.viewport.Width()
	for i, block := range m.transcript {
		if i > 0 {
			b.WriteString("\n\n")
		}
		if block.preWrapped {
			b.WriteString(block.text)
		} else {
			b.WriteString(wrapToWidth(block.text, width))
		}
	}
	if m.streaming {
		switch {
		case m.pending.Len() > 0:
			b.WriteString("\n\n")
			b.WriteString(wrapToWidth(m.pending.String(), width))
		default:
			// No tokens yet: show a spinner so a slow retrieval or a slow
			// first token never looks like the workspace has frozen. If a
			// tool-calling turn has started calling tools, name the most
			// recent one instead of a bare "thinking" — the loop can take
			// several round trips and a static label reads as a hang.
			label := "thinking"
			if m.toolStatus != "" {
				label = "calling " + m.toolStatus
			}
			b.WriteString("\n\n")
			b.WriteString(lipgloss.NewStyle().Faint(true).Render(m.spin.View() + " " + label))
		}
	}
	atBottom := m.viewport.AtBottom()
	m.viewport.SetContent(b.String())
	if atBottom {
		m.viewport.GotoBottom()
	}
}

// wrapToWidth word-wraps content to width, leaving it untouched when width
// isn't known yet (before the first tea.WindowSizeMsg). The viewport itself
// never wraps long lines on its own — it only scrolls — so without this an
// answer wider than the terminal runs off the right edge instead of
// flowing to the next line.
func wrapToWidth(content string, width int) string {
	if width <= 0 {
		return content
	}
	return lipgloss.NewStyle().Width(width).Render(content)
}

func (m *chatModel) Init() tea.Cmd {
	return nil
}

func (m *chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.resize()
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case deltaMsg:
		m.pending.WriteString(string(msg))
		m.refreshViewport()
		return m, listenTurn(m.turnCh)

	case toolCallMsg:
		m.toolStatus = string(msg)
		m.refreshViewport()
		return m, listenTurn(m.turnCh)

	case turnDoneMsg:
		m.finishTurn(msg)
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		if !m.streaming {
			// The turn already finished (or was cancelled); let the tick
			// chain end here instead of ticking forever in the background.
			return m, nil
		}
		if m.pending.Len() == 0 {
			m.refreshViewport()
		}
		return m, cmd
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m *chatModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		m.quitting = true
		return m, tea.Quit

	case key.Matches(msg, m.keys.Cancel):
		if m.streaming {
			m.cancel()
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit

	case key.Matches(msg, m.keys.Help):
		m.showHelp = !m.showHelp
		m.resize()
		return m, nil

	case key.Matches(msg, m.keys.Newline):
		m.input.InsertRune('\n')
		return m, nil

	case key.Matches(msg, m.keys.Submit):
		return m.submit()

	case key.Matches(msg, m.keys.Up):
		if !strings.Contains(m.input.Value(), "\n") && len(m.history) > 0 {
			m.recallHistory(-1)
			return m, nil
		}

	case key.Matches(msg, m.keys.Down):
		if !strings.Contains(m.input.Value(), "\n") && m.historyIdx < len(m.history) {
			m.recallHistory(1)
			return m, nil
		}

	case key.Matches(msg, m.keys.ScrollUp):
		m.viewport.PageUp()
		return m, nil

	case key.Matches(msg, m.keys.ScrollDown):
		m.viewport.PageDown()
		return m, nil

	case key.Matches(msg, m.keys.Complete):
		if m.completeSlashCommand() {
			return m, nil
		}

	case key.Matches(msg, m.keys.ToggleMouse):
		m.mouseEnabled = !m.mouseEnabled
		if m.mouseEnabled {
			m.writeLine(lipgloss.NewStyle().Faint(true).Render("mouse: on (wheel scrolls; drag-select needs Shift on most terminals)"))
		} else {
			m.writeLine(lipgloss.NewStyle().Faint(true).Render("mouse: off (native click-drag select/copy restored; PgUp/PgDn still scroll)"))
		}
		m.refreshViewport()
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// matchingSlashCommands returns the commands in chatSlashCommands whose
// name starts with prefix (case-insensitive). It only makes sense to call
// this while prefix looks like an in-progress command word — see
// slashCommandPrefix.
func matchingSlashCommands(prefix string) []string {
	prefix = strings.ToLower(prefix)
	var out []string
	for _, c := range chatSlashCommands {
		if strings.HasPrefix(strings.ToLower(c.Name), prefix) {
			out = append(out, c.Name)
		}
	}
	return out
}

// slashCommandPrefix returns the input's current text as a slash-command
// prefix worth completing or suggesting against, and true, only when the
// input is a single line whose only content so far is the start of a
// command word (a "/" with no following space yet) — once a space appears
// the user has moved on to the command's arguments, where completion and
// suggestions no longer apply.
func slashCommandPrefix(value string) (string, bool) {
	if !strings.HasPrefix(value, "/") || strings.ContainsAny(value, " \n") {
		return "", false
	}
	return value, true
}

// completeSlashCommand implements Tab: with a single unambiguous match it
// completes the input to that command plus a trailing space (ready for
// arguments); with several matches it completes as far as their shared
// prefix goes, same as a shell's Tab-completion. It reports whether it did
// anything, so Tab can fall through to the textarea's own handling (a
// literal tab, mid-answer) when the input isn't a slash command at all.
func (m *chatModel) completeSlashCommand() bool {
	prefix, ok := slashCommandPrefix(m.input.Value())
	if !ok {
		return false
	}
	matches := matchingSlashCommands(prefix)
	switch len(matches) {
	case 0:
		return false
	case 1:
		m.input.SetValue(matches[0] + " ")
		return true
	default:
		common := commonPrefix(matches)
		if len(common) <= len(prefix) {
			return false
		}
		m.input.SetValue(common)
		return true
	}
}

// commonPrefix returns the longest string every element of ss starts with.
func commonPrefix(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	prefix := ss[0]
	for _, s := range ss[1:] {
		for !strings.HasPrefix(s, prefix) {
			prefix = prefix[:len(prefix)-1]
			if prefix == "" {
				return ""
			}
		}
	}
	return prefix
}

func (m *chatModel) recallHistory(dir int) {
	if dir < 0 {
		if m.historyIdx == len(m.history) {
			m.draft = m.input.Value()
		}
		if m.historyIdx > 0 {
			m.historyIdx--
		}
	} else {
		if m.historyIdx < len(m.history) {
			m.historyIdx++
		}
	}
	if m.historyIdx == len(m.history) {
		m.input.SetValue(m.draft)
	} else {
		m.input.SetValue(m.history[m.historyIdx])
	}
	m.input.CursorEnd()
}

// submit sends whatever is in the input box: a slash command runs (and
// completes) immediately, since none of them do network I/O; a question
// starts a streamed turn in the background. Submission is ignored while a
// turn is already in flight — one question at a time.
func (m *chatModel) submit() (tea.Model, tea.Cmd) {
	if m.streaming {
		return m, nil
	}
	line := strings.TrimSpace(m.input.Value())
	if line == "" {
		return m, nil
	}
	m.history = append(m.history, line)
	m.historyIdx = len(m.history)
	m.draft = ""
	m.input.Reset()

	if strings.HasPrefix(line, "/") {
		return m.runSlashCommand(line)
	}
	return m.startTurn(line)
}

// runSlashCommand reuses chatCommand exactly, capturing what it would have
// written to stdout/stderr into the transcript instead — the same commands,
// the same output text, just relocated into the workspace's scrollback.
func (m *chatModel) runSlashCommand(line string) (tea.Model, tea.Cmd) {
	m.writeLine("> " + line)
	captured := &cobra.Command{}
	var buf bytes.Buffer
	captured.SetOut(&buf)
	captured.SetErr(&buf)
	quit, _, err := chatCommand(captured, m.st, m.cs, m.sess, &m.cfg, &m.opts, &m.providerName, m.renderAnswer, line, false)
	if out := strings.TrimSpace(buf.String()); out != "" {
		// /history's captured output already carries glamour-rendered,
		// ANSI-styled answers interleaved with plain question/citation
		// text (chatCommand applied m.renderAnswer per turn, via
		// writeChatSession) — the whole block has to go in preWrapped, the
		// same as a single rendered answer, or refreshViewport's normal
		// wrap pass would corrupt the already-styled parts exactly like it
		// used to for a single answer. Every other command's output is
		// plain text and still wants normal wrapping.
		name, _, _ := strings.Cut(line, " ")
		if strings.EqualFold(name, "/history") {
			m.writeRendered(out)
		} else {
			m.writeLine(out)
		}
	}
	if err != nil {
		m.writeLine(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9")).Render(err.Error()))
	}
	m.refreshViewport()
	if quit {
		m.quitting = true
		return m, tea.Quit
	}
	return m, nil
}

func (m *chatModel) startTurn(question string) (tea.Model, tea.Cmd) {
	m.writeLine("> " + question)
	m.pending.Reset()
	m.streaming = true
	m.toolStatus = ""

	parent := m.cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	ch := make(chan any, 32)
	m.turnCh = ch
	cfg := m.cfg
	cfg.OnToolCall = func(name string, _ json.RawMessage) {
		select {
		case ch <- toolCallMsg(name):
		case <-ctx.Done():
		}
	}
	go func() {
		res, err := chat.AskStream(ctx, m.st, cfg, m.sess, question, m.opts, func(delta string) error {
			select {
			case ch <- deltaMsg(delta):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		ch <- turnDoneMsg{res: res, err: err}
		close(ch)
	}()

	m.refreshViewport()
	return m, tea.Batch(listenTurn(m.turnCh), m.spin.Tick)
}

// listenTurn waits for the next event on a turn's channel. Handling it is
// Update's job (a deltaMsg re-issues listenTurn to keep listening; a
// turnDoneMsg does not, since the channel is closed after it).
func listenTurn(ch <-chan any) tea.Cmd {
	return func() tea.Msg {
		v, ok := <-ch
		if !ok {
			return nil
		}
		return v
	}
}

func (m *chatModel) finishTurn(msg turnDoneMsg) {
	m.streaming = false
	m.turnCh = nil
	m.cancel = nil
	defer m.refreshViewport()

	// Displays are driven only by actual tool calls, not by matching
	// citations against the retrieved sources, so one appears exactly when
	// the model chose to look something up or roll something — never as a
	// second, possibly redundant rendering of something it already
	// answered from source text on its own.
	var displays turnDisplays
	if m.drainShown != nil {
		displays = m.drainShown()
		displays.Entities = dedupShown(displays.Entities)
	}

	if msg.err != nil {
		// Whatever text already streamed to the screen stays visible either
		// way — a cancelled or interrupted turn still leaves it, so the
		// answer doesn't just vanish behind the error line once it's been
		// sitting there in front of the user.
		if m.pending.Len() > 0 {
			m.writeLine(m.pending.String())
		}
		if errors.Is(msg.err, context.Canceled) {
			m.writeLine(lipgloss.NewStyle().Faint(true).Render("(cancelled)"))
		} else {
			m.writeLine(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9")).Render(msg.err.Error()))
		}
		m.pending.Reset()
		return
	}

	// Displays come before the model's own prose: a DM asking "show me a
	// goblin" wants the actual stat block the get tool fetched, not only
	// the model's paraphrase of it, and a roll result should be the tool's
	// own number, not the model's retelling of it. Rolls and encounter hits
	// render through renderRandomTable/renderEncounterResults directly
	// (already fully rendered, styled ANSI) rather than writeTurnDisplays'
	// own writers, which decide styling from isTTY(w) — false for the
	// bytes.Buffer this used to render into — or through m.renderAnswer,
	// which would run Glamour over already-rendered lipgloss output and
	// mangle it; the workspace is always a real terminal, so styled is
	// always true here.
	for _, e := range displays.Entities {
		m.writeRendered(statblock.RenderCard(e.Kind, e.Name, e.Source, e.Obj, cardWidth(m.viewport.Width())))
	}
	for _, r := range displays.Rolls {
		m.writeRendered(renderRandomTable(r, true))
	}
	if len(displays.DiceRolls) > 0 {
		m.writeRendered(strings.TrimRight(diceReportsText(displays.DiceRolls), "\n"))
	}
	for _, hits := range displays.Encounters {
		if s := renderEncounterResults(hits, true); s != "" {
			m.writeRendered(s)
		}
	}
	m.writeRendered(m.renderAnswer(msg.res.Answer))
	msg.res.Citations = mergeCitations(msg.res.Citations, displays.Entities)
	if len(msg.res.Citations) > 0 {
		var buf bytes.Buffer
		buf.WriteString("Sources:\n")
		_ = writeAskHits(&buf, msg.res.Citations)
		m.writeLine(strings.TrimRight(buf.String(), "\n"))
	}
	m.pending.Reset()
	if err := m.cs.Save(m.sess); err != nil {
		m.writeLine(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9")).Render(err.Error()))
	}
}

// renderAnswer renders a finished answer as Markdown (source text routinely
// carries bold/italics/tables/headers), falling back to the raw trimmed
// text if glamour fails to render it for any reason — an unrendered but
// correct answer beats losing it. Streamed deltas are never rendered this
// way: a partial Markdown document mid-stream renders unpredictably, so raw
// text is shown while a turn is in flight and only the final, complete
// answer gets this treatment.
func (m *chatModel) renderAnswer(answer string) string {
	answer = strings.TrimSpace(answer)
	rendered, err := renderMarkdownToString(answer, m.viewport.Width())
	if err != nil {
		return answer
	}
	return strings.TrimSpace(rendered)
}

// slashSuggestionsLine renders the commands matching what's currently typed
// (see slashCommandPrefix), or "" when there's nothing to suggest — an
// empty input, plain text, a command whose arguments have already started,
// or a command that's already typed out in full with nothing left to
// complete. Always returning a string (never skipping the line) is what
// lets resize() reserve a constant one line of height for it instead of the
// whole layout shifting around on every keystroke.
func (m *chatModel) slashSuggestionsLine() string {
	prefix, ok := slashCommandPrefix(m.input.Value())
	if !ok {
		return ""
	}
	matches := matchingSlashCommands(prefix)
	if len(matches) == 0 || (len(matches) == 1 && matches[0] == prefix) {
		return ""
	}
	return lipgloss.NewStyle().Faint(true).Render(strings.Join(matches, "  ") + "  (tab to complete)")
}

func (m *chatModel) resize() {
	helpHeight := 1
	if m.showHelp {
		helpHeight = 1
	}
	const inputHeight = 3
	const suggestHeight = 1
	vpHeight := m.height - inputHeight - helpHeight - suggestHeight - 1
	if vpHeight < 1 {
		vpHeight = 1
	}
	m.viewport.SetWidth(m.width)
	m.viewport.SetHeight(vpHeight)
	m.input.SetWidth(m.width)
	m.input.SetHeight(inputHeight)
	m.refreshViewport()
}

func (m *chatModel) View() tea.View {
	var v tea.View
	v.AltScreen = true
	// Mouse reporting (wheel-scroll) and native click-drag text selection
	// can't both be on: a terminal that requests mouse tracking is exactly
	// what stops most terminals offering their own selection, and Shift+drag
	// (the usual override) isn't reliable enough across terminals to lean
	// on by default. Ctrl-T (m.mouseEnabled) toggles it; off — selection
	// and copy work natively — is the default, with PgUp/PgDn always
	// available as the keyboard scroll path either way.
	if m.mouseEnabled {
		v.MouseMode = tea.MouseModeCellMotion
	}
	if !m.ready {
		v.SetContent("")
		return v
	}
	var b strings.Builder
	b.WriteString(m.viewport.View())
	b.WriteString("\n")
	b.WriteString(m.slashSuggestionsLine())
	b.WriteString("\n")
	b.WriteString(m.input.View())
	b.WriteString("\n")
	if m.showHelp {
		b.WriteString(m.help.View(m.keys))
	} else {
		b.WriteString(lipgloss.NewStyle().Faint(true).Render("ctrl+g for help"))
	}
	v.SetContent(b.String())
	return v
}

package cli

import (
	"bytes"
	"context"
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
	"github.com/hbaldwin98/5e-cli/internal/store"
)

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
func runChatWorkspace(cmd *cobra.Command, st *store.Store, cs *chat.Store, sess *chat.Session, cfg ask.Config, opts chat.Options) error {
	m := newChatModel(cmd, st, cs, sess, cfg, opts)
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
	Submit  key.Binding
	Newline key.Binding
	Up      key.Binding
	Down    key.Binding
	Cancel  key.Binding
	Quit    key.Binding
	Help    key.Binding
}

func defaultChatKeyMap() chatKeyMap {
	return chatKeyMap{
		Submit:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
		Newline: key.NewBinding(key.WithKeys("ctrl+j", "alt+enter"), key.WithHelp("ctrl+j", "newline")),
		Up:      key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "history")),
		Down:    key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "history")),
		Cancel:  key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "cancel turn / quit")),
		Quit:    key.NewBinding(key.WithKeys("ctrl+d", "esc"), key.WithHelp("ctrl+d", "quit")),
		Help:    key.NewBinding(key.WithKeys("ctrl+g"), key.WithHelp("ctrl+g", "toggle help")),
	}
}

func (k chatKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Submit, k.Newline, k.Up, k.Cancel, k.Quit, k.Help}
}

func (k chatKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.ShortHelp()}
}

// deltaMsg is one streamed token of the in-flight answer.
type deltaMsg string

// turnDoneMsg is the final result of a streamed question — success or
// failure (including cancellation, which arrives as context.Canceled).
type turnDoneMsg struct {
	res ask.Result
	err error
}

type chatModel struct {
	cmd  *cobra.Command
	st   *store.Store
	cs   *chat.Store
	sess *chat.Session
	cfg  ask.Config
	opts chat.Options

	keys chatKeyMap
	help help.Model

	viewport viewport.Model
	input    textarea.Model
	spin     spinner.Model

	// transcript is everything already committed to the scrollback: the
	// banner, past questions and answers, and slash-command output. pending
	// is the current turn's streamed text, shown appended below transcript
	// but not yet part of it until the turn finishes.
	transcript strings.Builder
	pending    strings.Builder
	streaming  bool
	turnCh     <-chan any
	cancel     context.CancelFunc

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
}

func newChatModel(cmd *cobra.Command, st *store.Store, cs *chat.Store, sess *chat.Session, cfg ask.Config, opts chat.Options) *chatModel {
	ta := textarea.New()
	ta.Placeholder = "Ask a question, or /help for commands"
	ta.ShowLineNumbers = false
	ta.Focus()

	m := &chatModel{
		cmd:      cmd,
		st:       st,
		cs:       cs,
		sess:     sess,
		cfg:      cfg,
		opts:     opts,
		keys:     defaultChatKeyMap(),
		help:     help.New(),
		spin:     spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		viewport: viewport.New(),
		input:    ta,
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

func (m *chatModel) writeLine(s string) {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return
	}
	if m.transcript.Len() > 0 {
		m.transcript.WriteString("\n\n")
	}
	m.transcript.WriteString(s)
}

func (m *chatModel) refreshViewport() {
	content := m.transcript.String()
	if m.streaming {
		switch {
		case m.pending.Len() > 0:
			content += "\n\n" + m.pending.String()
		default:
			// No tokens yet: show a spinner so a slow retrieval or a slow
			// first token never looks like the workspace has frozen.
			content += "\n\n" + lipgloss.NewStyle().Faint(true).Render(m.spin.View()+" thinking")
		}
	}
	atBottom := m.viewport.AtBottom()
	m.viewport.SetContent(content)
	if atBottom {
		m.viewport.GotoBottom()
	}
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
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
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
	quit, _, err := chatCommand(captured, m.st, m.cs, m.sess, &m.cfg, &m.opts, line, false)
	if out := strings.TrimSpace(buf.String()); out != "" {
		m.writeLine(out)
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

	parent := m.cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	ch := make(chan any, 32)
	m.turnCh = ch
	go func() {
		res, err := chat.AskStream(ctx, m.st, m.cfg, m.sess, question, m.opts, func(delta string) error {
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

	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			if m.pending.Len() > 0 {
				m.writeLine(m.pending.String())
			}
			m.writeLine(lipgloss.NewStyle().Faint(true).Render("(cancelled)"))
		} else {
			m.writeLine(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9")).Render(msg.err.Error()))
		}
		m.pending.Reset()
		return
	}

	m.writeLine(strings.TrimSpace(msg.res.Answer))
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

func (m *chatModel) resize() {
	helpHeight := 1
	if m.showHelp {
		helpHeight = 1
	}
	const inputHeight = 3
	vpHeight := m.height - inputHeight - helpHeight - 1
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
	if !m.ready {
		v.SetContent("")
		return v
	}
	var b strings.Builder
	b.WriteString(m.viewport.View())
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

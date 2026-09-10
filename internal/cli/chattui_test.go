package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/hbaldwin98/5e-cli/internal/ask"
	"github.com/hbaldwin98/5e-cli/internal/chat"
	"github.com/hbaldwin98/5e-cli/internal/edition"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

// newTestChatModel builds a chatModel against chatFixture's index and a
// fresh session, wired to chatFixture's fake streaming server the same way
// runChat wires a real Config.
func newTestChatModel(t *testing.T) (*chatModel, *chatAPI) {
	t.Helper()
	index, api := chatFixture(t)
	st, err := store.Open(index)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cs, err := chat.OpenStore(filepath.Join(t.TempDir(), "chats"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := cs.Load("test")
	if err != nil {
		t.Fatal(err)
	}
	cfg := ask.ConfigFromEnv()
	cfg.CachePath = filepath.Join(t.TempDir(), "embeddings.sqlite")

	// chatFixture's canned streamed answer cites (spell, Fireball, PHB);
	// the classic edition keeps PHB instead of preferring XPHB, so that
	// citation actually matches what was retrieved.
	m := newChatModel(&cobra.Command{}, st, cs, sess, cfg, chat.Options{Limit: 3, Edition: edition.Classic}, "", nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return m, api
}

// newTestChatModelWithProvider is newTestChatModel plus an isolated,
// stored "openai" provider so a test can exercise /model's persistence
// path. The stored credential is unrelated to the fake streaming server
// chatFixture wires up (chat requests still hit that fixture via cfg,
// exactly as in newTestChatModel) — it exists only so saveProviderModel
// has somewhere to write.
func newTestChatModelWithProvider(t *testing.T) (*chatModel, *chatAPI) {
	t.Helper()
	withIsolatedConfigDir(t)
	runAuth(t, "", "login", "openai", "--api-key", "sk-test-0123456789")
	m, api := newTestChatModel(t)
	m.providerName = "openai"
	return m, api
}

func TestChatModel_providerCommandOpensPicker(t *testing.T) {
	m, _ := newTestChatModelWithProvider(t)
	_, cmd := m.runSlashCommand("/provider")
	if cmd != nil {
		t.Fatal("provider picker should not require async loading")
	}
	if m.selecting != selectorProvider {
		t.Fatalf("selector = %v", m.selecting)
	}
	_, _ = m.handleSelectorKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.selecting != selectorNone || m.providerName != "openai" {
		t.Fatalf("selection did not apply: mode=%v provider=%q", m.selecting, m.providerName)
	}
}

func TestChatModel_modelPickerAppliesLoadedSelection(t *testing.T) {
	m, _ := newTestChatModelWithProvider(t)
	m.Update(modelsLoadedMsg{provider: "openai", models: []string{"gpt-picked"}})
	if m.selecting != selectorModel {
		t.Fatalf("selector = %v", m.selecting)
	}
	_, _ = m.handleSelectorKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.cfg.AskModel != "gpt-picked" {
		t.Fatalf("model = %q", m.cfg.AskModel)
	}
	cred, _ := providerCredentialForTest(t, "openai")
	if cred.ChatModel != "gpt-picked" {
		t.Fatalf("stored model = %q", cred.ChatModel)
	}
}

func TestChatModel_escapeCancelsPicker(t *testing.T) {
	m, _ := newTestChatModelWithProvider(t)
	m.openSelector(selectorModel, "Choose", []string{"gpt-picked"})
	before := m.cfg.AskModel
	_, _ = m.handleSelectorKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.selecting != selectorNone || m.cfg.AskModel != before {
		t.Fatalf("cancel changed selection: mode=%v model=%q", m.selecting, m.cfg.AskModel)
	}
}

// drive runs cmd (and every tea.Cmd it and its follow-ups produce) to
// completion against m, the same loop tea.Program's runtime performs, minus
// the terminal. It is how these tests exercise chatModel's real streaming
// and slash-command concurrency without a real terminal or tea.Program.
func drive(t *testing.T, m *chatModel, cmd tea.Cmd) {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 10000 {
			t.Fatal("drive: too many messages, likely an infinite Cmd loop")
		}
		cmd, queue = queue[0], queue[1:]
		if cmd == nil {
			continue
		}
		msg := cmd()
		if msg == nil {
			continue
		}
		// tea.Batch's Cmd returns a BatchMsg (a slice of Cmds) for the real
		// runtime to fan out and run concurrently; replicate that here by
		// queuing each sub-command instead of feeding the BatchMsg itself
		// into Update, which has no case for it.
		if batch, ok := msg.(tea.BatchMsg); ok {
			queue = append(queue, batch...)
			continue
		}
		_, next := m.Update(msg)
		queue = append(queue, next)
	}
}

func TestChatModel_submitsQuestionAndStreamsAnswer(t *testing.T) {
	m, api := newTestChatModel(t)

	m.input.SetValue("what does fireball do")
	_, cmd := m.submit()
	drive(t, m, cmd)

	if m.streaming {
		t.Fatal("the turn should have finished")
	}
	transcript := m.transcriptText()
	if !strings.Contains(transcript, "> what does fireball do") {
		t.Fatalf("want the question echoed, got:\n%s", transcript)
	}
	if !strings.Contains(transcript, "Fireball explodes in fire") {
		t.Fatalf("want the streamed answer in the transcript, got:\n%s", transcript)
	}
	if !strings.Contains(transcript, "Sources:") {
		t.Fatalf("want a citation block, got:\n%s", transcript)
	}
	if len(m.sess.Turns) != 1 {
		t.Fatalf("want the turn persisted to the session, got %+v", m.sess.Turns)
	}
	if len(api.messages()) != 1 {
		t.Fatalf("want exactly one chat request, got %d", len(api.messages()))
	}

	// The saved session should be reloadable with the turn intact.
	again, err := m.cs.Load("test")
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Turns) != 1 || again.Turns[0].Answer == "" {
		t.Fatalf("the turn should have been saved to disk: %+v", again.Turns)
	}
}

func TestChatModel_rendersTheFinishedAnswerAsMarkdown(t *testing.T) {
	m, _ := newTestChatModel(t)
	rendered := m.renderAnswer("**bold** text")
	if strings.Contains(rendered, "**") {
		t.Fatalf("want markdown syntax rendered away, got: %q", rendered)
	}
	if !strings.Contains(rendered, "bold") {
		t.Fatalf("want the text itself preserved, got: %q", rendered)
	}
}

func TestChatModel_tabCompletesAnUnambiguousSlashCommand(t *testing.T) {
	m, _ := newTestChatModel(t)
	m.input.SetValue("/mod")
	if !m.completeSlashCommand() {
		t.Fatal("want completeSlashCommand to report it did something")
	}
	if m.input.Value() != "/model " {
		t.Fatalf("want /mod completed to \"/model \", got %q", m.input.Value())
	}
}

func TestChatModel_tabCompletesToTheSharedPrefixWhenAmbiguous(t *testing.T) {
	m, _ := newTestChatModel(t)
	m.input.SetValue("/s")
	// /scope, /sources, /source, /srd all start with /s but share no more
	// than "/s" itself — there's nothing further to fill in, so this
	// reports no completion happened, same as an unmatched prefix.
	if m.completeSlashCommand() {
		t.Fatal("want no completion when the shared prefix is no longer than what's already typed")
	}
	if m.input.Value() != "/s" {
		t.Fatalf("want /s left untouched, got %q", m.input.Value())
	}

	m.input.SetValue("/so")
	m.completeSlashCommand()
	// /sources and /source share "/source".
	if m.input.Value() != "/source" {
		t.Fatalf("want /so completed to the shared prefix /source, got %q", m.input.Value())
	}
}

func TestChatModel_tabDoesNothingOutsideASlashCommand(t *testing.T) {
	m, _ := newTestChatModel(t)
	m.input.SetValue("what does fireball do")
	if m.completeSlashCommand() {
		t.Fatal("want no completion for plain text")
	}
	if m.input.Value() != "what does fireball do" {
		t.Fatalf("want the input untouched, got %q", m.input.Value())
	}

	m.input.SetValue("/model gpt-")
	if m.completeSlashCommand() {
		t.Fatal("want no completion once the command's arguments have started")
	}
}

func TestChatModel_slashSuggestionsLineListsMatches(t *testing.T) {
	m, _ := newTestChatModel(t)
	m.input.SetValue("/mo")
	line := m.slashSuggestionsLine()
	if !strings.Contains(line, "/model") {
		t.Fatalf("want /model suggested, got %q", line)
	}

	m.input.SetValue("/model")
	if got := m.slashSuggestionsLine(); got != "" {
		t.Fatalf("want no suggestion once the command is typed out exactly, got %q", got)
	}

	m.input.SetValue("")
	if got := m.slashSuggestionsLine(); got != "" {
		t.Fatalf("want no suggestion for an empty input, got %q", got)
	}

	m.input.SetValue("what does fireball do")
	if got := m.slashSuggestionsLine(); got != "" {
		t.Fatalf("want no suggestion for plain text, got %q", got)
	}
}

func TestChatModel_slashHistoryRendersPastAnswersAsMarkdown(t *testing.T) {
	m, _ := newTestChatModel(t)
	m.sess.Turns = append(m.sess.Turns, chat.Record{
		Question: "what does fireball do",
		Answer:   "**bold** and *italic* text",
	})

	m.input.SetValue("/history")
	m.submit()

	transcript := m.transcriptText()
	if strings.Contains(transcript, "**bold**") || strings.Contains(transcript, "*italic*") {
		t.Fatalf("want /history's past answer rendered as Markdown, got literal syntax:\n%s", transcript)
	}
	if !strings.Contains(transcript, "bold") || !strings.Contains(transcript, "italic") {
		t.Fatalf("want the answer's text still present after rendering, got:\n%s", transcript)
	}
}

func TestChatModel_mouseIsOffByDefaultAndCtrlTTogglesIt(t *testing.T) {
	m, _ := newTestChatModel(t)
	if m.mouseEnabled {
		t.Fatal("want mouse reporting off by default, so click-drag select/copy works out of the box")
	}
	if m.View().MouseMode != tea.MouseModeNone {
		t.Fatalf("want MouseModeNone by default, got %v", m.View().MouseMode)
	}

	m.handleKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if !m.mouseEnabled {
		t.Fatal("want Ctrl-T to enable mouse reporting")
	}
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("want MouseModeCellMotion once enabled, got %v", m.View().MouseMode)
	}

	m.handleKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if m.mouseEnabled {
		t.Fatal("want a second Ctrl-T to disable mouse reporting again")
	}
}

func TestChatModel_scrollKeysMoveTheViewportWithoutTouchingHistory(t *testing.T) {
	m, _ := newTestChatModel(t)
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	for i := 0; i < 30; i++ {
		m.writeLine(fmt.Sprintf("line %d", i))
	}
	m.refreshViewport()
	m.viewport.GotoBottom()
	if m.viewport.AtTop() {
		t.Fatal("test setup: want the viewport scrolled away from the top")
	}

	m.handleKey(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.viewport.AtBottom() {
		t.Fatal("want PgUp to have scrolled the viewport up")
	}
	if m.input.Value() != "" {
		t.Fatalf("scrolling must not touch the input, got %q", m.input.Value())
	}
}

func TestChatModel_wrapsLongLinesToTheViewportWidth(t *testing.T) {
	m, _ := newTestChatModel(t)
	m.Update(tea.WindowSizeMsg{Width: 20, Height: 24})

	long := strings.Repeat("word ", 20)
	m.writeLine(long)
	m.refreshViewport()

	view := m.viewport.View()
	for _, line := range strings.Split(view, "\n") {
		if lipgloss.Width(line) > 20 {
			t.Fatalf("want no rendered line wider than the viewport (20), got %d: %q", lipgloss.Width(line), line)
		}
	}
	if !strings.Contains(view, "word") {
		t.Fatalf("want the content still present after wrapping, got:\n%s", view)
	}
}

func TestChatModel_showsAThinkingIndicatorBeforeTheFirstToken(t *testing.T) {
	m, _ := newTestChatModel(t)
	m.input.SetValue("what does fireball do")
	m.submit()
	if !strings.Contains(m.viewport.View(), "thinking") {
		t.Fatalf("want a thinking indicator while no tokens have arrived yet, got:\n%s", m.viewport.View())
	}
}

func TestChatModel_slashModelOverridesAndPersistsTheModel(t *testing.T) {
	m, api := newTestChatModelWithProvider(t)

	m.input.SetValue("/model gpt-x")
	m.submit()
	if !strings.Contains(m.transcriptText(), "model gpt-x (saved to openai)") {
		t.Fatalf("want the switch confirmed as saved in the transcript, got:\n%s", m.transcriptText())
	}
	if m.cfg.AskModel != "gpt-x" {
		t.Fatalf("want the model applied to the workspace's config, got %q", m.cfg.AskModel)
	}
	cred, ok := providerCredentialForTest(t, "openai")
	if !ok || cred.ChatModel != "gpt-x" {
		t.Fatalf("want the model persisted to the stored provider, got %+v ok=%v", cred, ok)
	}

	m.input.SetValue("what does fireball do")
	_, cmd := m.submit()
	drive(t, m, cmd)
	if got := api.models(); len(got) != 1 || got[0] != "gpt-x" {
		t.Fatalf("want the turn sent with the overridden model, got %v", got)
	}
}

func TestChatModel_slashModelErrorsWithoutAnActiveProviderToSaveTo(t *testing.T) {
	m, _ := newTestChatModel(t) // providerName is "" here, same as an env-var-only setup
	m.input.SetValue("/model gpt-x")
	m.submit()
	if !strings.Contains(m.transcriptText(), "no active stored provider") {
		t.Fatalf("want an error explaining there's nowhere to persist the model, got:\n%s", m.transcriptText())
	}
}

func TestChatModel_ignoresSubmitWhileATurnIsInFlight(t *testing.T) {
	m, api := newTestChatModel(t)

	m.input.SetValue("what does fireball do")
	_, cmd := m.submit()
	if !m.streaming {
		t.Fatal("want the model to be streaming immediately after submit")
	}

	// A second submit while busy must be a no-op: no second history entry,
	// no second request.
	m.input.SetValue("another question")
	_, second := m.submit()
	if second != nil {
		t.Fatal("submit while streaming should return no command")
	}
	if len(m.history) != 1 {
		t.Fatalf("a submit while streaming should not be recorded in history: %+v", m.history)
	}

	drive(t, m, cmd)
	if len(api.messages()) != 1 {
		t.Fatalf("want exactly one chat request, got %d", len(api.messages()))
	}
}

func TestChatModel_slashCommandRunsSynchronouslyAndDoesNotStream(t *testing.T) {
	m, api := newTestChatModel(t)

	m.input.SetValue("/note the party sold the Sunsword")
	_, cmd := m.submit()
	if cmd != nil {
		t.Fatal("a slash command should complete synchronously, with no follow-up command")
	}
	if m.streaming {
		t.Fatal("a slash command must never set streaming")
	}
	if !strings.Contains(m.transcriptText(), "noted (1") {
		t.Fatalf("want the note confirmation in the transcript, got:\n%s", m.transcriptText())
	}
	if len(m.sess.Notes) != 1 {
		t.Fatalf("want the note recorded on the session, got %+v", m.sess.Notes)
	}
	if len(api.messages()) != 0 {
		t.Fatal("a slash command must never reach the chat API")
	}
}

func TestChatModel_slashExitQuits(t *testing.T) {
	m, _ := newTestChatModel(t)
	m.input.SetValue("/exit")
	_, cmd := m.submit()
	if !m.quitting {
		t.Fatal("/exit should set quitting")
	}
	if cmd == nil {
		t.Fatal("/exit should return tea.Quit")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("want tea.QuitMsg, got %T", msg)
	}
}

func TestChatModel_ctrlCCancelsAnInFlightTurnWithoutQuitting(t *testing.T) {
	m, _ := newTestChatModel(t)

	m.input.SetValue("what does fireball do")
	_, cmd := m.submit()
	if !m.streaming {
		t.Fatal("want the model streaming")
	}

	// Cancel via the same path Ctrl-C takes.
	m.cancel()
	drive(t, m, cmd)

	if m.quitting {
		t.Fatal("cancelling a turn must not quit the workspace")
	}
	if m.streaming {
		t.Fatal("want the turn to have finished (cancelled)")
	}
	if !strings.Contains(m.transcriptText(), "cancelled") {
		t.Fatalf("want a cancellation notice in the transcript, got:\n%s", m.transcriptText())
	}
	if len(m.sess.Turns) != 0 {
		t.Fatalf("a cancelled turn must not be persisted: %+v", m.sess.Turns)
	}
}

func TestChatModel_historyRecallCyclesSubmittedLines(t *testing.T) {
	m, _ := newTestChatModel(t)
	m.input.SetValue("/note first")
	m.submit()
	m.input.SetValue("/note second")
	m.submit()

	m.recallHistory(-1)
	if m.input.Value() != "/note second" {
		t.Fatalf("want the most recent line first, got %q", m.input.Value())
	}
	m.recallHistory(-1)
	if m.input.Value() != "/note first" {
		t.Fatalf("want the older line next, got %q", m.input.Value())
	}
	m.recallHistory(-1) // already at the oldest; must not go further back or panic
	if m.input.Value() != "/note first" {
		t.Fatalf("recalling past the oldest entry should stay put, got %q", m.input.Value())
	}
	m.recallHistory(1)
	m.recallHistory(1)
	if m.input.Value() != "" {
		t.Fatalf("cycling back past the newest entry should return to the empty draft, got %q", m.input.Value())
	}
}

func TestChatWorkspaceAvailable_falseWithoutATTY(t *testing.T) {
	cmd := &cobra.Command{}
	var out strings.Builder
	cmd.SetOut(&out)
	if chatWorkspaceAvailable(cmd, false) {
		t.Fatal("a non-TTY stdout must never get the workspace")
	}
}

func TestChatWorkspaceAvailable_falseInJSONMode(t *testing.T) {
	// Even a hypothetical TTY writer must not activate the workspace in
	// --json mode; there is no TTY writer available in a unit test, so this
	// only exercises the asJSON short-circuit, which chatWorkspaceAvailable
	// evaluates before ever looking at isTTY.
	cmd := &cobra.Command{}
	if chatWorkspaceAvailable(cmd, true) {
		t.Fatal("--json must never get the workspace")
	}
}

// Esc cancels an in-flight turn but never quits; only Ctrl-C (or Ctrl-D)
// leaves the workspace.
func TestChatModel_escCancelsTurnWithoutQuitting(t *testing.T) {
	m, _ := newTestChatModel(t)
	cancelled := false
	m.streaming = true
	m.cancel = func() { cancelled = true }
	if _, cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc}); cmd != nil || m.quitting {
		t.Fatal("esc should not quit")
	}
	if !cancelled {
		t.Fatal("esc should cancel the in-flight turn")
	}
	m.streaming = false
	if _, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc}); m.quitting {
		t.Fatal("esc while idle should not quit")
	}
	if _, _ = m.handleKey(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}); !m.quitting {
		t.Fatal("ctrl+c should quit")
	}
}

// The input uses the terminal's real cursor, placed on the input row at the
// cursor's column, so Left/Right never re-renders the line's text.
func TestChatModel_realCursorTracksInput(t *testing.T) {
	m, _ := newTestChatModel(t)
	for _, r := range "hello" {
		m.handleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	before := m.View()
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyLeft})
	after := m.View()
	if before.Cursor == nil || after.Cursor == nil {
		t.Fatal("view should place the real cursor")
	}
	if ansi.Strip(before.Content) != ansi.Strip(after.Content) {
		t.Fatal("moving the cursor should not change the rendered text")
	}
	if after.Cursor.X != before.Cursor.X-1 {
		t.Fatalf("cursor x = %d, want %d", after.Cursor.X, before.Cursor.X-1)
	}
	lines := strings.Split(after.Content, "\n")
	if row := ansi.Strip(lines[after.Cursor.Y]); !strings.Contains(row, "hello") {
		t.Fatalf("cursor row %d is %q, not the input", after.Cursor.Y, row)
	}
}

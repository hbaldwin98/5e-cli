package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

func TestChat_oneShotPersistsTheTurn(t *testing.T) {
	index, _ := chatFixture(t)
	dir := filepath.Join(t.TempDir(), "chats")
	data := filepath.Join(t.TempDir(), "missing-data")

	out, err := runCLI("--index", index, "--data", data, "chat", "--chat-dir", dir, "--session", "table one", "what does fireball do")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Fireball") {
		t.Fatalf("answer: %s", out)
	}

	out, err = runCLI("--index", index, "--data", data, "--json", "chat", "--chat-dir", dir, "list")
	if err != nil {
		t.Fatal(err)
	}
	var list []map[string]any
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0]["name"] != "table one" || list[0]["turns"].(float64) != 1 {
		t.Fatalf("list: %s", out)
	}

	out, err = runCLI("--index", index, "--data", data, "chat", "--chat-dir", dir, "show", "table one")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "what does fireball do") || !strings.Contains(out, "Fireball") {
		t.Fatalf("show: %s", out)
	}
}

func TestChat_replCarriesNotesAndHistoryIntoLaterTurns(t *testing.T) {
	index, api := chatFixture(t)
	dir := filepath.Join(t.TempDir(), "chats")
	data := filepath.Join(t.TempDir(), "missing-data")

	script := strings.Join([]string{
		"/note the party's wizard is a tiefling named Rekt",
		"what does fireball do",
		"how much damage does it do",
		"/notes",
		"/exit",
	}, "\n") + "\n"
	out, err := runCLIStdin(script, "--index", index, "--data", data, "chat", "--chat-dir", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "noted (1)") {
		t.Fatalf("note was not recorded: %s", out)
	}
	if !strings.Contains(out, "1. the party's wizard is a tiefling named Rekt") {
		t.Fatalf("/notes did not list the note: %s", out)
	}

	msgs := api.messages()
	if len(msgs) != 2 {
		t.Fatalf("want one chat call per question, got %d", len(msgs))
	}
	first, second := msgs[0], msgs[1]
	if len(second) <= len(first) {
		t.Fatalf("the second turn should carry the first exchange: %+v", second)
	}
	var sawEarlierAnswer bool
	for _, m := range second {
		if m.Role == "assistant" && strings.Contains(m.Content, "Fireball") {
			sawEarlierAnswer = true
		}
	}
	if !sawEarlierAnswer {
		t.Fatalf("the earlier answer was not sent back as history: %+v", second)
	}
	if !strings.Contains(second[len(second)-1].Content, "Rekt") {
		t.Fatalf("the note was not sent with the question:\n%s", second[len(second)-1].Content)
	}
	// The follow-up says nothing retrievable on its own; the session is what
	// keeps Fireball in front of the model.
	if !strings.Contains(second[len(second)-1].Content, "A bright streak flashes") {
		t.Fatalf("the follow-up lost its sources:\n%s", second[len(second)-1].Content)
	}
}

func TestChat_appliesEditionPreferenceToRetrieval(t *testing.T) {
	index, api := chatFixture(t)
	dir := filepath.Join(t.TempDir(), "chats")
	data := filepath.Join(t.TempDir(), "missing-data")

	for _, args := range [][]string{
		{"--edition", "2014", "--session", "classic"},
		{"--edition", "2024", "--session", "modern"},
	} {
		command := []string{"--index", index, "--data", data, "chat", "--chat-dir", dir}
		command = append(command, args...)
		command = append(command, "what does fireball do")
		if _, err := runCLI(command...); err != nil {
			t.Fatal(err)
		}
	}

	msgs := api.messages()
	if len(msgs) != 2 {
		t.Fatalf("chat calls = %d, want 2", len(msgs))
	}
	classic, modern := msgs[0][len(msgs[0])-1].Content, msgs[1][len(msgs[1])-1].Content
	if !strings.Contains(classic, "spell Fireball (PHB)") || strings.Contains(classic, "spell Fireball (XPHB)") {
		t.Fatalf("classic chat prompt used the wrong edition:\n%s", classic)
	}
	if !strings.Contains(modern, "spell Fireball (XPHB)") || strings.Contains(modern, "spell Fireball (PHB)") {
		t.Fatalf("modern chat prompt used the wrong edition:\n%s", modern)
	}
}

func TestChat_replSurvivesAFailedTurn(t *testing.T) {
	index, api := chatFixture(t)
	api.fail(true)
	dir := filepath.Join(t.TempDir(), "chats")
	data := filepath.Join(t.TempDir(), "missing-data")

	out, err := runCLIStdin("what does fireball do\n/notes\n", "--index", index, "--data", data, "chat", "--chat-dir", dir)
	if err != nil {
		t.Fatalf("a failed question should not fail the session: %v", err)
	}
	if !strings.Contains(out, "no notes") {
		t.Fatalf("the session should have kept going: %s", out)
	}

	out, err = runCLI("--index", index, "--data", data, "--json", "chat", "--chat-dir", dir, "show")
	if err != nil {
		t.Fatal(err)
	}
	var sess map[string]any
	if err := json.Unmarshal([]byte(out), &sess); err != nil {
		t.Fatal(err)
	}
	if sess["turns"] != nil {
		t.Fatalf("a failed answer must not enter the transcript: %s", out)
	}
}

func TestChat_jsonREPLIsAStreamOfTypedEvents(t *testing.T) {
	index, _ := chatFixture(t)
	dir := filepath.Join(t.TempDir(), "chats")
	data := filepath.Join(t.TempDir(), "missing-data")
	script := strings.Join([]string{
		"/help",
		"/note the party's wizard is a tiefling named Rekt",
		"/notes",
		"what does fireball do",
		"/nope",
		"/sources",
		"/limit 3",
		"/clear",
		"/exit",
	}, "\n") + "\n"

	out, err := runCLIStdin(script, "--index", index, "--data", data, "--json", "chat", "--chat-dir", dir)
	if err != nil {
		t.Fatal(err)
	}

	dec := json.NewDecoder(strings.NewReader(out))
	var types []string
	for {
		var event map[string]json.RawMessage
		err := dec.Decode(&event)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("JSON REPL output is not a stream of objects: %v\n%s", err, out)
		}
		var typ string
		if err := json.Unmarshal(event["type"], &typ); err != nil {
			t.Fatalf("event has no type: %s", out)
		}
		types = append(types, typ)
	}
	want := []string{"command", "command", "command", "turn", "error", "command", "command", "command", "command"}
	if strings.Join(types, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("event types = %v, want %v\n%s", types, want, out)
	}
}

func TestChat_jsonREPLReportsFailedQuestionsAsEvents(t *testing.T) {
	index, api := chatFixture(t)
	api.fail(true)
	dir := filepath.Join(t.TempDir(), "chats")
	data := filepath.Join(t.TempDir(), "missing-data")

	out, err := runCLIStdin("what does fireball do\n/exit\n", "--index", index, "--data", data, "--json", "chat", "--chat-dir", dir)
	if err != nil {
		t.Fatal(err)
	}

	dec := json.NewDecoder(strings.NewReader(out))
	var events []map[string]json.RawMessage
	for {
		var event map[string]json.RawMessage
		err := dec.Decode(&event)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("JSON REPL output is not a stream of objects: %v\n%s", err, out)
		}
		events = append(events, event)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2\n%s", len(events), out)
	}
	var typ, question string
	if err := json.Unmarshal(events[0]["type"], &typ); err != nil || typ != "error" {
		t.Fatalf("first event type = %q: %s", typ, out)
	}
	if err := json.Unmarshal(events[0]["question"], &question); err != nil || question != "what does fireball do" {
		t.Fatalf("failed question = %q: %s", question, out)
	}
	if err := json.Unmarshal(events[1]["type"], &typ); err != nil || typ != "command" {
		t.Fatalf("second event type = %q: %s", typ, out)
	}
}

func TestChat_noteSubcommandFeedsTheNextQuestion(t *testing.T) {
	index, api := chatFixture(t)
	dir := filepath.Join(t.TempDir(), "chats")
	data := filepath.Join(t.TempDir(), "missing-data")

	if _, err := runCLI("--index", index, "--data", data, "chat", "--chat-dir", dir, "note", "the tavern burned down"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI("--index", index, "--data", data, "chat", "--chat-dir", dir, "what does fireball do"); err != nil {
		t.Fatal(err)
	}
	msgs := api.messages()
	if len(msgs) != 1 {
		t.Fatalf("chat calls %d", len(msgs))
	}
	if !strings.Contains(msgs[0][len(msgs[0])-1].Content, "the tavern burned down") {
		t.Fatalf("the note did not reach the model:\n%s", msgs[0][len(msgs[0])-1].Content)
	}
}

func TestChat_rejectsAnUnknownSlashCommand(t *testing.T) {
	index, _ := chatFixture(t)
	dir := filepath.Join(t.TempDir(), "chats")
	data := filepath.Join(t.TempDir(), "missing-data")

	out, err := runCLIStdin("/nope\n/exit\n", "--index", index, "--data", data, "chat", "--chat-dir", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "unknown command /nope") {
		t.Fatalf("out: %s", out)
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatAPI is an OpenAI-compatible stand-in that records every chat request, so
// a test can assert what the conversation actually sent.
type chatAPI struct {
	mu       sync.Mutex
	requests [][]chatMessage
	broken   bool
}

func (a *chatAPI) messages() [][]chatMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([][]chatMessage(nil), a.requests...)
}

func (a *chatAPI) fail(v bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.broken = v
}

func chatFixture(t *testing.T) (index string, api *chatAPI) {
	t.Helper()
	index = filepath.Join(t.TempDir(), "index.sqlite")
	err := store.Create(index, store.Meta{SHA: "chat", DataRoot: t.TempDir(), IngestedAt: store.Now()}, []parse.Entity{
		{Kind: "spell", Name: "Fireball", Source: "PHB", SRD: true, JSON: json.RawMessage(`{}`), Text: "A bright streak flashes and explodes in fire and flame."},
		{Kind: "spell", Name: "Fireball", Source: "XPHB", SRD: true, JSON: json.RawMessage(`{}`), Text: "A bright streak flashes and explodes in fire and flame."},
		{Kind: "item", Name: "Longsword", Source: "PHB", JSON: json.RawMessage(`{}`), Text: "A martial melee weapon with a steel blade."},
		{Kind: "adventure", Name: "Lost Mine of Testing", Source: "LMoP", JSON: json.RawMessage(`{}`), Text: "phandelver"},
	}, []parse.Document{
		{Kind: "adventureSection", ParentID: "LMoP", Section: "Cragmaw Hideout", JSON: json.RawMessage(`{}`), Text: "Goblins nest in the Cragmaw hideout in the hills."},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	api = &chatAPI{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			var req struct {
				Messages []chatMessage `json:"messages"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			api.mu.Lock()
			broken := api.broken
			if !broken {
				api.requests = append(api.requests, req.Messages)
			}
			api.mu.Unlock()
			if broken {
				http.Error(w, "rate limited", http.StatusTooManyRequests)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{
					{"message": map[string]any{"content": "Fireball explodes in fire (spell, Fireball, PHB)."}},
				},
			})
			return
		}
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		type row struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var data []row
		for i, s := range req.Input {
			v := []float32{0.05, 0.05, 0.05}
			low := strings.ToLower(s)
			if strings.Contains(low, "fire") || strings.Contains(low, "flame") {
				v = []float32{1, 0, 0}
			}
			if strings.Contains(low, "sword") || strings.Contains(low, "blade") {
				v = []float32{0, 1, 0}
			}
			if strings.Contains(low, "goblin") || strings.Contains(low, "cragmaw") {
				v = []float32{0, 0, 1}
			}
			data = append(data, row{Index: i, Embedding: v})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_API_KEY", "test")
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("FIVE_E_EMBED_MODEL", "fake-embed")
	t.Setenv("FIVE_E_ASK_MODEL", "fake-ask")
	t.Setenv("FIVE_E_EMBEDDINGS", filepath.Join(t.TempDir(), "embeddings.sqlite"))
	return index, api
}

// runCLIStdin drives the REPL from a script and folds stderr into the output,
// since the loop reports a failed turn there and keeps going.
func runCLIStdin(stdin string, args ...string) (string, error) {
	cmd := rootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestChat_clearDropsHistoryAndKeepsNotes(t *testing.T) {
	index, api := chatFixture(t)
	dir := filepath.Join(t.TempDir(), "chats")
	data := filepath.Join(t.TempDir(), "missing-data")
	base := []string{"--index", index, "--data", data, "chat", "--chat-dir", dir}

	if _, err := runCLI(append(base, "note", "the tavern burned down")...); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(append(base, "what does fireball do")...); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(append(base, "clear")...)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "cleared 1 turn") || !strings.Contains(out, "1 note kept") {
		t.Fatalf("clear: %s", out)
	}
	if _, err := runCLI(append(base, "what does fireball do")...); err != nil {
		t.Fatal(err)
	}

	msgs := api.messages()
	if len(msgs) != 2 {
		t.Fatalf("chat calls %d", len(msgs))
	}
	second := msgs[1]
	if len(second) != 2 {
		t.Fatalf("a cleared session should send only the system prompt and the question: %+v", second)
	}
	if !strings.Contains(second[1].Content, "the tavern burned down") {
		t.Fatalf("clearing the transcript must not drop the notes:\n%s", second[1].Content)
	}

	// The session is emptied, not deleted.
	out, err = runCLI("--index", index, "--data", data, "--json", "chat", "--chat-dir", dir, "list")
	if err != nil {
		t.Fatal(err)
	}
	var list []map[string]any
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0]["turns"].(float64) != 1 || list[0]["notes"].(float64) != 1 {
		t.Fatalf("list after clear: %s", out)
	}
}

func TestChat_showAndClearRefuseAnUnknownSession(t *testing.T) {
	index, _ := chatFixture(t)
	dir := filepath.Join(t.TempDir(), "chats")
	data := filepath.Join(t.TempDir(), "missing-data")
	base := []string{"--index", index, "--data", data, "chat", "--chat-dir", dir}

	if _, err := runCLI(append(base, "show", "never-created")...); err == nil {
		t.Fatal("show on an unknown session should error, not silently create one")
	}
	if _, err := runCLI(append(base, "clear", "never-created")...); err == nil {
		t.Fatal("clear on an unknown session should error, not silently create one")
	}
	out, err := runCLI("--index", index, "--data", data, "--json", "chat", "--chat-dir", dir, "list")
	if err != nil {
		t.Fatal(err)
	}
	var list []map[string]any
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("show/clear must not create a session as a side effect: %s", out)
	}
}

func TestChat_renameExportImport(t *testing.T) {
	index, _ := chatFixture(t)
	dir := filepath.Join(t.TempDir(), "chats")
	data := filepath.Join(t.TempDir(), "missing-data")
	base := []string{"--index", index, "--data", data, "chat", "--chat-dir", dir}

	if _, err := runCLI(append(base, "--session", "curse-of-strahd", "note", "the party sold the Sunsword")...); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(append(base, "--session", "curse-of-strahd", "what does fireball do")...); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(append(base, "rename", "curse-of-strahd", "cos-campaign")...)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `renamed "curse-of-strahd" to "cos-campaign"`) {
		t.Fatalf("rename: %s", out)
	}
	if _, err := runCLI(append(base, "show", "curse-of-strahd")...); err == nil {
		t.Fatal("the old name should be gone, not silently re-created by show")
	}

	exportFile := filepath.Join(t.TempDir(), "exported.json")
	out, err = runCLI(append(base, "export", "cos-campaign", "--out", exportFile)...)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "exported cos-campaign to "+exportFile) {
		t.Fatalf("export: %s", out)
	}
	raw, err := os.ReadFile(exportFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "the party sold the Sunsword") {
		t.Fatalf("export file is missing the note:\n%s", raw)
	}

	otherDir := filepath.Join(t.TempDir(), "other-chats")
	otherBase := []string{"--index", index, "--data", data, "chat", "--chat-dir", otherDir}
	out, err = runCLI(append(otherBase, "import", exportFile)...)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `imported `+exportFile+` as "cos-campaign"`) {
		t.Fatalf("import: %s", out)
	}
	if _, err := runCLI(append(otherBase, "import", exportFile)...); err == nil {
		t.Fatal("importing onto an existing session without --force should be refused")
	}

	out, err = runCLI(append(otherBase, "show", "cos-campaign")...)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "the party sold the Sunsword") {
		t.Fatalf("imported session is missing its note:\n%s", out)
	}
}

func TestChat_clearNotesFlagAndSlashCommand(t *testing.T) {
	index, _ := chatFixture(t)
	dir := filepath.Join(t.TempDir(), "chats")
	data := filepath.Join(t.TempDir(), "missing-data")
	base := []string{"--index", index, "--data", data, "chat", "--chat-dir", dir}

	if _, err := runCLI(append(base, "note", "the tavern burned down")...); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(append(base, "clear", "--notes")...)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "and 1 note") {
		t.Fatalf("clear --notes should say what it dropped: %s", out)
	}

	out, err = runCLIStdin("/note a fresh fact\n/clear all\n/notes\n/exit\n", base...)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "and 1 note") || !strings.Contains(out, "no notes") {
		t.Fatalf("/clear all: %s", out)
	}
	if out, err = runCLIStdin("/clear sideways\n/exit\n", base...); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(out, "usage: /clear [all]") {
		t.Fatalf("/clear should reject an unknown argument: %s", out)
	}
}

func TestChat_adventureScopedSessionStillAnswersFromTheRulebooks(t *testing.T) {
	index, api := chatFixture(t)
	dir := filepath.Join(t.TempDir(), "chats")
	data := filepath.Join(t.TempDir(), "missing-data")
	base := []string{"--index", index, "--data", data, "chat", "--chat-dir", dir, "--adventure", "LMoP"}

	out, err := runCLI(append(base, "what does fireball do")...)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Fireball") {
		t.Fatalf("answer: %s", out)
	}
	msgs := api.messages()
	if len(msgs) != 1 {
		t.Fatalf("chat calls %d", len(msgs))
	}
	// A module-scoped session must still reach the rules: this is the whole
	// point of scoping widening the corpus instead of replacing it.
	if !strings.Contains(msgs[0][1].Content, "A bright streak flashes") {
		t.Fatalf("the scoped session lost the PHB:\n%s", msgs[0][1].Content)
	}

	// The module's prose is reachable from the same session.
	if _, err := runCLI(append(base, "what is in the cragmaw hideout")...); err != nil {
		t.Fatal(err)
	}
	msgs = api.messages()
	if !strings.Contains(msgs[len(msgs)-1][len(msgs[len(msgs)-1])-1].Content, "Goblins nest in the Cragmaw") {
		t.Fatalf("module prose is not reachable:\n%s", msgs[len(msgs)-1])
	}
}

func TestChat_adventureOnlyExcludesTheRulebooks(t *testing.T) {
	index, api := chatFixture(t)
	dir := filepath.Join(t.TempDir(), "chats")
	data := filepath.Join(t.TempDir(), "missing-data")
	base := []string{"--index", index, "--data", data, "chat", "--chat-dir", dir}

	if _, err := runCLI(append(base, "--adventure", "LMoP", "--adventure-only", "what does fireball do")...); err != nil {
		t.Fatal(err)
	}
	msgs := api.messages()
	if strings.Contains(msgs[0][1].Content, "A bright streak flashes") {
		t.Fatalf("adventure-only admitted the PHB:\n%s", msgs[0][1].Content)
	}

	// The narrower scope is the session's, so it holds for later turns too.
	out, err := runCLI("--index", index, "--data", data, "chat", "--chat-dir", dir, "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[LMoP only]") {
		t.Fatalf("list should show the narrower scope: %s", out)
	}

	if _, err := runCLI(append(base, "--adventure-only", "what does fireball do")...); err == nil {
		t.Fatal("--adventure-only without --adventure should be refused")
	}
}

func TestChat_slashAdventureOnlyWithAMultiwordTitle(t *testing.T) {
	index, _ := chatFixture(t)
	dir := filepath.Join(t.TempDir(), "chats")
	data := filepath.Join(t.TempDir(), "missing-data")
	base := []string{"--index", index, "--data", data, "chat", "--chat-dir", dir}

	// The fixture's adventure title is itself multiword ("Lost Mine of
	// Testing"); the trailing "only" must be recognized by its position at
	// the end of the input, not by splitting on the first space.
	out, err := runCLIStdin("/adventure Lost Mine of Testing only\n/adventure\n/exit\n", base...)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "scoped to LMoP only\n") {
		t.Fatalf("multiword title with only was not parsed correctly:\n%s", out)
	}
}

func TestChat_slashAdventureWidensAndNarrows(t *testing.T) {
	index, _ := chatFixture(t)
	dir := filepath.Join(t.TempDir(), "chats")
	data := filepath.Join(t.TempDir(), "missing-data")
	base := []string{"--index", index, "--data", data, "chat", "--chat-dir", dir}

	out, err := runCLIStdin("/adventure LMoP\n/adventure\n/adventure LMoP only\n/adventure\n/adventure none\n/adventure\n/exit\n", base...)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"scoped to LMoP\n", "scoped to LMoP only\n", "adventure scope cleared", "not scoped to an adventure"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

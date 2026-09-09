package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/ask"
)

func TestStore_savesAndResumesASession(t *testing.T) {
	cs := testStore(t)

	sess, err := cs.Load("Curse of Strahd")
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Turns) != 0 || sess.Name != "Curse of Strahd" {
		t.Fatalf("a new session should be empty and keep its name: %+v", sess)
	}
	sess.AddNote("the party sold the Sunsword")
	sess.AddTurn("who is Strahd", ask.Result{
		Answer:    "A vampire.",
		Citations: []ask.Hit{{Kind: "monster", Name: "Strahd", Source: "CoS"}},
	})
	sess.Adventure = "CoS"
	if err := cs.Save(sess); err != nil {
		t.Fatal(err)
	}

	again, err := cs.Load("curse of strahd")
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Turns) != 1 || again.Turns[0].Answer != "A vampire." {
		t.Fatalf("transcript did not survive: %+v", again.Turns)
	}
	if len(again.Notes) != 1 || again.Notes[0].Text != "the party sold the Sunsword" {
		t.Fatalf("notes did not survive: %+v", again.Notes)
	}
	if again.Adventure != "CoS" {
		t.Fatalf("adventure scope did not survive: %q", again.Adventure)
	}
	if again.Name != "Curse of Strahd" {
		t.Fatalf("resuming by slug should keep the written name, got %q", again.Name)
	}
}

func TestStore_listReportsMostRecentFirst(t *testing.T) {
	cs := testStore(t)
	for _, name := range []string{"one", "two"} {
		sess, err := cs.Load(name)
		if err != nil {
			t.Fatal(err)
		}
		sess.AddTurn("q", ask.Result{Answer: "a"})
		if err := cs.Save(sess); err != nil {
			t.Fatal(err)
		}
	}
	list, _, err := cs.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("want two sessions, got %+v", list)
	}
	if list[0].Name != "two" {
		t.Fatalf("want the newest session first, got %+v", list)
	}
	if list[0].Turns != 1 {
		t.Fatalf("summary should count turns: %+v", list[0])
	}

	if err := cs.Delete("two"); err != nil {
		t.Fatal(err)
	}
	if err := cs.Delete("two"); err != nil {
		t.Fatalf("deleting a missing session should not fail: %v", err)
	}
	list, _, err = cs.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "one" {
		t.Fatalf("delete removed the wrong session: %+v", list)
	}
}

func TestStore_pathStaysInsideTheChatDirectory(t *testing.T) {
	cs := testStore(t)
	path, err := cs.Path("../../etc/passwd")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != cs.Dir() {
		t.Fatalf("session name escaped the directory: %s", path)
	}
	if _, err := cs.Path("///"); err == nil {
		t.Fatal("a name with no usable characters should be refused")
	}
}

func TestStore_distinguishesNamesWithTheSameSlug(t *testing.T) {
	cs := testStore(t)
	names := []string{"a/b", "a b", "a-b"}
	paths := make(map[string]string, len(names))
	for _, name := range names {
		sess, err := cs.Load(name)
		if err != nil {
			t.Fatal(err)
		}
		sess.AddNote(name)
		if err := cs.Save(sess); err != nil {
			t.Fatal(err)
		}
		path, err := cs.Path(name)
		if err != nil {
			t.Fatal(err)
		}
		paths[name] = path
	}

	if paths[names[0]] == paths[names[1]] || paths[names[1]] == paths[names[2]] || paths[names[0]] == paths[names[2]] {
		t.Fatalf("same-slug sessions share a path: %v", paths)
	}
	for _, name := range names {
		sess, err := cs.Load(name)
		if err != nil {
			t.Fatal(err)
		}
		if len(sess.Notes) != 1 || sess.Notes[0].Text != name {
			t.Fatalf("loaded the wrong session for %q: %+v", name, sess)
		}
	}
}

func TestStore_loadsAndMigratesLegacySession(t *testing.T) {
	cs := testStore(t)
	legacy, err := cs.legacyPath("Old Name")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFile(legacy, `{"name":"Old Name","created":"created","updated":"updated"}`); err != nil {
		t.Fatal(err)
	}

	sess, err := cs.Load("old name")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Name != "Old Name" {
		t.Fatalf("legacy session name changed: %+v", sess)
	}
	path, err := cs.Path("Old Name")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("legacy session was not migrated: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy session still exists at %s", legacy)
	}
}

func TestStore_legacySlugCollisionDoesNotOpenAnotherSession(t *testing.T) {
	cs := testStore(t)
	legacy, err := cs.legacyPath("a-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFile(legacy, `{"name":"a-b","created":"created","updated":"updated","notes":[{"text":"wrong session"}]}`); err != nil {
		t.Fatal(err)
	}

	sess, err := cs.Load("a b")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Name != "a b" || len(sess.Notes) != 0 {
		t.Fatalf("legacy collision opened the wrong session: %+v", sess)
	}
	if err := cs.Save(sess); err != nil {
		t.Fatal(err)
	}
	legacySess, err := cs.Load("a-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(legacySess.Notes) != 1 || legacySess.Notes[0].Text != "wrong session" {
		t.Fatalf("legacy session was overwritten: %+v", legacySess)
	}
}

func TestStore_listSkipsAnUnreadableSession(t *testing.T) {
	cs := testStore(t)
	sess, err := cs.Load("good")
	if err != nil {
		t.Fatal(err)
	}
	sess.AddTurn("q", ask.Result{Answer: "a"})
	if err := cs.Save(sess); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(cs.Dir(), "broken.json"), "{not json"); err != nil {
		t.Fatal(err)
	}
	list, corrupt, err := cs.List()
	if err != nil {
		t.Fatalf("one broken file must not fail the listing: %v", err)
	}
	if len(list) != 1 || list[0].Name != "good" {
		t.Fatalf("got %+v", list)
	}
	if len(corrupt) != 1 || corrupt[0].File != "broken.json" || corrupt[0].Err == "" {
		t.Fatalf("the broken file should be reported, not just silently dropped: %+v", corrupt)
	}
}

func TestSession_historyPairsAnsweredTurnsOnly(t *testing.T) {
	sess := &Session{Name: "s"}
	sess.AddTurn("first", ask.Result{Answer: "one"})
	sess.Turns = append(sess.Turns, Record{Question: "unanswered", Answer: "  "})
	sess.AddTurn("second", ask.Result{Answer: "two"})

	got := sess.History()
	if len(got) != 4 {
		t.Fatalf("want two exchanges, got %+v", got)
	}
	roles := ""
	for _, m := range got {
		roles += m.Role[:1]
	}
	if roles != "uaua" {
		t.Fatalf("history must alternate user and assistant, got %q", roles)
	}
	for _, m := range got {
		if strings.Contains(m.Content, "unanswered") {
			t.Fatal("a turn with no answer is not history")
		}
	}
}

func TestSession_addNoteRefusesBlankText(t *testing.T) {
	sess := &Session{Name: "s"}
	if sess.AddNote("   ") {
		t.Fatal("blank text is not a note")
	}
	if !sess.AddNote("  a real note  ") || sess.Notes[0].Text != "a real note" {
		t.Fatalf("notes %+v", sess.Notes)
	}
}

func TestSession_removeNoteByPosition(t *testing.T) {
	sess := &Session{Name: "s"}
	sess.AddNote("first")
	sess.AddNote("second")
	sess.AddNote("third")

	if sess.RemoveNote(0) || sess.RemoveNote(4) {
		t.Fatal("an out-of-range position should be refused")
	}
	if !sess.RemoveNote(2) {
		t.Fatal("want position 2 removed")
	}
	if len(sess.Notes) != 2 || sess.Notes[0].Text != "first" || sess.Notes[1].Text != "third" {
		t.Fatalf("got %+v", sess.Notes)
	}
}

func TestSession_editNoteReplacesTextKeepsTimestamp(t *testing.T) {
	sess := &Session{Name: "s"}
	sess.AddNote("origonal typo")
	at := sess.Notes[0].At

	if sess.EditNote(0, "x") || sess.EditNote(2, "x") {
		t.Fatal("an out-of-range position should be refused")
	}
	if sess.EditNote(1, "   ") {
		t.Fatal("blank replacement text should be refused")
	}
	if !sess.EditNote(1, "  original  ") {
		t.Fatal("want the edit to succeed")
	}
	if sess.Notes[0].Text != "original" {
		t.Fatalf("got %q", sess.Notes[0].Text)
	}
	if sess.Notes[0].At != at {
		t.Fatalf("editing should not re-date the note: got %q, want %q", sess.Notes[0].At, at)
	}
}

func testStore(t *testing.T) *Store {
	t.Helper()
	cs, err := OpenStore(filepath.Join(t.TempDir(), "chats"))
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}

func TestSession_clearKeepsNotesAndScope(t *testing.T) {
	sess := &Session{Name: "s", Adventure: "CoS"}
	sess.AddNote("the party sold the Sunsword")
	sess.AddTurn("who is Strahd", ask.Result{Answer: "A vampire."})

	turns, notes := sess.Clear(false)
	if turns != 1 || notes != 0 {
		t.Fatalf("clear reported %d turns and %d notes", turns, notes)
	}
	if len(sess.Turns) != 0 {
		t.Fatalf("transcript survived: %+v", sess.Turns)
	}
	if len(sess.Notes) != 1 {
		t.Fatal("clearing the transcript must keep the notes")
	}
	if sess.Adventure != "CoS" {
		t.Fatalf("clearing dropped the adventure scope: %q", sess.Adventure)
	}
	if len(sess.History()) != 0 {
		t.Fatal("a cleared session has no history to send")
	}

	sess.AddTurn("and now", ask.Result{Answer: "Still a vampire."})
	turns, notes = sess.Clear(true)
	if turns != 1 || notes != 1 {
		t.Fatalf("clear --notes reported %d turns and %d notes", turns, notes)
	}
	if len(sess.Notes) != 0 || len(sess.NoteTexts()) != 0 {
		t.Fatalf("notes survived: %+v", sess.Notes)
	}
}

func TestStore_clearedSessionStaysListed(t *testing.T) {
	cs := testStore(t)
	sess, err := cs.Load("lmop")
	if err != nil {
		t.Fatal(err)
	}
	sess.AddNote("the party is level 2")
	sess.AddTurn("q", ask.Result{Answer: "a"})
	if err := cs.Save(sess); err != nil {
		t.Fatal(err)
	}
	sess.Clear(false)
	if err := cs.Save(sess); err != nil {
		t.Fatal(err)
	}

	list, _, err := cs.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Turns != 0 || list[0].Notes != 1 {
		t.Fatalf("clearing is not deleting: %+v", list)
	}
}

func TestStore_saveRefusesToClobberAConcurrentUpdate(t *testing.T) {
	cs := testStore(t)

	first, err := cs.Load("shared")
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.Save(first); err != nil {
		t.Fatal(err)
	}

	// Two independent loads of the same session, as two processes editing it
	// concurrently would each do.
	a, err := cs.Load("shared")
	if err != nil {
		t.Fatal(err)
	}
	b, err := cs.Load("shared")
	if err != nil {
		t.Fatal(err)
	}

	a.AddNote("from process A")
	if err := cs.Save(a); err != nil {
		t.Fatal(err)
	}

	b.AddNote("from process B")
	if err := cs.Save(b); err == nil {
		t.Fatal("saving b after a already changed the file on disk should be refused, not silently overwrite a's update")
	}

	onDisk, err := cs.Load("shared")
	if err != nil {
		t.Fatal(err)
	}
	if len(onDisk.Notes) != 1 || onDisk.Notes[0].Text != "from process A" {
		t.Fatalf("process A's saved update should survive the refused save: %+v", onDisk.Notes)
	}

	// Reloading b picks up a's change and lets b save on top of it.
	b, err = cs.Load("shared")
	if err != nil {
		t.Fatal(err)
	}
	b.AddNote("from process B, retried")
	if err := cs.Save(b); err != nil {
		t.Fatal(err)
	}
	final, err := cs.Load("shared")
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Notes) != 2 {
		t.Fatalf("a retried save after reloading should succeed and keep both notes: %+v", final.Notes)
	}
}

func TestStore_sequentialSavesOnTheSameLoadedSessionSucceed(t *testing.T) {
	cs := testStore(t)
	sess, err := cs.Load("s")
	if err != nil {
		t.Fatal(err)
	}
	sess.AddNote("one")
	if err := cs.Save(sess); err != nil {
		t.Fatal(err)
	}
	sess.AddNote("two")
	if err := cs.Save(sess); err != nil {
		t.Fatalf("a second save on the same in-memory session, with nothing else touching the file, should succeed: %v", err)
	}
}

func TestStore_loadExistingRefusesAnUnknownSession(t *testing.T) {
	cs := testStore(t)
	if _, err := cs.LoadExisting("nope"); err == nil {
		t.Fatal("want an error for a session that was never created")
	}

	sess, err := cs.Load("real session")
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.Save(sess); err != nil {
		t.Fatal(err)
	}
	found, err := cs.LoadExisting("real session")
	if err != nil {
		t.Fatal(err)
	}
	if found.Name != "real session" {
		t.Fatalf("got %+v", found)
	}
}

func TestStore_renamePreservesTranscriptAndFreesTheOldName(t *testing.T) {
	cs := testStore(t)

	sess, err := cs.Load("Curse of Strahd")
	if err != nil {
		t.Fatal(err)
	}
	sess.AddTurn("who is Strahd", ask.Result{Answer: "A vampire."})
	if err := cs.Save(sess); err != nil {
		t.Fatal(err)
	}

	renamed, err := cs.Rename("Curse of Strahd", "CoS Campaign")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "CoS Campaign" || len(renamed.Turns) != 1 {
		t.Fatalf("rename should keep the transcript under the new name: %+v", renamed)
	}

	again, err := cs.Load("CoS Campaign")
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Turns) != 1 {
		t.Fatalf("the renamed session did not persist: %+v", again)
	}

	fresh, err := cs.Load("Curse of Strahd")
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.Turns) != 0 {
		t.Fatalf("the old name should be free for reuse, got a nonempty session: %+v", fresh)
	}
}

func TestStore_renameRefusesAnExistingTarget(t *testing.T) {
	cs := testStore(t)
	if _, err := cs.Load("a"); err != nil {
		t.Fatal(err)
	}
	if err := cs.Save(mustLoad(t, cs, "a")); err != nil {
		t.Fatal(err)
	}
	if err := cs.Save(mustLoad(t, cs, "b")); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Rename("a", "b"); err == nil {
		t.Fatal("renaming onto an existing session should be refused")
	}
	if _, err := cs.Rename("missing", "c"); err == nil {
		t.Fatal("renaming a session that doesn't exist should be refused")
	}
}

func mustLoad(t *testing.T, cs *Store, name string) *Session {
	t.Helper()
	sess, err := cs.Load(name)
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestStore_exportThenImportRoundTrips(t *testing.T) {
	cs := testStore(t)
	sess, err := cs.Load("Curse of Strahd")
	if err != nil {
		t.Fatal(err)
	}
	sess.AddNote("the party sold the Sunsword")
	sess.AddTurn("who is Strahd", ask.Result{
		Answer:    "A vampire.",
		Citations: []ask.Hit{{Kind: "monster", Name: "Strahd", Source: "CoS"}},
	})
	if err := cs.Save(sess); err != nil {
		t.Fatal(err)
	}

	raw, err := cs.Export("Curse of Strahd")
	if err != nil {
		t.Fatal(err)
	}

	other := testStore(t)
	imported, err := other.Import(raw, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Name != "Curse of Strahd" || len(imported.Turns) != 1 || len(imported.Notes) != 1 {
		t.Fatalf("import did not round-trip the session: %+v", imported)
	}

	if _, err := other.Import(raw, "", false); err == nil {
		t.Fatal("importing onto an existing session without --force should be refused")
	}
	if _, err := other.Import(raw, "", true); err != nil {
		t.Fatalf("force should allow overwriting: %v", err)
	}

	renamedImport, err := other.Import(raw, "A Copy", false)
	if err != nil {
		t.Fatal(err)
	}
	if renamedImport.Name != "A Copy" {
		t.Fatalf("--name should override the file's own name, got %q", renamedImport.Name)
	}
}

func TestStore_importRejectsGarbage(t *testing.T) {
	cs := testStore(t)
	if _, err := cs.Import([]byte("not json"), "", false); err == nil {
		t.Fatal("want an error for unparseable input")
	}
	if _, err := cs.Import([]byte(`{"turns":[]}`), "", false); err == nil {
		t.Fatal("want an error when neither the file nor --name supplies a session name")
	}
}

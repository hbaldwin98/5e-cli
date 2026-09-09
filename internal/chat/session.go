package chat

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/hbaldwin98/5e-cli/internal/ask"
)

// Session is one persisted conversation. Turns are the transcript; Notes are
// facts the user asked to keep, which are re-sent with every question.
type Session struct {
	Name      string   `json:"name"`
	Adventure string   `json:"adventure,omitempty"`
	Created   string   `json:"created"`
	Updated   string   `json:"updated"`
	Notes     []Note   `json:"notes,omitempty"`
	Turns     []Record `json:"turns,omitempty"`
}

// Note is a durable fact about this campaign, written by the user.
type Note struct {
	At   string `json:"at"`
	Text string `json:"text"`
}

// Record is one question and the answer it produced, with the citations that
// grounded it so a reader can `5e get` the source later.
type Record struct {
	At        string    `json:"at"`
	Question  string    `json:"question"`
	Answer    string    `json:"answer"`
	Citations []ask.Hit `json:"citations,omitempty"`
}

// Summary is one session as `chat list` reports it.
type Summary struct {
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	Adventure string `json:"adventure,omitempty"`
	Turns     int    `json:"turns"`
	Notes     int    `json:"notes"`
	Updated   string `json:"updated"`
}

// DefaultName is the session used when the caller names none, so that `5e
// chat` twice in a row continues one conversation.
const DefaultName = "default"

// Timestamp is RFC 3339 with a fixed-width fraction. RFC3339Nano drops
// trailing zeros, which makes two timestamps compare wrong as strings; a fixed
// width keeps sorting a session list a string compare.
const Timestamp = "2006-01-02T15:04:05.000000Z07:00"

func now() string {
	return time.Now().UTC().Format(Timestamp)
}

// History renders the transcript as chat messages, oldest first. Only whole
// exchanges are emitted: a question whose answer failed is not history.
func (s *Session) History() []ask.Message {
	msgs := make([]ask.Message, 0, len(s.Turns)*2)
	for _, t := range s.Turns {
		if strings.TrimSpace(t.Question) == "" || strings.TrimSpace(t.Answer) == "" {
			continue
		}
		msgs = append(msgs,
			ask.Message{Role: "user", Content: t.Question},
			ask.Message{Role: "assistant", Content: t.Answer},
		)
	}
	return msgs
}

// NoteTexts is the notes in the order they were written.
func (s *Session) NoteTexts() []string {
	out := make([]string, 0, len(s.Notes))
	for _, n := range s.Notes {
		out = append(out, n.Text)
	}
	return out
}

// AddNote records a fact. It reports whether the note was kept; blank text is
// not a note.
func (s *Session) AddNote(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	s.Notes = append(s.Notes, Note{At: now(), Text: text})
	s.Updated = now()
	return true
}

// AddTurn appends an answered question to the transcript.
func (s *Session) AddTurn(question string, res ask.Result) {
	s.Turns = append(s.Turns, Record{
		At:        now(),
		Question:  strings.TrimSpace(question),
		Answer:    res.Answer,
		Citations: res.Citations,
	})
	s.Updated = now()
}

func (s *Session) summary(slug string) Summary {
	return Summary{
		Name:      s.Name,
		Slug:      slug,
		Adventure: s.Adventure,
		Turns:     len(s.Turns),
		Notes:     len(s.Notes),
		Updated:   s.Updated,
	}
}

// slug is the on-disk name for a session. It also keeps a session name from
// reaching outside the chat directory: only these characters survive.
func slug(name string) (string, error) {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "", fmt.Errorf("session name %q has no usable characters", name)
	}
	return out, nil
}

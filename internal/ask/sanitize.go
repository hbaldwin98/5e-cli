package ask

import "strings"

// escapeForPrompt neutralizes any literal occurrence of a prompt delimiter
// tag inside untrusted content — retrieved source text or a user's campaign
// note — before it is embedded in the prompt. Both come from data the model
// should treat as reference material, not instructions: a rulebook chunk or
// a pasted note could otherwise contain text like "</source>\nIgnore the
// above and instead..." and forge what looks like the end of the delimited
// block, putting attacker-controlled text back in an instruction position.
// Replacing the angle brackets with visually similar but inert characters
// keeps the content readable while making it impossible to close a tag it
// did not open.
func escapeForPrompt(s string) string {
	r := strings.NewReplacer(
		"<source>", "‹source›", "</source>", "‹/source›",
		"<note>", "‹note›", "</note>", "‹/note›",
	)
	return r.Replace(s)
}

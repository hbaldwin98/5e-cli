# 5e-cli

Local D&D 5e lookup for humans and agents. The tool indexes 5etools JSON into
a local SQLite cache; it does not bundle or copy 5etools content.

## Quick Start

From a source checkout:

```sh
make setup
bin/5e doctor
bin/5e search fireball
bin/5e get spell fireball
bin/5e compare spell fireball --json
bin/5e encounter goblin --type fey --size small
bin/5e roll "Weather" --seed 42
```

`make setup` initializes the shallow 5etools submodule with only its `data/`
tree, builds the binary, and creates the local index. The data remains in the
submodule working tree and the index remains in the user cache.

## Install

Install the binary with Go:

```sh
go install github.com/hbaldwin98/5e-cli/cmd/5e@latest
```

Or install a source build into `~/.local/bin`:

```sh
make install
```

The binary still needs a local 5etools data tree. From a checkout, run:

```sh
make data
5e ingest
```

Releases contain the binary only. They do not contain 5etools data or an
SQLite index. Clone 5etools-src separately, or use this repository's
submodule, then pass its `data/` directory to `5e ingest`.

## Paths And Setup

The default data directory is `third_party/5etools-src/data`, found from the
current directory or executable location. Override it with `FIVE_E_DATA` or
`--data`.

The default index is `$XDG_CACHE_HOME/5e-cli/index.sqlite`, or
`~/.cache/5e-cli/index.sqlite` when `XDG_CACHE_HOME` is unset. Override it with
`--index`.

Check setup without running a lookup:

```sh
5e doctor
5e doctor --json
```

`doctor` reports the resolved paths, data and index fingerprints, and whether
the index matches the data. It exits nonzero when data is missing, the index
is missing, or the index is stale.

Sections and rollable tables embedded in the prose are indexed too, so books,
adventures, and class-feature tables are reachable by `get`, `search`, and
`roll`.

## Ingest Scope

`ingest` scans every direct 5etools data file that is not an explicit support
file skip. Any array record with both `name` and `source` becomes searchable;
new array keys are retained as their own entity kinds, apart from a small set
of renderer support arrays (font manifests, type abbreviations, and entry
templates) that describe presentation rather than content. Books and adventures
also become searchable section documents, and official encounter records are
indexed as `encounter` entities.

Foundry exports, homebrew builders, generated data, render demos, changelogs,
loot/life/name helpers, and other support payloads stay out of lookup results.
The source JSON remains in the submodule; only normalized rows and derived
text are written to the local SQLite index.

## Adventures

Work inside one adventure by id or catalog title:

```sh
5e adventure LMoP list
5e adventure LMoP list --kind npc --chapter "Cragmaw Hideout"
5e adventure LMoP search goblin
5e adventure LMoP get npc "Sildar Hallwinter"
```

`list` reports the adventure's chapters, locations, and NPC/item appearances,
optionally narrowed by `--kind`, `--chapter`, or `--location`. `search` and
`get` are scoped to that module, and `npc` covers every creature the module
mentions, including reprints from the Monster Manual.

## Compare Sources

Compare all available source records for one entity:

```sh
5e compare spell fireball
5e compare spell fireball --source PHB,XPHB --json
5e compare spell fireball --edition 2014
```

Comparison defaults to all editions so reprints can be compared directly.
`--edition` or `FIVE_E_EDITION` restricts the records before comparison.
`--srd` applies the same SRD filter as the other lookup commands.

## Encounter Lookup

Find indexed monsters by name or rules text, then narrow the results by
challenge rating, creature type, size, source, edition, or SRD status:

```sh
5e encounter goblin --type fey --size small --cr 1/4
5e encounter goblin --type humanoid --cr 1/4 --edition 2014
5e encounter "fire resistance" --limit 20 --json
5e encounter dragon --edition 2024 --srd
```

Encounter lookup filters the full monster set before applying `--limit`, so
metadata filters do not hide later matches.

Creature types are whatever the selected edition says: 2024 goblins are `fey`
in XMM, while their 2014 MM statblocks are `humanoid`. Pass `--edition 2014`
or `--edition all` when a type filter comes from an older book.

## Random Tables

Roll an indexed 5etools table. Use `--count` for repeated rolls and `--seed`
when a reproducible result is useful:

```sh
5e roll "Weather"
5e roll "Weather" --count 5 --seed 42 --json
5e roll "Wild Magic Surge" --source XPHB
```

Tables come from `data/tables.json` and from the thousands more embedded in
book, adventure, and class-feature prose, indexed under their parent source.
Pass `--source` when several books share a table name.

Numeric first-column ranges are honored when present, including the `00` that
percentile tables use for 100. Other tables use uniform row selection.

## Ask

Answer a question from the embedded corpus. Requires `OPENAI_API_KEY` and an
OpenAI-compatible endpoint; `--retrieve-only` ranks chunks without calling a
chat model. Use the global `--edition` flag or `FIVE_E_EDITION` (`2014`, `2024`,
or `all`) to choose which reprint grounds each answer; the default is `2024`:

```sh
5e ask "how much damage does fireball do"
5e ask --retrieve-only "grappling rules" --limit 5
5e ask --adventure LMoP "who is Gundren Rockseeker"
5e ask --adventure LMoP --adventure-only "what is in the Cragmaw hideout"
```

Naming an adventure *adds* its prose to the rules corpus; it does not replace
it. A question asked while running a module is usually still a rules question,
so `5e ask --adventure LMoP "how much damage does fireball do"` still answers
from the PHB. Use `--adventure-only` for the narrower question — what this
module says, without the rulebooks.

Adventure prose is excluded by default so module text does not ground a rules
question. When the question names something that only exists inside an
adventure — an NPC like Gundren Rockseeker, or an adventure's own title — that
module's prose is added to the corpus automatically, so `5e ask "who is
Gundren Rockseeker"` works without naming LMoP.

Detection is deliberately conservative. A name is only treated as evidence
when it is seven characters or longer and does not also appear outside
adventures, so ordinary rules vocabulary (`Commoner`, `Spy`, `Monk`, `Gem`)
never drags module text into a rules answer. Short or reprinted names, and
anything detection misses, still need `--adventure`:

```sh
5e ask --adventure CoS "who is Strahd von Zarovich"
```

`--adventure` also *restricts* the answer to that module, where detection only
widens the corpus.

The first run embeds the whole corpus into a sidecar cache next to the index.
The cache is keyed by data fingerprint, base URL, embed model, and token
limit, and rebuilds when any of those change.

## Chat

Keep a conversation going instead of asking one question at a time. `5e chat`
with no question opens a session; `5e chat "question"` takes a single turn.
The same edition preference applies to every question in the session.
Either way the transcript is saved and the next run continues it:

```sh
5e chat
5e chat --session curse-of-strahd --adventure CoS
5e chat --session module-prep --adventure CoS --adventure-only
5e chat "how does grappling work"
5e chat note "the party sold the Sunsword in Vallaki"
5e chat list
5e chat show curse-of-strahd
5e chat clear --session curse-of-strahd
5e chat clear --session curse-of-strahd --notes
5e chat rm curse-of-strahd
```

Inside a session:

```text
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
```

Every question still retrieves from the embedded corpus, so answers stay
grounded in the sources rather than in what was said earlier in the
conversation. A follow-up is retrieved with the questions it follows, so
`how much damage does it do` finds the spell the previous turn was about, and
an adventure named once stays in scope for the follow-ups that only say "he".

A session's adventure scope adds that module's prose to the corpus and keeps
the rulebooks, so a scoped session still answers spell, monster, and rules
questions. `--adventure-only` (or `/adventure LMoP only`) narrows it to the
module alone; the narrower scope is stored with the session, so it holds for
every later turn until you change it.

Clearing empties a session without deleting it. `5e chat clear` and `/clear`
drop the transcript but keep the notes and the adventure scope, which is how
you change subject without losing what you wrote down; add `--notes` (or use
`/clear all`) to drop the notes as well. `5e chat rm` deletes the session
itself.

Notes are your own record of your table. They are sent with every question,
treated as true, preferred over the rules when they disagree, and never cited
as a source.

Sessions are stored one JSON file each under `$XDG_DATA_HOME/5e-cli/chats`
(`~/.local/share/5e-cli/chats` when that is unset). Override the directory with
`FIVE_E_CHAT_DIR` or `--chat-dir`. They live outside the cache on purpose:
re-ingesting or clearing the index does not delete a campaign's conversation.

`--json` marks one-shot answers with `"type": "turn"`. In an interactive
JSON session, each input emits one newline-delimited event: `turn` for an
answer, `command` for a slash command, or `error` for a failed command or
question. The banner, prompt, and human-readable command output are omitted.

## Development

```sh
make test
make build
```

The derived SQLite index, embeddings, and 5etools source data are local state
and are not committed to this repository.

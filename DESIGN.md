# 5e-cli

A local D&D 5e lookup tool. Humans use it as a fast fuzzy finder. Agents use the same commands as structured retrieval.

The 5etools JSON is the source of truth. This repo never copies, vendors, or ships that JSON. A git submodule provides the files; we parse them in place and write derived indexes to a local cache.

## Goals

- `5e get` returns one entity (spell, monster, item, …) by name and source.
- `5e search` ranks names and rules text for typos, fragments, and “what was that called?”
- `5e ask` answers natural-language questions from the same entity IDs `get` uses.
- `--json` is a first-class output mode so a campaign TUI or MCP server can call this without scraping the terminal.
- Offline after ingest. No runtime calls to 5e.tools.

## Non-goals (v1)

- A campaign / session manager. This is the data plane those apps call.
- Shipping 5etools content in git, releases, or containers.
- Foundry, makebrew, converter, or render-demo payloads.
- Reimplementing the 5etools website.

## Data boundary

5etools is a static JSON corpus, not a REST API. The live site and [5etools-src](https://github.com/5etools-mirror-3/5etools-src) share the same `data/` tree.

### Submodule

| | |
|---|---|
| Remote | `https://github.com/5etools-mirror-3/5etools-src` |
| Path | `third_party/5etools-src` |
| What we need | `data/` only (sparse checkout) |
| What the parent repo stores | `.gitmodules` + the gitlink SHA |

JSON files stay in the submodule working tree. They are never copied into `data/`, `internal/`, fixtures that ship, or CI artifacts.

Sparse-checkout the submodule to `data/` so a clone does not pull the 5etools web app.

```text
git clone --recurse-submodules <this-repo>
# or
git submodule update --init --depth 1
```

Binary installs and checkouts without the submodule must pass an explicit data root (see [Runtime paths](#runtime-paths)). `ingest` fails loudly if that directory is missing.

### What we ingest

Discover files. Do not hard-code source filenames or silently drop a new
official identity-bearing dataset.

| Kind of file | How we find it |
|---|---|
| Top-level datasets | every non-skipped `data/*.json` identity array |
| Spells | `data/spells/index.json` → `data/spells/{file}` |
| Spell fluff | `data/spells/fluff-index.json` |
| Monsters | `data/bestiary/index.json` → `data/bestiary/{file}` |
| Bestiary fluff | `data/bestiary/fluff-index.json` (same pattern) |
| Classes | `data/class/index.json` → `data/class/{file}` |
| Class fluff | `data/class/fluff-index.json` |
| Books | `data/books.json` → `data/book/book-{id lowercase}.json` |
| Adventures | `data/adventures.json` → `data/adventure/adventure-{id lowercase}.json` |

**Ingest (searchable records)**

- Every array entry in a direct data file with both `name` and `source`.
- Known 5etools keys keep their canonical aliases; new keys use their array key
  as the entity kind so a corpus update does not make them disappear.
- Matching `fluff-*.json` entries are joined onto their mechanical records.
- Every indexed spell, bestiary, and class file, plus their fluff indexes
- Official encounter datasets are indexed as `encounter` records.

**Ingest (v1 documents)**

- `books.json` + `data/book/book-*.json`
- `adventures.json` + `data/adventure/adventure-*.json`

Book and adventure files are searchable documents (chapter/section chunks), not the same row type as a spell. They stay in the submodule; we only store derived chunks in the local cache.

**Skip**

- `foundry-*`, `makebrew-*`, `makecards`, `converter`, `renderdemo`, `encounterbuilder`, `changelog`, `life`, `loot`, `msbcr`, `names`
- `data/generated/*` and other support payloads that are not searchable source records

### What this repo may contain

Allowed: Go source, tests that use tiny *synthetic* 5etools-shaped JSON, this design doc, the submodule pointer.

Not allowed: copied 5etools files, a committed SQLite/embedding database, golden files that reproduce book or adventure text.

## Runtime paths

| Role | Location |
|---|---|
| Source JSON | `--data` / `FIVE_E_DATA`, else `third_party/5etools-src/data` relative to the repo |
| Derived index | `$XDG_CACHE_HOME/5e-cli/index.sqlite` (fallback `~/.cache/5e-cli/`) |
| Embeddings | `$XDG_CACHE_HOME/5e-cli/embeddings.sqlite` (same directory as `--index`) |
| Chat sessions | `$XDG_DATA_HOME/5e-cli/chats/` (fallback `~/.local/share/5e-cli/chats/`), or `FIVE_E_CHAT_DIR` / `--chat-dir` |
| Config (later) | `$XDG_CONFIG_HOME/5e-cli/config.toml` |

The cache is gitignored local state. It is keyed by the submodule commit SHA (or a hash of `--data`). If the SHA changes, `ingest` rebuilds. `get` / `search` refuse to run against a stale index.

## Command surface

Binary name: `5e`. Module: `github.com/hbaldwin98/5e-cli`.

```text
5e ingest
5e ingest --force
5e ingest --data /path/to/5etools-src/data
5e doctor [--json]

5e get <kind> <name> [--source PHB] [--json]
5e search <query> [--kind spell] [--source PHB,XPHB] [--json] [--limit 10]
5e compare <kind> <name> [--source PHB,XPHB] [--edition 2014|2024|all] [--json]
5e encounter <query> [--cr CR] [--type TYPE] [--size SIZE] [--source SOURCE] [--limit 10] [--json]
5e roll <table name> [--source PHB] [--count 1] [--seed N] [--json]

5e refs <kind> <name> [--source PHB] [--direction outgoing|incoming|both] [--tag spell] [--json]

5e ask <query> [--retrieve-only] [--limit 8] [--json]
5e chat [question] [--session NAME] [--adventure ID] [--kind spell] [--source PHB] [--limit 8] [--json]
5e chat list [--json]
5e chat show [session] [--json]
5e chat note <text>
5e chat clear [session] [--notes]
5e chat rm <session>
5e adventure <id-or-name> search <query> [--kind npc|location|item] [--json] [--limit 10]
5e adventure <id-or-name> get <role> <name> [--json]
5e adventure <id-or-name> list [--kind npc|location|item] [--chapter NAME] [--location NAME] [--json]
5e mcp
```

Global flags: `--json`, `--data`, `--index` (path to the sqlite file), `--edition` (`2014` | `2024` | `all`), `--srd`.

### `ingest`

Walk the data tree, parse entities, join fluff onto the matching `(kind, name, source)`, extract plaintext and `{@tag}` edges, replace the sqlite file atomically (write temp, fsync, rename).

Idempotent. Cheap no-op when the submodule SHA already matches `ingest_meta`.

### `get`

Typed lookup. Kind is required so `5e get spell shield` and `5e get item shield` do not collide.

Identity is `(kind, name, source)`. Names are matched case-insensitively.

1. If `--source` is omitted and several sources match, apply `--edition` / `FIVE_E_EDITION` (default `2024`: `XPHB` over `PHB`, `XMM` over `MM`, `XDMG` over `DMG`). `--edition all` keeps every match.
2. If `--srd` is set, keep only rows ingested with 5etools `srd`, `srd52`, or `basicRules`. Book and adventure sections have no SRD flag and are dropped.
3. If still ambiguous, print the candidates and exit non-zero. Never silently pick among remaining reprints.

Human output is a compact stat block / entry. `--json` is the original normalized entity plus resolved fluff, not a screenshot of the TTY.

### `search`

Two ranked lists, merged:

1. **Name fuzzy** — typo-tolerant match on entity names (`firebal` → Fireball).
2. **FTS5** — query against extracted plaintext (name, entries, fluff).

Filters: `--kind`, `--source` (repeatable or comma-separated). `--srd` keeps SRD / basic-rules entities and skips book/adventure sections. FTS applies that filter in SQL so a tight `LIMIT` cannot hide SRD rows behind non-SRD hits.

Default `search` / `ask` is the **rules corpus**: entity rows plus `bookSection`. Adventure chapter and location text is not mixed in. Unique adventure monsters and items stay ordinary entity rows (`source` = the adventure id) and still appear in default search. To search inside a module, use `5e adventure`.

`--json` returns `{ kind, name, source, score, snippet }[]`. The IDs are the same ones `get` accepts.

### `adventure`

A separate query context for one module. `<id-or-name>` is the 5etools adventure id (`LMoP`) or the catalog title (`Lost Mine of Phandelver`).

Roles:

| Role | What it is |
|---|---|
| `npc` | Every creature **mentioned** in the module: `{@creature}` tags, `statblock` with `tag: creature`, and bestiary rows whose source is this adventure. Reused MM creatures are links to `(monster, name, source)`, not copies. |
| `location` | Named `type: "section"` / `type: "entries"` chunks in the adventure file |
| `item` | Same appearance pattern as `npc`, for items |

`search` ranks names and FTS inside that adventure only. `get npc` / `get location` / `get item` returns one hit using the same `(kind, name, source)` IDs as the global `get` (locations use `adventureLocation` + adventure id as source).

Do not invent a 5etools `npc` kind. `npc` is a role over appearances.

### `ask`

Embed the query against cached vectors, retrieve entity/document chunks, then optionally call a chat model. `--edition` / `FIVE_E_EDITION` keeps the preferred reprint for each logical record when both editions are indexed, while retaining a source that has no preferred-edition counterpart. Citations are `(kind, name, source)` or book section IDs so the caller can `get` the full record. Do not build a second corpus: vectors are derived from the sqlite `text` columns.

`--retrieve-only` skips generation and prints ranked chunks. That is the same path MCP `semantic_search` will call so an agent can reason without a nested LLM. `--srd` filters retrieved chunks to SRD entities; book and adventure document chunks are excluded. Default retrieve also skips adventure documents so module prose does not ground a rules question, until a module is named or detected. The embedding cache still covers the full corpus.

Provider is any OpenAI-compatible host:

| Env | Default |
|---|---|
| `OPENAI_API_KEY` | required |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` |
| `FIVE_E_EMBED_MODEL` | `text-embedding-3-small` |
| `FIVE_E_ASK_MODEL` | `gpt-4o-mini` |
| `FIVE_E_EMBEDDINGS` | sidecar next to `--index` |
| `FIVE_E_EMBED_MAX_TOKENS` | `8192` |
| `FIVE_E_ASK_MAX_TOKENS` | `12000` |
| `FIVE_E_MIN_SCORE` | `0.15` |

Use `/v1/embeddings` and `/v1/chat/completions` so OpenRouter and similar proxies work. The embedding cache is keyed by corpus fingerprint, base URL, embed model, and the token limit. Do not store the API key.

**Adventure scope.** Module prose is reachable two ways, and both are a union. `5e ask --adventure`, `5e chat --adventure`, and MCP `semantic_search`'s `adventure` argument *add* one module's prose to the rules corpus. Otherwise the question is matched against `adventure_names`, a table of names that occur only inside adventures, and any module it names is added the same way. On a full corpus, naming LMoP reads 22,251 vectors rather than the 22,009 of a default ask; detection costs about 950.

Naming a module does not restrict the answer to it: a question asked while running an adventure is usually still a rules question, and the party's spells and the monsters they are fighting are in the rulebooks. `--adventure-only` (MCP `adventureOnly`, `/adventure <id> only` in chat) is the narrower reading, for "what does this module say" — on LMoP that is 260 vectors, with no PHB spell and no MM statblock among them. Detection is skipped when a module is named explicitly: that scope is the caller's answer to the same question.

`adventure_names` is derived at ingest, not queried live: the equivalent correlated query costs ~2.8s. Two guards keep it conservative — a name that also appears outside adventures is dropped (removing reprints like `Commoner` and `Spy`), as is any name under seven characters (removing `Gem`, `Monk`, `Sun`). Adventure titles are included so "what happens in Curse of Strahd" scopes too. The guards cost some real NPCs, notably `Strahd von Zarovich`, whose name also appears in a book-classified source; `--adventure` covers those.

**Vector scope.** The kind, source, and adventure filters run in sqlite before rows are read, and chunk text is fetched only for the chunks that rank. A default ask reads 22k vectors rather than the full 52k, since module prose is excluded anyway.

**Grounding.** `ask` sends the chat model the retrieved chunks' source text, budgeted by `FIVE_E_ASK_MAX_TOKENS` and shared so short sources are never clipped and their unused share goes to long ones. `Hit.Snippet` is a display preview only and must not be what an answer is built from.

**Citations.** `Result.Citations` is not the retrieval list; it is the `(kind, name, source)` triples the model actually wrote in its answer, each checked against the chunks that were really retrieved. A triple that does not match a retrieved chunk (a plausible-looking name the model invented, a typo, a source it wasn't given) is dropped rather than surfaced as if it were grounded. A question with no citable answer legitimately returns no citations, even when retrieval found chunks.

**Chunk windows.** A record longer than the embedding model's per-input limit is split into overlapping windows rather than truncated. At retrieval, up to two of a record's windows can both make the result list when both independently clear the relevance gate — a broad question can genuinely be answered by two different passages of one long section, and collapsing to a single best-scoring window would silently drop the second one. Set `FIVE_E_EMBED_MAX_TOKENS` when the backend caps lower than OpenAI does; many local embedding servers stop at 512.

**Relevance gate.** Query and corpus vectors are both L2-normalized, so a chunk's score is already cosine similarity. Any chunk scoring below `FIVE_E_MIN_SCORE` is rejected before ranking rather than padding the result list: an off-topic question should retrieve nothing and get "no matching sources," not a citation to whatever scored highest among unrelated chunks. Set `FIVE_E_MIN_SCORE` to a negative value to disable the gate for a differently calibrated embedding model.

**Lexical rescue.** Retrieval also runs the question through FTS5 (entity and document indexes, terms ORed rather than ANDed since a full question rarely repeats its own wording verbatim). A chunk that clears the score gate on its own is ranked purely by cosine similarity — a strong semantic match is never displaced by an unrelated chunk that happens to share one common word with the question. A chunk that does *not* clear the gate is otherwise dropped, but a lexical hit on it is treated as independent evidence of relevance and rescues it, appended after the confident tier in FTS rank order. This mainly recovers exact names and numbers a paraphrased embedding can miss.

### `chat`

An ongoing conversation over the same retrieval `ask` uses. `5e chat` with no question opens a REPL; `5e chat "question"` takes one turn and returns. Either way the transcript is saved and the next run continues it.

A session holds three things: the transcript, the notes the user recorded, and an optional adventure scope. That scope *adds* the module's prose to the rules corpus; `--adventure-only` or `/adventure <id> only` is the narrower reading, and it is stored on the session because a conversation that excludes the rulebooks should keep excluding them. Retrieval knobs (`--edition`, `--kind`, `--source`, `--limit`) are per invocation, so a saved conversation never carries a filter from a previous run.

**Sessions are conversation state, not a campaign model.** They store what was asked, what was answered, and facts the user wrote down. They do not model characters, initiative, inventory, or scheduling — that is still the campaign app's job, and this remains its data plane.

**Grounding still runs every turn.** `ask.Converse` retrieves fresh chunks for each question and sends them with the question, so an answer is grounded in the corpus rather than in what the model said three turns ago. The system prompt says as much: earlier turns are context, not sources.

**Follow-ups.** A follow-up is often unintelligible alone ("how much damage does it do?"), so the retrieval text is the question plus the last two *user* turns. Earlier answers are deliberately excluded: embedding the model's own words steers retrieval toward whatever it already said. The same widened text feeds adventure detection, so a module named once stays in scope for the follow-ups that only say "he" or "there".

**Notes.** `/note` in the REPL, or `5e chat note`, records a fact ("the party sold the Sunsword in Vallaki"). Notes are re-sent with every question, marked as the user's own record: true, preferred over the rules when they conflict, and never cited as a source.

**Clearing.** `5e chat clear` (or `/clear`) empties the transcript and keeps the session, its notes, and its adventure scope: dropping the history is how you change subject without losing what you wrote down. `--notes` (or `/clear all`) drops the notes too. Deleting the session is `5e chat rm`.

**Budget.** `FIVE_E_ASK_MAX_TOKENS` covers the whole prompt. Notes take at most a fifth, history at most a third, and the retrieved sources get the rest plus whatever those two did not use. Notes drop oldest-first and history drops oldest whole exchanges, so the model never sees half of one.

**Persistence.** One JSON file per session, written temp-then-rename. Sessions live under `XDG_DATA_HOME`, not beside the index: a session is the user's own writing, and clearing the derived cache must not delete a campaign's conversation. A session name is slugged and given an identity hash for its filename, which keeps the path safe without aliasing names such as `a/b`, `a b`, and `a-b`. Existing slug-only files are migrated when they are opened. A failed answer is not written, and a failed turn does not end the REPL — a rate limit should cost one question, not the session.

`--json` makes an answer a typed `turn` event (`session`, `question`, `answer`,
`citations`). In the REPL, every input emits one newline-delimited `turn`,
`command`, or `error` event; slash-command results and failures stay in the
stream, while the banner and prompt are suppressed.

### `mcp`

Thin stdio MCP server over the same store: `get`, `search`, `semantic_search`, plus adventure-scoped search using the same ids. `semantic_search` reuses `ask --retrieve-only`. No extra ingest path. stdout is JSON-RPC; embedding progress goes to stderr. `--edition` and `--srd` on `5e mcp` apply to every tool the same way they apply to the CLI.

## Parser

The hard part. 5etools JSON is tagged, recursive, and source-keyed.

**Entity identity.** `(kind, name, source)`. Optional `page` is metadata, not part of the primary key unless we hit a real collision.

**Entries.** `entries` (and nested `entriesHigherLevel`, traits, actions, …) are a mix of strings and objects (`type: list | table | entries | quote | inset | item | …`). The parser walks this tree once and produces:

- `raw` — the original JSON object, stored as JSON in sqlite
- `text` — plaintext for FTS and human fallback
- `edges` — extracted tags

**Tags.** Keep `{@spell …}`, `{@creature …}`, `{@item …}`, `{@condition …}`, `{@class …}`, `{@feat …}`, `{@variantrule …}`, `{@book …}`, `{@adventure …}` as edges instead of flattening them away. `{@adventure}` targets kind `adventure` (catalog row), not a section.

Typical forms:

```text
{@spell fireball}
{@creature goblin|MM}
{@item bag of holding|DMG|that bag}
```

Unresolved tags stay in the edge table with a null target; ingest should not fail the whole corpus for one bad link.

**Fluff.** Mechanical files and `fluff-*` files share `(name, source)`. Join at ingest. Search both. `get` shows mechanics first, lore second.

**2014 vs 2024.** Treat `XPHB` / `XMM` / `XDMG` as distinct sources. `--edition` / `FIVE_E_EDITION` (`2014` | `2024` | `all`, default `2024`) affects default `get` disambiguation, search ranking, and ask/chat retrieval, not what is ingested. Adventure sources remain available when they are explicitly or automatically in scope.

**SRD.** `--srd` is a query filter, not a second ingest. An entity is SRD when ingest saw a truthy `srd`, `srd52`, or `basicRules` field. Book and adventure sections are never SRD.

**Kinds (v1 entity rows)**

`spell`, `monster`, `item`, `itemBase`, `class`, `subclass`, `classFeature`, `subclassFeature`, `feat`, `race`, `background`, `optionalfeature`, `condition`, `disease`, `action`, `sense`, `skill`, `reward`, `deity`, `object`, `vehicle`, `trap`, `hazard`, `psionic`, `table`, `variantrule`, `language`, `cult`, `boon`, `deck`, `card`, `charoption`, `bastion`, `recipe`, `monsterfeature`, `encounter`, `itemMastery`, `adventure`, plus newly discovered identity-bearing array keys.

Renderer support arrays are excluded: `itemEntry`, `itemType`, `itemProperty`, `itemTypeAdditionalEntries`, and `languageScript` carry abbreviations, font manifests, and `{{placeholder}}` templates rather than content.

**Kinds (v1 document rows)**

`bookSection`, `adventureSection`, `adventureLocation`

Chunk books on 5etools `type: "section"` headings. Chunk adventures on `type: "section"` (chapters) and named `type: "entries"` (locations). Preserve the book/adventure id and section name. Default search does not query adventure document kinds.

**Embedded tables.** `data/tables.json` holds only a handful of tables; the rest are `type: "table"` nodes inside book, adventure, and entity bodies. Captioned table nodes become `table` entities under their parent source, and never displace a standalone record with the same kind/name/source.

**Adventure catalog.** Each `adventures.json` item is an `adventure` entity: title as name, 5etools id as source. `5e get adventure LMoP` resolves id or title.

**Adventure appearances.** Derived rows (not a second monster/item copy): adventure id, role (`npc` / `item`), target `(kind, name, source)`, optional location name. Built from creature/item tags and statblocks in the adventure file, plus entity rows already sourced to that adventure.

## Store

SQLite in the cache directory. WAL. One file.

```sql
ingest_meta (
  submodule_sha TEXT NOT NULL,
  data_root     TEXT NOT NULL,
  ingested_at   TEXT NOT NULL
);

entities (
  id        INTEGER PRIMARY KEY,
  kind      TEXT NOT NULL,
  name      TEXT NOT NULL,
  source    TEXT NOT NULL,
  page      INTEGER,
  srd       INTEGER NOT NULL DEFAULT 0,
  json      TEXT NOT NULL,  -- original entity object
  text      TEXT NOT NULL,  -- extracted plaintext
  UNIQUE (kind, name, source)
);

entity_fts USING fts5(
  name,
  text,
  content='entities',
  content_rowid='id'
);

edges (
  from_id    INTEGER NOT NULL REFERENCES entities(id),
  tag        TEXT NOT NULL,          -- spell, creature, item, ...
  to_kind    TEXT,
  to_name    TEXT NOT NULL,
  to_source  TEXT,
  display    TEXT
);

documents (
  id         INTEGER PRIMARY KEY,
  kind       TEXT NOT NULL,          -- bookSection | adventureSection
  parent_id  TEXT NOT NULL,          -- PHB, CoS, ...
  section    TEXT NOT NULL,
  json       TEXT NOT NULL,
  text       TEXT NOT NULL
);

document_fts USING fts5(
  section,
  text,
  content='documents',
  content_rowid='id'
);

appearances (
  adventure TEXT NOT NULL,   -- LMoP
  role      TEXT NOT NULL,   -- npc | item
  kind      TEXT NOT NULL,   -- monster | item
  name      TEXT NOT NULL,
  source    TEXT NOT NULL,   -- MM, LMoP, …
  location  TEXT             -- optional section/location name
);
```

`json` / `text` in sqlite are a **derived cache**, not a second source tree. They exist so `get` does not re-parse hundreds of files per call. Deleting the cache and re-running `ingest` is always correct.

Do not commit this file. Do not embed it in the binary.

## Search ranking

For `search`:

1. Exact name (case-insensitive) in preferred sources
2. Fuzzy name (small edit distance / subsequence)
3. FTS5 bm25 on `entity_fts` and `document_fts`
4. Prefer configured edition, then shorter names, then source abbreviation

Name hits outrank body hits unless the query is clearly prose (“hold breath”, “opportunity attack”).

FTS + fuzzy remains the default `search`. Embeddings are an extra index for `ask` / semantic retrieve, not a replacement.

## Output

Human mode is compact, stable, and pipeable. No TUI in v1 (a picker can come later). Color on a TTY, Markdown otherwise. `--json` is the agent contract.

JSON mode: one document per command.

```json
{
  "kind": "spell",
  "name": "Fireball",
  "source": "XPHB",
  "page": 0,
  "text": "...",
  "json": {},
  "edges": [{ "tag": "condition", "name": "Invisible", "source": "XPHB" }]
}
```

`text` is rendered plaintext (tags expanded to display names). `json` is the 5etools object so callers who already speak 5etools do not lose fields.

## Stack

- **Language:** Go. Static binary, good SQLite story, matches the campaign TUI this is meant to feed.
- **CLI:** `spf13/cobra` (or `github.com/alecthomas/kong` if cobra feels heavy — pick cobra unless a first spike says otherwise).
- **SQLite:** `modernc.org/sqlite` (pure Go) unless we need a FTS5 extension that forces CGo.
- **Fuzzy:** small in-process matcher on the name list (e.g. a trigram or smith-waterman style ranker). Do not shell out to `fzf`.
- **Embeddings:** OpenAI-compatible HTTP (`/v1/embeddings`, `/v1/chat/completions`). Vectors live in a sidecar sqlite file next to the index; similarity is cosine in-process (pure Go, no sqlite-vec).

## Layout

```text
cmd/5e/              // main
internal/ingest/     // discovery, parse, fluff join, atomic sqlite write
internal/parse/      // entries walker, tag lexer, plaintext render
internal/store/      // sqlite schema, queries
internal/search/     // fuzzy + FTS merge
internal/edition/    // 2014 / 2024 / all preference
internal/adventure/  // scoped module lookup, npc/location roles
internal/ask/        // OpenAI-compatible embed + retrieve + generate
internal/chat/       // persisted conversations and notes over ask
internal/mcpserver/  // stdio MCP tools over get/search/retrieve
internal/cli/        // cobra commands, human vs json
third_party/5etools-src/  // submodule, sparse data/
DESIGN.md
```

No public library API in v1. Other tools invoke the binary with `--json`.

## Phases

1. **Submodule + ingest + get + search.** Done.
2. **Formatted human rendering.** Done. Markdown stat blocks; Glamour on a TTY; `--json` unchanged.
3. **`ask`.** Done. OpenAI-compatible embeddings over existing `text` columns, then optional chat completions. Same IDs.
4. **`mcp`.** Done. Stdio server wrapping `get` / `search` / `semantic_search` / `adventure_search`.
5. **Edition default, `--srd`, adventure-scoped lookup.** Done.
6. **Reference navigation.** Done. `refs` and MCP `references` expose incoming and outgoing tag edges.
7. **Richer adventure tools.** Done. List module chapters, locations, and NPC/item appearances with filters.
8. **Distribution workflow.** Done. `doctor` diagnoses local setup; Make targets cover build, install, data, ingest, and tests.
9. **Source comparison.** Done. Compare same-name records across sources and report top-level field differences.
10. **Encounter lookup.** Done. Filter indexed monsters by name/text, CR, type, size, source, edition, and SRD.
11. **Random tables.** Done. Roll indexed tables with repeat counts, deterministic seeds, and numeric ranges (including the `00` that percentile tables use for 100), sourced from `tables.json` and from tables embedded in prose.
12. **Chat sessions.** Done. Saved multi-turn conversations with recorded notes over the same retrieval as `ask`.

## Board

The in-repo board. Argus mirrors this feature; git is the durable copy.

### Done

- Write DESIGN.md
- Add 5etools-src submodule with `data/` sparse checkout
- Implement ingest, get, and search against sqlite FTS
- Formatted human CLI rendering for get and search
- `ask` over OpenAI-compatible embeddings
- MCP stdio server wrapping get/search/retrieve
- Edition default for get disambiguation and search ranking
- `--srd` filter using ingested `srd` / `srd52` / `basicRules`
- Keep default search/ask on entities and book sections
- Ingest adventure catalog as kind `adventure`
- `5e adventure` command with scoped search
- Location chunks and npc/item appearance index
- `refs` command and MCP `references` tool for incoming/outgoing tag edges
- `adventure list` command and MCP `adventure_list` report with chapter/location filters
- `compare` command for source/edition differences
- `encounter` command for pre-filtered monster lookup
- `roll` command for indexed random tables
- MCP `compare`, `encounter`, and `roll` tools matching the CLI commands
- `chat` command with saved sessions, recorded notes, and multi-turn grounding

### Open

- **Homebrew (deferred):** extra JSON files in a user dir, same parser.

## Follow-ups

Work after random tables. Do these in order unless a later item is unblocked.

### Later

- **Homebrew (deferred):** extra JSON files in a user dir, same parser.

## Distribution

- Source checkout: `make setup` initializes the shallow submodule with sparse `data/`, builds the binary, and ingests into the user cache. `make data`, `make build`, `make install`, `make ingest`, `make doctor`, and `make test` are also available independently.
- `5e doctor` reports resolved data/index paths, fingerprints, and readiness. It emits JSON with `--json` and exits non-zero when setup is incomplete or stale.
- Data can be supplied with `--data` or `FIVE_E_DATA`; the derived index can be supplied with `--index`. The default index is under `$XDG_CACHE_HOME/5e-cli/` or `~/.cache/5e-cli/`.
- Releases: ship the binary only. Document that the user must clone 5etools-src (or this repo with submodules) and run `5e ingest --data …`.
- Do not attach `data/` or `index.sqlite` to GitHub releases.

## Decided in v1

- Fuzzy: prefix / contains / compact-name plus short Levenshtein. No subsequence matching.
- Class files explode into `class` + `subclass` + feature rows. Feature names are stored as `Extra Attack (Fighter 5)` so `(kind, name, source)` stays unique. Subraces become `High (Elf)`.
- Ask uses an OpenAI-compatible base URL (`OPENAI_API_KEY`, `OPENAI_BASE_URL`) rather than a bundled local model.
- `ask --retrieve-only` (and MCP `semantic_search`) embeds the query without a generation call.
- MCP is the official Go SDK over stdio, wrapping the same `get` / `search` / retrieve paths as the CLI.
- Default edition is `2024`. `--edition 2014` or `all` opts out; `FIVE_E_EDITION` is the env equivalent.
- `--srd` filters `get` / `search` / `ask` / MCP using ingested `srd`, `srd52`, and `basicRules`. Documents are excluded.
- Adventures are a separate query context (`5e adventure`). Default search/ask stay entities + `bookSection`. `npc` is an appearance role, not a 5etools kind.

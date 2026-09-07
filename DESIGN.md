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

Discover files. Do not hard-code source filenames.

| Kind of file | How we find it |
|---|---|
| Top-level datasets | known list of `data/*.json` (see below) |
| Spells | `data/spells/index.json` → `data/spells/{file}` |
| Spell fluff | `data/spells/fluff-index.json` |
| Monsters | `data/bestiary/index.json` → `data/bestiary/{file}` |
| Bestiary fluff | `data/bestiary/fluff-index.json` (same pattern) |
| Classes | `data/class/index.json` → `data/class/{file}` |
| Class fluff | `data/class/fluff-index.json` |
| Books | `data/books.json` → `data/book/book-{id lowercase}.json` |
| Adventures | `data/adventures.json` → `data/adventure/adventure-{id lowercase}.json` |

**Ingest (v1 mechanics + fluff)**

- Direct: `actions`, `backgrounds`, `bastions`, `charcreationoptions`, `conditionsdiseases`, `cultsboons`, `decks`, `deities`, `feats`, `homecrafts`, `items`, `items-base`, `languages`, `magicvariants`, `monsterfeatures`, `objects`, `optionalfeatures`, `psionics`, `races`, `recipes`, `rewards`, `senses`, `skills`, `tables`, `trapshazards`, `variantrules`, `vehicles`
- Matching `fluff-*.json` next to those
- Every indexed spell, bestiary, and class file, plus their fluff indexes

**Ingest (v1 documents)**

- `books.json` + `data/book/book-*.json`
- `adventures.json` + `data/adventure/adventure-*.json`

Book and adventure files are searchable documents (chapter/section chunks), not the same row type as a spell. They stay in the submodule; we only store derived chunks in the local cache.

**Skip**

- `foundry-*`, `makebrew-*`, `makecards`, `converter`, `renderdemo`, `encounterbuilder`, `encounters`, `changelog`, `life`, `loot`, `msbcr`, `names`
- `data/generated/*` until we prove we cannot derive the same lookups ourselves

### What this repo may contain

Allowed: Go source, tests that use tiny *synthetic* 5etools-shaped JSON, this design doc, the submodule pointer.

Not allowed: copied 5etools files, a committed SQLite/embedding database, golden files that reproduce book or adventure text.

## Runtime paths

| Role | Location |
|---|---|
| Source JSON | `--data` / `FIVE_E_DATA`, else `third_party/5etools-src/data` relative to the repo |
| Derived index | `$XDG_CACHE_HOME/5e-cli/index.sqlite` (fallback `~/.cache/5e-cli/`) |
| Embeddings | `$XDG_CACHE_HOME/5e-cli/embeddings.sqlite` (same directory as `--index`) |
| Config (later) | `$XDG_CONFIG_HOME/5e-cli/config.toml` |

The cache is gitignored local state. It is keyed by the submodule commit SHA (or a hash of `--data`). If the SHA changes, `ingest` rebuilds. `get` / `search` refuse to run against a stale index.

## Command surface

Binary name: `5e`. Module: `github.com/hbaldwin98/5e-cli`.

```text
5e ingest
5e ingest --force
5e ingest --data /path/to/5etools-src/data

5e get <kind> <name> [--source PHB] [--json]
5e search <query> [--kind spell] [--source PHB,XPHB] [--json] [--limit 10]

5e ask <query> [--retrieve-only] [--limit 8] [--json]
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

`--json` returns `{ kind, name, source, score, snippet }[]`. The IDs are the same ones `get` accepts.

### `ask`

Embed the query against cached vectors, retrieve entity/document chunks, then optionally call a chat model. Citations are `(kind, name, source)` or book section IDs so the caller can `get` the full record. Do not build a second corpus: vectors are derived from the sqlite `text` columns.

`--retrieve-only` skips generation and prints ranked chunks. That is the same path MCP `semantic_search` will call so an agent can reason without a nested LLM. `--srd` filters retrieved chunks to SRD entities; document chunks are excluded. The embedding cache still covers the full corpus.

Provider is any OpenAI-compatible host:

| Env | Default |
|---|---|
| `OPENAI_API_KEY` | required |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` |
| `FIVE_E_EMBED_MODEL` | `text-embedding-3-small` |
| `FIVE_E_ASK_MODEL` | `gpt-4o-mini` |
| `FIVE_E_EMBEDDINGS` | sidecar next to `--index` |

Use `/v1/embeddings` and `/v1/chat/completions` so OpenRouter and similar proxies work. The embedding cache is keyed by corpus fingerprint, base URL, and embed model. Do not store the API key.

### `mcp`

Thin stdio MCP server over the same store: `get`, `search`, `semantic_search`. `semantic_search` reuses `ask --retrieve-only`. No extra ingest path. stdout is JSON-RPC; embedding progress goes to stderr. `--edition` and `--srd` on `5e mcp` apply to every tool the same way they apply to the CLI.

## Parser

The hard part. 5etools JSON is tagged, recursive, and source-keyed.

**Entity identity.** `(kind, name, source)`. Optional `page` is metadata, not part of the primary key unless we hit a real collision.

**Entries.** `entries` (and nested `entriesHigherLevel`, traits, actions, …) are a mix of strings and objects (`type: list | table | entries | quote | inset | item | …`). The parser walks this tree once and produces:

- `raw` — the original JSON object, stored as JSON in sqlite
- `text` — plaintext for FTS and human fallback
- `edges` — extracted tags

**Tags.** Keep `{@spell …}`, `{@creature …}`, `{@item …}`, `{@condition …}`, `{@class …}`, `{@feat …}`, `{@variantrule …}`, `{@book …}`, `{@adventure …}` as edges instead of flattening them away.

Typical forms:

```text
{@spell fireball}
{@creature goblin|MM}
{@item bag of holding|DMG|that bag}
```

Unresolved tags stay in the edge table with a null target; ingest should not fail the whole corpus for one bad link.

**Fluff.** Mechanical files and `fluff-*` files share `(name, source)`. Join at ingest. Search both. `get` shows mechanics first, lore second.

**2014 vs 2024.** Treat `XPHB` / `XMM` / `XDMG` as distinct sources. `--edition` / `FIVE_E_EDITION` (`2014` | `2024` | `all`, default `2024`) only affects default `get` disambiguation and default search ranking, not what is ingested.

**SRD.** `--srd` is a query filter, not a second ingest. An entity is SRD when ingest saw a truthy `srd`, `srd52`, or `basicRules` field. Book and adventure sections are never SRD.

**Kinds (v1 entity rows)**

`spell`, `monster`, `item`, `itemBase`, `class`, `subclass`, `classFeature`, `subclassFeature`, `feat`, `race`, `background`, `optionalfeature`, `condition`, `disease`, `action`, `sense`, `skill`, `reward`, `deity`, `object`, `vehicle`, `trap`, `hazard`, `psionic`, `table`, `variantrule`, `language`, `cult`, `boon`, `deck`, `charoption`, `bastion`, `recipe`, `monsterfeature`

**Kinds (v1 document rows)**

`bookSection`, `adventureSection`

Chunk books/adventures on 5etools section headings (`type: "section"` / chapter contents), not arbitrary token windows. Preserve the book/adventure id and section name.

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
internal/ask/        // OpenAI-compatible embed + retrieve + generate
internal/mcpserver/  // stdio MCP tools over get/search/retrieve
internal/cli/        // cobra commands, human vs json
third_party/5etools-src/  // submodule, sparse data/
DESIGN.md
```

No public library API in v1. Other tools invoke the binary with `--json`.

## Phases

1. **Submodule + ingest + get + search.** Done.
2. **Formatted human rendering.** Done. Markdown stat blocks; Glamour on a TTY; `--json` unchanged.
3. **`ask`.** OpenAI-compatible embeddings over existing `text` columns, then optional chat completions. Same IDs.
4. **`mcp`.** Done. Stdio server wrapping `get` / `search` / `semantic_search`.

## Follow-ups

Work after `--srd`. Do these in order unless a later item is unblocked.

### Later

- **Homebrew (deferred):** extra JSON files in a user dir, same parser.

## Distribution

- Source checkout: submodule (or `--data`) + `go build`.
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


# 5e-cli

A local D&D 5e lookup tool. Humans use it as a fast fuzzy finder. Agents use the same commands as structured retrieval.

The 5etools JSON is the source of truth. This repo never copies, vendors, or ships that JSON. A git submodule provides the files; we parse them in place and write derived indexes to a local cache.

## Goals

- `5e get` returns one entity (spell, monster, item, …) by name and source.
- `5e search` ranks names and rules text for typos, fragments, and “what was that called?”
- `5e ask` (later) answers natural-language questions from the same entity IDs `get` uses.
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
| Embeddings (later) | `$XDG_CACHE_HOME/5e-cli/embeddings/` |
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

5e ask <query>          # phase 3
5e mcp                  # phase 4
```

Global flags: `--json`, `--data`, `--index` (path to the sqlite file).

### `ingest`

Walk the data tree, parse entities, join fluff onto the matching `(kind, name, source)`, extract plaintext and `{@tag}` edges, replace the sqlite file atomically (write temp, fsync, rename).

Idempotent. Cheap no-op when the submodule SHA already matches `ingest_meta`.

### `get`

Typed lookup. Kind is required so `5e get spell shield` and `5e get item shield` do not collide.

Identity is `(kind, name, source)`. Names are matched case-insensitively. If `--source` is omitted and several sources match:

1. Apply configured edition preference (2014 vs 2024 — `PHB` vs `XPHB`, `MM` vs `XMM`, …).
2. If still ambiguous, print the candidates and exit non-zero. Never silently pick a reprint.

Human output is a compact stat block / entry. `--json` is the original normalized entity plus resolved fluff, not a screenshot of the TTY.

### `search`

Two ranked lists, merged:

1. **Name fuzzy** — typo-tolerant match on entity names (`firebal` → Fireball).
2. **FTS5** — query against extracted plaintext (name, entries, fluff).

Filters: `--kind`, `--source` (repeatable or comma-separated).

`--json` returns `{ kind, name, source, score, snippet }[]`. The IDs are the same ones `get` accepts.

### `ask` (later)

Embed the query, retrieve entity/document chunks, optionally call an LLM. Citations must be `(kind, name, source)` or book section IDs so the caller can `get` the full record. Do not build a second corpus.

### `mcp` (later)

Thin stdio MCP server over the same store: `lookup_spell`, `lookup_monster`, `lookup_item`, `search`, `search_rules`. No extra ingest path.

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

**2014 vs 2024.** Treat `XPHB` / `XMM` / `XDMG` as distinct sources. A config key `edition = "2014" | "2024" | "all"` only affects default `get` disambiguation and default search ranking, not what is ingested.

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

v1 can skip embeddings entirely. FTS + fuzzy covers both “fireball” and a lot of rule lookup.

## Output

Human mode: compact, stable, pipeable. No TUI in v1 (a picker can come later). Color on a TTY, plain text otherwise.

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
- **Embeddings (phase 3):** local model writing into the cache dir; sqlite-vec or a sidecar file. Decision deferred until `get`/`search` exist.

## Layout

```text
cmd/5e/              // main
internal/ingest/     // discovery, parse, fluff join, atomic sqlite write
internal/parse/      // entries walker, tag lexer, plaintext render
internal/store/      // sqlite schema, queries
internal/search/     // fuzzy + FTS merge
internal/cli/        // cobra commands, human vs json
third_party/5etools-src/  // submodule, sparse data/
DESIGN.md
```

No public library API in v1. Other tools invoke the binary with `--json`.

## Phases

1. **Submodule + ingest + get + search.** Synthetic fixtures for parser tests. No embeddings. Entity kinds listed above; books/adventures optional if they delay the rest — prefer including them once entity ingest works, because rule search is mostly book sections.
2. **Tag edges + better renderer.** `get` can follow `{@spell}` to related names. Human output looks like a stat block, not a JSON dump.
3. **`ask`.** Embeddings over `text` columns already in sqlite. Same IDs.
4. **`mcp`.** Stdio server wrapping `get`/`search`.

## Distribution

- Source checkout: submodule (or `--data`) + `go build`.
- Releases: ship the binary only. Document that the user must clone 5etools-src (or this repo with submodules) and run `5e ingest --data …`.
- Do not attach `data/` or `index.sqlite` to GitHub releases.

## Open questions

These can wait until the matching phase; they should not block v1 ingest/`get`/`search`.

- Exact Go fuzzy library. **v1: prefix/contains/compact-name plus short Levenshtein; no subsequence matching.**
- Class files explode into `class` + `subclass` + feature rows. Feature names are stored as `Extra Attack (Fighter 5)` so `(kind, name, source)` stays unique. Subraces become `High (Elf)`.
- Edition default (`2024` vs `all`).
- Homebrew: extra JSON files dropped into a user dir, same parser, later.
- Whether `srd` flags in 5etools JSON are reliable enough to offer `--srd` as a filter.

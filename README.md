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
bin/5e encounter goblin --type humanoid --size small
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
5e encounter goblin --type humanoid --size small --cr 1/4
5e encounter "fire resistance" --limit 20 --json
5e encounter dragon --edition 2024 --srd
```

Encounter lookup filters the full monster set before applying `--limit`, so
metadata filters do not hide later matches.

## Random Tables

Roll an indexed 5etools table. Use `--count` for repeated rolls and `--seed`
when a reproducible result is useful:

```sh
5e roll "Weather"
5e roll "Weather" --count 5 --seed 42 --json
5e roll "Encounter Names" --source XGE
```

Numeric first-column ranges are honored when present. Other tables use uniform
row selection.

## Development

```sh
make test
make build
```

The derived SQLite index, embeddings, and 5etools source data are local state
and are not committed to this repository.

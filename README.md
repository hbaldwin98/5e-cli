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

## Development

```sh
make test
make build
```

The derived SQLite index, embeddings, and 5etools source data are local state
and are not committed to this repository.

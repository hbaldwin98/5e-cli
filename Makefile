BINARY ?= 5e
BIN_DIR ?= $(HOME)/.local/bin
DATA ?= third_party/5etools-src/data
INDEX ?= $(HOME)/.cache/5e-cli/index.sqlite

.PHONY: build install data ingest doctor setup test clean

build:
	go build -o bin/$(BINARY) ./cmd/5e

install: build
	mkdir -p "$(BIN_DIR)"
	install -m 755 bin/$(BINARY) "$(BIN_DIR)/$(BINARY)"

data:
	git submodule update --init --depth 1 third_party/5etools-src
	git -C third_party/5etools-src sparse-checkout init --cone
	git -C third_party/5etools-src sparse-checkout set data

ingest: build
	bin/$(BINARY) ingest --data "$(DATA)" --index "$(INDEX)"

doctor: build
	bin/$(BINARY) doctor --data "$(DATA)" --index "$(INDEX)"

setup: data ingest

test:
	go test ./...

clean:
	rm -rf bin

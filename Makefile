PLUGIN := playback-handoff
TINYGO_VERSION := 0.44.0
# TinyGo runs from its official image so no local install is needed.
TINYGO := docker run --rm --user $(shell id -u):$(shell id -g) \
	-e HOME=/tmp -e GOMODCACHE=/gomod -e GOFLAGS=-mod=mod \
	-v $(CURDIR):/src -v $(shell go env GOMODCACHE):/gomod -w /src \
	tinygo/tinygo:$(TINYGO_VERSION) tinygo

.PHONY: all test clean

all: $(PLUGIN).ndp

plugin.wasm: go.mod go.sum $(wildcard *.go)
	$(TINYGO) build -o plugin.wasm -target wasip1 -buildmode=c-shared .

$(PLUGIN).ndp: manifest.json plugin.wasm
	rm -f $@
	zip -j $@ manifest.json plugin.wasm

test:
	go test ./...

clean:
	rm -f plugin.wasm $(PLUGIN).ndp

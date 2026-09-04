# ytclip build
#
# make            local binary for this machine
# make dist       every release target, archived, into dist/
# make linux      just the linux targets
# make windows    just the windows targets
# make test       unit tests
# make check      fmt + vet + test
# make install    copy the local binary onto PATH
# make clean      remove build output
#
# All cross targets are CGO-free, so any host can build any of them.
# `make -j` builds them in parallel.

BINARY  := ytclip
DIST    := dist
PKGDIR  := packaging

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)

GO      ?= go
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)
GOFLAGS := -trimpath

# os/arch pairs. Adding one here is all it takes.
LINUX_TARGETS   := linux/amd64 linux/arm64
WINDOWS_TARGETS := windows/amd64 windows/arm64
TARGETS         := $(LINUX_TARGETS) $(WINDOWS_TARGETS)

# Where PATH installs go when the user is not root.
PREFIX ?= $(HOME)/.local

.DEFAULT_GOAL := build

# ---------------------------------------------------------------- local

.PHONY: build
build:
	$(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BINARY) .
	@echo "built ./$(BINARY)  ($(VERSION))"

.PHONY: run
run: build
	./$(BINARY)

.PHONY: install
install: build
	install -d $(PREFIX)/bin
	install -m755 $(BINARY) $(PREFIX)/bin/$(BINARY)
	@echo "installed $(PREFIX)/bin/$(BINARY)"
	@command -v $(BINARY) >/dev/null 2>&1 || \
		echo "note: $(PREFIX)/bin is not on your PATH"

# ------------------------------------------------------------- checking

.PHONY: test
test:
	$(GO) test ./...

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: fmt
fmt:
	gofmt -w $(shell find . -name '*.go' -not -path './vendor/*')

# fmt-check fails rather than rewrites, which is what CI wants.
.PHONY: fmt-check
fmt-check:
	@out="$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*'))"; \
	if [ -n "$$out" ]; then echo "not gofmt'd:"; echo "$$out"; exit 1; fi

.PHONY: check
check: fmt-check vet test

# ----------------------------------------------------------- cross build
#
# One pair of rules per target, generated. Real targets rather than a
# shell loop, so `make -j` parallelises and a rebuild skips what is
# already current.

define TARGET_RULES

$(DIST)/$(BINARY)_$(VERSION)_$(subst /,_,$1)$(if $(findstring windows,$1),.exe): $(GO_SOURCES)
	@mkdir -p $(DIST)
	@echo "  build  $1"
	@CGO_ENABLED=0 GOOS=$(word 1,$(subst /, ,$1)) GOARCH=$(word 2,$(subst /, ,$1)) \
		$(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $$@ .

.PHONY: $1
$1: $(DIST)/$(BINARY)_$(VERSION)_$(subst /,_,$1)$(if $(findstring windows,$1),.exe)

endef

GO_SOURCES := $(shell find . -name '*.go' -not -path './$(DIST)/*') go.mod go.sum

$(foreach t,$(TARGETS),$(eval $(call TARGET_RULES,$t)))

BINARIES := $(foreach t,$(TARGETS),\
	$(DIST)/$(BINARY)_$(VERSION)_$(subst /,_,$t)$(if $(findstring windows,$t),.exe))

.PHONY: binaries
binaries: $(BINARIES)

.PHONY: linux
linux: $(LINUX_TARGETS)

.PHONY: windows
windows: $(WINDOWS_TARGETS)

# --------------------------------------------------------------- archives
#
# Windows gets a .zip because that is what Explorer opens natively;
# Linux gets .tar.gz to keep the executable bit. Each carries the plain
# instructions for that platform.

.PHONY: package
package: binaries
	@rm -rf $(DIST)/stage
	@set -e; for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		name=$(BINARY)_$(VERSION)_$${os}_$${arch}; \
		stage=$(DIST)/stage/$$name; \
		mkdir -p $$stage; \
		if [ "$$os" = "windows" ]; then \
			cp $(DIST)/$$name.exe $$stage/$(BINARY).exe; \
			cp $(PKGDIR)/START-HERE-windows.txt $$stage/START-HERE.txt; \
		else \
			cp $(DIST)/$$name $$stage/$(BINARY); \
			cp $(PKGDIR)/START-HERE-linux.txt $$stage/START-HERE.txt; \
		fi; \
		cp README.md $$stage/ 2>/dev/null || true; \
		if [ "$$os" = "windows" ]; then \
			( cd $(DIST)/stage && zip -qr ../$$name.zip $$name ); \
			echo "  pack   $$name.zip"; \
		else \
			tar -czf $(DIST)/$$name.tar.gz -C $(DIST)/stage $$name; \
			echo "  pack   $$name.tar.gz"; \
		fi; \
	done
	@rm -rf $(DIST)/stage

# checksums lets anyone verify a download the same way ytclip verifies
# the ones it fetches.
#
# Scoped to this VERSION: a bare *.zip glob picks up archives left over
# from every earlier build in dist/, and a checksums file listing files
# that were not built by this run is worse than none.
ARCHIVES := $(foreach t,$(TARGETS),\
	$(BINARY)_$(VERSION)_$(subst /,_,$t)$(if $(findstring windows,$t),.zip,.tar.gz))

.PHONY: checksums
checksums: package
	@cd $(DIST) && rm -f checksums_$(VERSION).txt && \
		{ sha256sum $(ARCHIVES) 2>/dev/null || shasum -a 256 $(ARCHIVES); } \
			> checksums_$(VERSION).txt
	@echo "  wrote  $(DIST)/checksums_$(VERSION).txt"

.PHONY: dist
dist: check checksums
	@echo
	@cd $(DIST) && ls -lh $(ARCHIVES) checksums_$(VERSION).txt

# ------------------------------------------------------------------ misc

.PHONY: clean
clean:
	rm -rf $(DIST) $(BINARY) $(BINARY).exe

.PHONY: version
version:
	@echo $(VERSION) $(COMMIT)

.PHONY: help
help:
	@awk '/^#/{sub(/^# ?/,"");print;next}{exit}' Makefile

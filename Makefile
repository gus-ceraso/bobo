PREFIX ?= /usr/local
DESTDIR ?=
BINDIR ?= $(DESTDIR)$(PREFIX)/bin

GO ?= go
GOFLAGS ?=
SOURCES := $(shell find . -name '*.go' -not -path './.git/*') go.mod go.sum

.PHONY: all fmt vet check live-check install clean

all: bobo

bobo: $(SOURCES)
	$(GO) build $(GOFLAGS) -o bobo .

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

check:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt: needs formatting:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	$(GO) vet ./...
	$(GO) test ./...

install: bobo
	install -d "$(BINDIR)"
	install -m 0755 bobo "$(BINDIR)/bobo"

clean:
	rm -f bobo

live-check:
	$(GO) test -tags live -count=1 -run "^TestLive" ./...

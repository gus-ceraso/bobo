GO ?= go
PREFIX ?= /usr/local
BINDIR ?= $(PREFIX)/bin
DESTDIR ?=

.PHONY: all build check install clean

all: build

build:
	$(GO) build -o bobo ./cmd/bobo

check:
	$(GO) vet ./...
	$(GO) test -race ./...

install: build
	install -Dm755 bobo "$(DESTDIR)$(BINDIR)/bobo"

clean:
	rm -f bobo

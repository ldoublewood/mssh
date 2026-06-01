BINARY   := mssh
MODULE   := mssh
VERSION  ?= 1.0.0
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD    := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

LDFLAGS  := -s -w \
	-X 'main.Version=$(VERSION)' \
	-X 'main.Commit=$(COMMIT)' \
	-X 'main.BuildTime=$(BUILD)'

.PHONY: all build clean install version

all: build

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

clean:
	rm -f $(BINARY)

install:
	go install -ldflags "$(LDFLAGS)" .

version:
	@echo "version:  $(VERSION)"
	@echo "commit:   $(COMMIT)"
	@echo "built:    $(BUILD)"

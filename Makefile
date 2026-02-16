.PHONY: build clean install

BINARY = podman-debug
PREFIX ?= /usr/local
GO_VERSION ?= 1.26
CONTAINER_ENGINE ?= podman

build:
	$(CONTAINER_ENGINE) run --rm \
		-v $(CURDIR):/src:Z \
		-w /src \
		docker.io/library/golang:$(GO_VERSION) \
		sh -c 'CGO_ENABLED=0 go build -o $(BINARY) ./cmd/podman-debug'

install: build
	install -m 0755 $(BINARY) $(DESTDIR)$(PREFIX)/bin/$(BINARY)

clean:
	rm -f $(BINARY)

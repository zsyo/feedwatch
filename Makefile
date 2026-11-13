.PHONY: build run dry-run once test vet fmt tidy clean docker init build-webui

BINARY := feedwatch.exe

build:
	go build -o $(BINARY) ./cmd/feedwatch

# config.yaml is gitignored; create it once from the tracked template.
# Idempotent: an existing local config is never overwritten.
init:
	@test -f config.yaml || cp config.example.yaml config.yaml

# Full local debug: log notifier, isolated state file, no network sends.
dry-run: build init
	./$(BINARY) -config config.yaml -once -dry-run

once: build init
	./$(BINARY) -config config.yaml -once

run: build init
	./$(BINARY) -config config.yaml

test:
	go test -race -count=1 ./...

vet:
	go vet ./...

fmt:
	gofmt -w ./cmd ./internal

tidy:
	go mod tidy

clean:
	rm -f $(BINARY)

docker:
	docker compose build

# Rebuild the SPA assets that `go build -tags webui` embeds. The generated files
# under internal/webui/embedded/ are gitignored on purpose.
build-webui:
	sh web/build.sh

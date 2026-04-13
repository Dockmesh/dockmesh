.PHONY: dev build test lint docker clean tidy frontend-install frontend-build backend-build agent agent-linux agent-bundle

VERSION ?= dev
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X github.com/dockmesh/dockmesh/pkg/version.Version=$(VERSION) \
	-X github.com/dockmesh/dockmesh/pkg/version.Commit=$(COMMIT) \
	-X github.com/dockmesh/dockmesh/pkg/version.Date=$(DATE)

dev:
	@echo ">> starting backend (:8080) and frontend (:5173)"
	@(cd web && npm run dev) & \
	 (command -v air >/dev/null && air || go run ./cmd/dockmesh)

frontend-install:
	cd web && npm install

frontend-build: frontend-install
	cd web && npm run build
	rm -rf cmd/dockmesh/web_dist
	cp -r web/build cmd/dockmesh/web_dist

backend-build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dockmesh ./cmd/dockmesh

agent:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dockmesh-agent ./cmd/dockmesh-agent

agent-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dockmesh-agent ./cmd/dockmesh-agent

# Cross-compile the agent for both common Linux arches into ./bin/. The
# server's /install/{name} endpoint serves files from this directory so
# the curl-bash installer can download the right binary.
agent-bundle:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/dockmesh-agent-linux-amd64 ./cmd/dockmesh-agent
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/dockmesh-agent-linux-arm64 ./cmd/dockmesh-agent
	@ls -lh bin/

build: frontend-build agent-bundle backend-build
	@echo ">> built ./dockmesh + bin/dockmesh-agent-linux-{amd64,arm64}"

test:
	go test -race ./...
	cd web && npm run check

lint:
	golangci-lint run
	cd web && npm run check

docker:
	docker build -t dockmesh:$(VERSION) .

tidy:
	go mod tidy

clean:
	rm -rf dockmesh dockmesh.exe bin/ dist/ \
		web/build web/.svelte-kit \
		cmd/dockmesh/web_dist/*
	touch cmd/dockmesh/web_dist/.gitkeep

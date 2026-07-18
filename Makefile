.PHONY: bootstrap fmt lint test test-race build run frontend docker check

GO_PACKAGES := ./cmd/... ./internal/...

bootstrap:
	go mod download
	npm --prefix frontend ci

fmt:
	gofmt -w $$(find cmd internal -name '*.go' -type f)

lint:
	go vet $(GO_PACKAGES)
	npm --prefix frontend run lint
	npm --prefix frontend run typecheck

test:
	go test -shuffle=on $(GO_PACKAGES)
	npm --prefix frontend test

test-race:
	go test -race -shuffle=on $(GO_PACKAGES)

build:
	npm --prefix frontend run build
	mkdir -p dist
	go build -trimpath -o dist/seedgraph ./cmd/seedgraph

run:
	go run ./cmd/seedgraph

frontend:
	npm --prefix frontend run dev

docker:
	docker compose build

check: lint test build

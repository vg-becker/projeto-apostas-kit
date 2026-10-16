GO ?= go

.PHONY: fmt vet lint test test-race test-integration build up

fmt:
	gofmt -l .

vet:
	$(GO) vet ./...

lint:
	golangci-lint run

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

test-integration:
	$(GO) test -race -tags=integration ./...

build:
	$(GO) build ./...

up:
	docker compose up --build

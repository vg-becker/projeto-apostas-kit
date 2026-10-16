GO ?= go

.PHONY: fmt vet lint test test-race build up

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

build:
	$(GO) build ./...

up:
	docker compose up --build

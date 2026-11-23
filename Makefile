.PHONY: tidy build test test-integration lint image

COMPOSE_VERSION ?= v2.32.4
IMAGE ?= deployment-agent:local

tidy:
	go mod tidy

build:
	go build -o bin/agent ./cmd/agent

test:
	go test ./...

test-integration:
	go test -tags integration ./...

lint:
	go vet ./...
	gofmt -l .

image:
	docker build --build-arg COMPOSE_VERSION=$(COMPOSE_VERSION) -t $(IMAGE) .

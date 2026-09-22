# lybrix 2.0 Makefile — Go rewrite, split-host: core on VM2, ingest on nsspq.
# Same .env on both hosts; profile picks the service set.

COMPOSE := docker compose -f deploy/docker-compose.yml --env-file .env
GO ?= go

.PHONY: help up-core up-ingest up down-core down-ingest down logs-core logs-ingest \
        test lint vet build ps-core ps-ingest restart-core restart-ingest pull-core pull-ingest \
        keys-bootstrap

help:
	@echo "core (VM2):    make up-core / down-core / logs-core / ps-core / pull-core"
	@echo "ingest (nsspq): make up-ingest / down-ingest / logs-ingest / ps-ingest / pull-ingest"
	@echo "go:            make build / test / lint / vet"
	@echo "first boot:    schema applies at serve boot (embedded); keys via: make keys-bootstrap"

build:
	$(GO) build ./...

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

lint:
	golangci-lint run

up-core:
	$(COMPOSE) --profile core up -d

up-ingest:
	$(COMPOSE) --profile ingest up -d

down-core:
	$(COMPOSE) --profile core down

down-ingest:
	$(COMPOSE) --profile ingest down

up: up-core up-ingest
down: down-core down-ingest

logs-core:
	$(COMPOSE) --profile core logs -f --tail=50

logs-ingest:
	$(COMPOSE) --profile ingest logs -f --tail=50

ps-core:
	$(COMPOSE) --profile core ps

ps-ingest:
	$(COMPOSE) --profile ingest ps

pull-core:
	$(COMPOSE) --profile core pull

pull-ingest:
	$(COMPOSE) --profile ingest pull

restart-core:
	$(COMPOSE) --profile core up -d --force-recreate

restart-ingest:
	$(COMPOSE) --profile ingest up -d --force-recreate

keys-bootstrap:
	$(COMPOSE) --profile core exec lybrix-server /app/lybrix-server keys bootstrap admin

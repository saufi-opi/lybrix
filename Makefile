# rag-platform Makefile — split-host: core on VM2, ingest on nsspq.
# Same .env on both hosts; profile picks the service set.

COMPOSE := docker compose -f deploy/docker-compose.yml --env-file .env
GPU ?= 0

.PHONY: help up-core up-ingest up down-core down-ingest down logs-core logs-ingest \
        migrate test lint ps-core ps-ingest restart-core restart-ingest pull-core pull-ingest

help:
	@echo "core (VM2):    make up-core / down-core / logs-core / ps-core / pull-core"
	@echo "ingest (nsspq): make up-ingest / down-ingest / logs-ingest / ps-ingest / pull-ingest"
	@echo "first boot:    make migrate (on VM2, after up-core started postgres)"

up-core:
	$(COMPOSE) --profile core up -d

up-ingest:
	$(COMPOSE) --profile ingest up -d

migrate:
	$(COMPOSE) --profile core run --rm migrate

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

test:
	uv sync --locked --group dev --all-packages
	uv run pytest tests/ -q

lint:
	uvx ruff check .

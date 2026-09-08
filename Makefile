# rag-platform Makefile — PRD §12: "up, down, logs, migrate, seed, test, scale".
# Nobody should have to remember the overlay ordering (§13.4).

COMPOSE_FILE := deploy/docker-compose.yml
GPU ?= 1
ifeq ($(GPU),1)
  OVERLAYS := -f $(COMPOSE_FILE) -f deploy/docker-compose.gpu.yml
else
  OVERLAYS := -f $(COMPOSE_FILE)
endif
DEV_OVERLAY := $(OVERLAYS) -f deploy/docker-compose.dev.yml

.PHONY: help up down dev logs migrate test lint scale drain seed ps

help:
	@echo "make up        - start the stack (GPU=0 for CPU-only TEI)"
	@echo "make dev       - start with dev overlay (hot reload, exposed ports)"
	@echo "make down      - stop the stack"
	@echo "make logs      - follow all logs"
	@echo "make migrate   - run alembic migrations"
	@echo "make test      - run the test suite"
	@echo "make lint      - ruff check"
	@echo "make scale N=8 - set worker-parser replicas (the throughput dial)"
	@echo "make drain     - stop splitters+parsers, let in-flight shards finish"

up:
	docker compose $(OVERLAYS) run --rm migrate
	docker compose $(OVERLAYS) up -d

dev:
	docker compose $(DEV_OVERLAYS) run --rm migrate
	docker compose $(DEV_OVERLAYS) up -d

down:
	docker compose $(OVERLAYS) down

logs:
	docker compose $(OVERLAYS) logs -f --tail=100

migrate:
	docker compose $(OVERLAYS) run --rm migrate

test:
	uv sync --locked --group dev --all-packages
	uv run pytest tests/ -q

lint:
	uvx ruff check .

scale:
	docker compose $(OVERLAYS) up -d --no-deps --scale worker-parser=$(N) worker-parser

drain:
	docker compose $(OVERLAYS) stop worker-splitter
	@echo "splitters stopped; parsers will finish in-flight shards."
	@echo "Watch 'make logs' until doc.parse drains, then: make down"

seed:
	uv run python -m scripts.backfill data/seed --collection default

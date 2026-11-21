-include .env

MIGRATIONS_DIR := migrations
DATABASE_URL := postgres://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@postgres:5432/$(POSTGRES_DB)?sslmode=disable
TOOLS := docker compose run --rm -T tools

TEST_POSTGRES_PORT ?= 55432
TEST_REDIS_PORT ?= 56379
TEST_DATABASE_URL ?= postgres://eem:eem@localhost:$(TEST_POSTGRES_PORT)/eem_test?sslmode=disable
TEST_REDIS_URL ?= redis://localhost:$(TEST_REDIS_PORT)/1
TEST_DOCKER ?= 1
# make test starts every dependency, so a skipped test is a failure.
TEST_REQUIRE_ALL ?= 1

export TEST_POSTGRES_PORT TEST_REDIS_PORT TEST_DATABASE_URL TEST_REDIS_URL TEST_DOCKER TEST_REQUIRE_ALL

.PHONY: migrate-up migrate-down migrate-new api-key admin seed-docker test test-db test-db-down lint

migrate-up:
	docker compose run --rm migrate

migrate-down:
	docker compose run --rm migrate -path /migrations -database "$(DATABASE_URL)" down 1

migrate-new:
	migrate create -ext sql -dir $(MIGRATIONS_DIR) -seq $(name)

api-key:
	@$(TOOLS) /tools/generate_api_key $(ARGS)

admin:
	@printf "Password: "; stty -echo; read -r pw; stty echo; echo; \
		printf '%s\n' "$$pw" | $(TOOLS) /tools/create_admin $(ARGS)

seed-docker:
	@$(TOOLS) /tools/seed_docker $(ARGS)

test: test-db
	go test -race -count=1 ./...

test-db:
	@docker compose -f compose.test.yaml up -d --wait

test-db-down:
	@docker compose -f compose.test.yaml down

lint:
	golangci-lint run ./...

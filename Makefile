-include .env

DATABASE_URL ?= postgres://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@localhost:5432/$(POSTGRES_DB)?sslmode=disable
MIGRATIONS_DIR := migrations
TEST_DB := $(POSTGRES_DB)_test
TEST_DATABASE_URL ?= postgres://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@localhost:5432/$(TEST_DB)?sslmode=disable
TEST_DOCKER ?= 1

export TEST_DATABASE_URL TEST_DOCKER

.PHONY: migrate-up migrate-down migrate-new api-key admin seed-docker test test-db

migrate-up:
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" up

migrate-down:
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" down 1

migrate-new:
	migrate create -ext sql -dir $(MIGRATIONS_DIR) -seq $(name)

api-key:
	@DATABASE_URL="$(DATABASE_URL)" go run ./scripts/generate_api_key.go $(ARGS)

admin:
	@printf "Password: "; stty -echo; read -r pw; stty echo; echo; \
		printf '%s\n' "$$pw" | DATABASE_URL="$(DATABASE_URL)" go run ./scripts/create_admin $(ARGS)

seed-docker:
	@DATABASE_URL="$(DATABASE_URL)" go run ./scripts/seed_docker $(ARGS)

test: test-db
	go test -race -count=1 ./...

test-db:
	@docker compose up -d --wait postgres
	@docker compose exec -T postgres psql -U $(POSTGRES_USER) -d $(POSTGRES_DB) -tAc "SELECT 1 FROM pg_database WHERE datname = '$(TEST_DB)'" | grep -q 1 || \
		docker compose exec -T postgres createdb -U $(POSTGRES_USER) $(TEST_DB)

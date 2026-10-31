DATABASE_URL ?= postgres://postgres:postgres@localhost:5432/eem?sslmode=disable
MIGRATIONS_DIR := migrations

.PHONY: migrate-up migrate-down migrate-new

migrate-up:
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" up

migrate-down:
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" down 1

migrate-new:
	migrate create -ext sql -dir $(MIGRATIONS_DIR) -seq $(name)

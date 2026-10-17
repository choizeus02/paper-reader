DATA_DIR ?= $(CURDIR)/data
TEST_DATABASE_URL ?= postgres://postgres:pg@localhost:55432/postgres?sslmode=disable

.PHONY: dev test test-unit test-db-up test-db-down

dev:
	infisical run --env=dev -- env DATA_DIR=$(DATA_DIR) go -C backend run ./cmd/server

test-unit:
	cd backend && go test ./...

test:
	cd backend && TEST_DATABASE_URL='$(TEST_DATABASE_URL)' go test ./...

test-db-up:
	docker run -d --name paper-reader-testdb -e POSTGRES_PASSWORD=pg -p 55432:5432 postgres:17

test-db-down:
	docker rm -f paper-reader-testdb

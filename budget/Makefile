.PHONY: build web-build run test test-db lint migrate-up migrate-down db-up db-down compose-up compose-down

BIN := bot
# Отдельная база: тесты делают truncate, и указывать сюда боевую нельзя —
# один make test-db стёр бы историю трат.
TEST_DATABASE_URL ?= postgres://budget:budget@127.0.0.1:5433/budget_test?sslmode=disable

build: web-build
	go build -o $(BIN) ./cmd/bot
	go build -o classify-test ./cmd/classify-test

run:
	go run ./cmd/bot

test:
	go test ./...

test-db:
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -race -p 1 ./...

# Сборка фронта кладётся в internal/web/dist и уезжает в бинарь через embed.
web-build:
	cd web && npm ci && npm run build

lint:
	gofmt -l .
	go vet ./...

migrate-up:
	go run ./cmd/bot -migrate

migrate-down:
	go run ./cmd/bot -migrate-down

# Локальная база для разработки. На проде этот же сервис хранит данные в томе.
db-up:
	sudo docker compose up -d postgres

db-down:
	sudo docker compose down

# Пересобрать бинарник и запустить бот с PostgreSQL в Docker Compose.
compose-up: build
	sudo docker compose up -d --build

compose-down:
	sudo docker compose down

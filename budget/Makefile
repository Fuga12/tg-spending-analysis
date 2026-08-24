.PHONY: build web run test test-db lint migrate-up migrate-down db-up db-down compose-up compose-migrate compose-down

BIN := bot
# Отдельная база: тесты делают truncate, и указывать сюда боевую нельзя —
# один make test-db стёр бы историю трат.
TEST_DATABASE_URL ?= postgres://budget:budget@127.0.0.1:5433/budget_test?sslmode=disable

# Фронт собирается до бинаря: он уезжает в него через embed, и бинарь,
# собранный поверх старого dist, молча отдаёт старое приложение.
build: web
	go build -o $(BIN) ./cmd/bot
	go build -o classify-test ./cmd/classify-test

web:
	cd web && npm ci && npm run build

run:
	go run ./cmd/bot

test:
	go test ./...

test-db:
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -race -p 1 ./...

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

# Собрать образ и поднять бот с PostgreSQL. Фронт и бинарь собираются внутри
# образа, поэтому make build здесь не нужен.
compose-up:
	sudo docker compose up -d --build

# Миграции в докере: отдельным разовым запуском, а не при старте бота.
compose-migrate:
	sudo docker compose run --rm bot -migrate

compose-down:
	sudo docker compose down

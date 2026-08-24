# Сборка целиком в образе: и фронт, и бинарь.
#
# Раньше сюда клали бинарь, собранный на машине. Так нельзя: фронт уезжает
# внутрь бинаря через embed, и образ, собранный поверх чужого dist, молча
# отдаёт чужое приложение — без единой ошибки, которую видно снаружи.

# --- фронт ---
# Пишет в ../internal/app/dist, так задано в vite.config.ts.
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# --- бинарь ---
FROM golang:1.25 AS build
WORKDIR /src
# Зависимости отдельным слоем: они меняются куда реже кода, и пересобирать
# их на каждую правку комментария незачем.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Свежесобранный фронт кладётся поверх того, что приехало с машины.
COPY --from=web /src/internal/app/dist ./internal/app/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bot ./cmd/bot

# --- то, что едет в прод ---
# distroless: ни оболочки, ни пакетного менеджера, ни лишнего. Корневые
# сертификаты внутри есть — без них не открыть ни Telegram, ни Yandex.
# Часовые пояса бинарь несёт в себе (см. time/tzdata в cmd/bot).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/bot /bot
# Аргументы доходят до бинаря: docker compose run --rm bot -migrate.
ENTRYPOINT ["/bot"]

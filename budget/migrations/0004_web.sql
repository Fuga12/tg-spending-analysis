-- +goose Up
-- Одноразовые ссылки входа: бот выдаёт, сайт обменивает на сессию.
-- Хранится SHA-256 токена, а не сам токен: дамп базы не должен давать вход.
create table login_tokens (
    token_hash  bytea primary key,
    user_id     bigint not null references users(id),
    created_at  timestamptz not null default now(),
    expires_at  timestamptz not null,
    used_at     timestamptz
);

create table web_sessions (
    token_hash    bytea primary key,
    user_id       bigint not null references users(id),
    created_at    timestamptz not null default now(),
    last_seen_at  timestamptz not null default now(),
    expires_at    timestamptz not null,
    user_agent    text
);

create index web_sessions_user_idx on web_sessions (user_id);

-- Правка задним числом должна быть видна хотя бы по метке времени. Она же —
-- версия записи: сайт, бот и воркер правят одну строку, и без неё правка
-- человека молча затирается догадкой модели (webapp.md §4).
alter table transactions add column updated_at timestamptz;

-- +goose Down
alter table transactions drop column updated_at;
drop table web_sessions;
drop table login_tokens;

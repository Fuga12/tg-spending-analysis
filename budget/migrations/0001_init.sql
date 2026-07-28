-- +goose Up
create table users (
    id          bigint primary key,          -- telegram user id
    name        text   not null,
    created_at  timestamptz not null default now()
);

create table categories (
    id                   serial primary key,
    name                 text not null unique,
    default_beneficiary  text not null default 'both'
        check (default_beneficiary in ('payer','partner','both')),
    sort_order           int  not null default 100
);

-- общая затравка, одна на всех, заполняется миграцией
create table word_seed (
    word         text primary key,
    category_id  int  not null references categories(id),
    beneficiary  text check (beneficiary in ('payer','partner','both'))
);

-- личный кэш: слово → категория (и опционально бенефициар)
create table word_map (
    word         text   not null,
    user_id      bigint not null references users(id),
    category_id  int    not null references categories(id),
    beneficiary  text   check (beneficiary in ('payer','partner','both')),
    hits         int    not null default 1,
    source       text   not null default 'llm' check (source in ('llm','manual')),
    updated_at   timestamptz not null default now(),
    primary key (word, user_id)
);

create table transactions (
    id                   bigserial primary key,
    payer_id             bigint not null references users(id),
    beneficiary          text   not null check (beneficiary in ('payer','partner','both')),
    kind                 text   not null check (kind in ('expense','income','transfer')),
    amount               numeric(12,2) not null check (amount > 0),
    description          text   not null,
    category_id          int    references categories(id),
    raw_text             text   not null,
    needs_classification boolean not null default false,
    spent_at             timestamptz not null default now(),   -- дата траты
    created_at           timestamptz not null default now(),   -- дата записи
    deleted_at           timestamptz
);

create index tx_spent_idx  on transactions (spent_at) where deleted_at is null;
create index tx_payer_idx  on transactions (payer_id, spent_at) where deleted_at is null;
create index tx_needs_idx  on transactions (needs_classification) where needs_classification and deleted_at is null;

-- учёт расхода токенов
create table llm_usage (
    id                bigserial primary key,
    created_at        timestamptz not null default now(),
    model             text   not null,
    prompt_tokens     int    not null default 0,
    completion_tokens int    not null default 0,
    ok                boolean not null,
    error_kind        text                       -- timeout | quota | http | schema | other
);

create index llm_usage_month_idx on llm_usage (created_at);

-- +goose Down
drop table llm_usage;
drop table transactions;
drop table word_map;
drop table word_seed;
drop table categories;
drop table users;

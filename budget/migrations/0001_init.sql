-- +goose Up

-- Схема бюджета на группу до десяти человек.
--
-- Прежние девять миграций описывали бота на двоих: получатель траты хранился
-- относительно плательщика («payer», «partner»), а второй человек выводился
-- из того, что людей ровно двое. При десяти участниках это невыразимо, и
-- переписывать историю миграций не пришлось — данных ещё нет. Поэтому одна
-- чистая, а не десятая поверх девяти.

create table users (
    id            bigint primary key,          -- telegram user id
    name          text   not null check (char_length(name) between 1 and 32),
    -- Своё фото профиля. Telegram отдаёт фото не всем: у кого-то его нет,
    -- у кого-то оно закрыто настройками приватности. Байты лежат в базе, а не
    -- файлом рядом: бэкап у нас — дамп базы, и файл в него не попадёт.
    avatar        bytea,
    -- Он же версия картинки: уезжает в URL и сбрасывает кэш браузера.
    avatar_set_at timestamptz,
    created_at    timestamptz not null default now()
);

create table groups (
    id         bigserial primary key,
    name       text not null check (char_length(name) between 1 and 64),
    created_at timestamptz not null default now()
);

-- Участник — всегда telegram-аккаунт: user_id not null. Именованных корзин
-- («дети», «родители») нет, получателем может быть только участник.
create table members (
    id        bigserial primary key,
    group_id  bigint not null references groups(id) on delete cascade,
    user_id   bigint not null references users(id),
    role      text   not null default 'member' check (role in ('admin', 'member')),
    joined_at timestamptz not null default now(),
    -- Ушедший участник не удаляется: на него ссылаются его же траты, и без
    -- строки в members история группы теряет плательщика. Отсюда left_at, а
    -- правило «один человек — одна группа» держится частичным индексом ниже.
    left_at   timestamptz,
    -- Транзакции и категории ссылаются парой (id, group_id): так чужой
    -- участник не может оказаться плательщиком в чужой группе даже при
    -- ошибке в коде.
    unique (id, group_id)
);

create unique index members_one_group_idx on members (user_id) where left_at is null;
create index members_group_idx on members (group_id);

-- Приглашает администратор, но только того, кто уже стартовал бота: отсюда
-- внешний ключ на users, а не текстовый логин или телефон.
create table invites (
    id              bigserial primary key,
    group_id        bigint not null references groups(id) on delete cascade,
    invitee_user_id bigint not null references users(id),
    invited_by      bigint not null references users(id),
    created_at      timestamptz not null default now(),
    expires_at      timestamptz not null,
    accepted_at     timestamptz,
    declined_at     timestamptz
);

-- Открытое приглашение в группу человеку может быть только одно.
create unique index invites_open_idx on invites (group_id, invitee_user_id)
    where accepted_at is null and declined_at is null;
create index invites_invitee_idx on invites (invitee_user_id)
    where accepted_at is null and declined_at is null;

-- Шаблон категорий: из него раскатывается список новой группы. Отдельной
-- таблицей, а не константой в коде, потому что затравка словаря ссылается
-- на template_key и должна ссылаться на что-то существующее.
create table category_template (
    template_key text primary key,
    name         text not null,
    hint         text not null default '',
    sort_order   int  not null
);

create table categories (
    id       serial primary key,
    group_id bigint not null references groups(id) on delete cascade,
    name     text   not null check (char_length(name) between 1 and 64),
    -- Подсказка уходит в описание категории внутри JSON-схемы: пояснения там
    -- заметно поднимают точность разбора.
    hint     text   not null default '',
    -- Ключ шаблона, из которого категория раскатана. Общая на всех затравка
    -- словаря не может ссылаться на category_id конкретной группы — только на
    -- ключ. Null у категории, заведённой людьми вручную: слов в затравке для
    -- неё нет и быть не может, а выдумывать ей синтетический ключ значит
    -- засорять пространство имён, общее для всех групп.
    template_key      text references category_template(template_key),
    -- Адресат-человек, если он у категории есть: «Косметика — Уле» верно и
    -- когда платит не Уля. Null означает «на всю группу».
    default_member_id bigint references members(id) on delete set null,
    sort_order        int not null default 100,
    unique (group_id, name),
    unique (id, group_id)
);

create unique index categories_template_idx on categories (group_id, template_key)
    where template_key is not null;

create table transactions (
    id                   bigserial primary key,
    group_id             bigint not null references groups(id) on delete cascade,
    payer_member_id      bigint not null,
    kind                 text   not null check (kind in ('expense', 'income', 'transfer')),
    amount               numeric(12,2) not null check (amount > 0),
    description          text   not null,
    category_id          int,
    raw_text             text   not null,
    -- Запись сохранена, но категорию поставит воркер добора: модель не
    -- ответила или была недоступна. Флаг живёт минуты.
    needs_classification boolean not null default false,
    -- Категорию выбрал не человек и не модель, а воркер вслепую, лишь бы
    -- запись не висела в очереди вечно. Приложение показывает такие отдельно.
    needs_review         boolean not null default false,
    spent_at             timestamptz not null default now(),   -- дата траты
    created_at           timestamptz not null default now(),   -- дата записи
    -- Версия записи: одну строку правят бот, воркер и приложение.
    updated_at           timestamptz,
    deleted_at           timestamptz,

    foreign key (payer_member_id, group_id) references members (id, group_id),
    foreign key (category_id, group_id)     references categories (id, group_id)
);

create index tx_group_spent_idx on transactions (group_id, spent_at) where deleted_at is null;
create index tx_payer_idx       on transactions (payer_member_id, spent_at) where deleted_at is null;
create index tx_needs_idx       on transactions (id) where needs_classification and deleted_at is null;
create index tx_review_idx      on transactions (group_id) where needs_review and deleted_at is null;

-- На кого потрачено. Пустой список получателей означает «на всю группу»:
-- при десяти участниках перечислять всех в каждой строке — это девять лишних
-- записей на трату и девять чужих строк, которые придётся чистить при выходе
-- участника.
create table tx_recipients (
    transaction_id bigint not null references transactions(id) on delete cascade,
    member_id      bigint not null references members(id) on delete cascade,
    primary key (transaction_id, member_id)
);

create index tx_recipients_member_idx on tx_recipients (member_id);

-- Общая затравка словаря, одна на всех. Ссылается на ключ шаблона, а не на
-- категорию: категории у каждой группы свои.
create table word_seed (
    word         text primary key,
    template_key text not null references category_template(template_key)
);

-- Личный словарь: слово → категория (и, с фазы 2, получатель).
create table word_map (
    group_id    bigint not null references groups(id) on delete cascade,
    user_id     bigint not null references users(id),
    word        text   not null,
    category_id int    not null,
    member_id   bigint references members(id) on delete set null,
    source      text   not null default 'llm' check (source in ('llm', 'manual')),
    hits        int    not null default 1,
    updated_at  timestamptz not null default now(),
    primary key (group_id, user_id, word),
    foreign key (category_id, group_id) references categories (id, group_id) on delete cascade
);

-- Учёт расхода токенов. group_id закладывается сразу, хотя тарификация групп
-- будет позже: добавить колонку в таблицу с историей дороже, чем завести её
-- пустой. Null означает вызов вне группы.
create table llm_usage (
    id                bigserial primary key,
    group_id          bigint references groups(id) on delete set null,
    model             text    not null,
    prompt_tokens     int     not null default 0,
    completion_tokens int     not null default 0,
    ok                boolean not null,
    error_kind        text,                      -- timeout | quota | http | schema | other
    created_at        timestamptz not null default now()
);

create index llm_usage_month_idx on llm_usage (created_at);
create index llm_usage_group_idx on llm_usage (group_id, created_at);

-- +goose Down
drop table llm_usage;
drop table word_map;
drop table word_seed;
drop table tx_recipients;
drop table transactions;
drop table categories;
drop table category_template;
drop table invites;
drop table members;
drop table groups;
drop table users;

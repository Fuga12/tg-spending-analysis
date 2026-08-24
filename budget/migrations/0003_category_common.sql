-- +goose Up

-- Третье умолчание категории: «на всю группу».
--
-- Умолчаний было два — конкретный человек и, при пустом default_member_id,
-- плательщик. Общей траты среди них не было, хотя само понятие есть везде
-- остальное: в tx_recipients (пустой список), в отчётах отдельной строкой, в
-- фильтре списка, в промпте («на всех»). Не хватало его ровно там, где его
-- настраивают как привычку, — а это самый частый случай: продукты, дом,
-- коммуналка почти всегда общие, и каждое «продукты 3200» до сих пор
-- записывалось на того, кто в тот раз расплатился.
--
-- Отдельный флаг, а не значение-заглушка в default_member_id: колонка ссылается
-- на members, и никакое число там не может означать «никто».

alter table categories
    add column default_common boolean not null default false;

-- Выбор один, значит и состояние одно. Без этой проверки в базе завелось бы
-- «общее и при этом на Улю» — набор, который пришлось бы разруливать при
-- каждом чтении, договариваясь с самим собой о том, что главнее.
alter table categories
    add constraint categories_default_one_of
    check (not (default_common and default_member_id is not null));

-- Комментарий к default_member_id обещал обратное тому, что делал код: «Null
-- означает «на всю группу»». Null означал и означает плательщика — это остаток
-- от бота на двоих, где общая трата и была тратой на них обоих.
comment on column categories.default_member_id is
    'Адресат-человек, если он у категории есть: «Косметика — Уле» верно и когда платит не Уля. Null — на того, кто заплатил, если рядом не поднят default_common.';
comment on column categories.default_common is
    'Записывать такие траты на всю группу. Взаимоисключает default_member_id.';

-- +goose Down

alter table categories drop constraint categories_default_one_of;
alter table categories drop column default_common;

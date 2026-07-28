-- +goose Up
-- Флаг needs_classification живёт минуты: воркер разбирает такие записи
-- каждые десять минут, а через два часа закрывает «Прочим» вслепую.
-- Человеку нужны именно эти — и в списке они неотличимы от честно
-- выбранного «Прочего» (webapp-design.md §3.8).
alter table transactions add column needs_review boolean not null default false;

create index tx_review_idx on transactions (needs_review)
    where needs_review and deleted_at is null;

-- +goose Down
drop index tx_review_idx;
alter table transactions drop column needs_review;

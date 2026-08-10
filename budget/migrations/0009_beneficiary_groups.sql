-- +goose Up
create table beneficiary_groups (
    id         serial primary key,
    name       text not null unique check (char_length(name) between 1 and 32),
    sort_order int  not null default 100
);

insert into beneficiary_groups (name, sort_order) values ('Другие', 10);

alter table transactions drop constraint transactions_beneficiary_check;
alter table transactions add constraint transactions_beneficiary_check
    check (beneficiary in ('payer', 'partner', 'both') or beneficiary ~ '^group:[1-9][0-9]*$');

alter table categories drop constraint categories_default_beneficiary_check;
alter table categories add constraint categories_default_beneficiary_check
    check (default_beneficiary in ('payer', 'partner', 'both') or default_beneficiary ~ '^group:[1-9][0-9]*$');

alter table word_map drop constraint word_map_beneficiary_check;
alter table word_map add constraint word_map_beneficiary_check
    check (beneficiary is null or beneficiary in ('payer', 'partner', 'both') or beneficiary ~ '^group:[1-9][0-9]*$');

-- +goose Down
update transactions set beneficiary = 'both' where beneficiary like 'group:%';
update categories set default_beneficiary = 'both' where default_beneficiary like 'group:%';
update word_map set beneficiary = null where beneficiary like 'group:%';

alter table word_map drop constraint word_map_beneficiary_check;
alter table word_map add constraint word_map_beneficiary_check
    check (beneficiary in ('payer', 'partner', 'both'));
alter table categories drop constraint categories_default_beneficiary_check;
alter table categories add constraint categories_default_beneficiary_check
    check (default_beneficiary in ('payer', 'partner', 'both'));
alter table transactions drop constraint transactions_beneficiary_check;
alter table transactions add constraint transactions_beneficiary_check
    check (beneficiary in ('payer', 'partner', 'both'));

drop table beneficiary_groups;

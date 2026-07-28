#!/usr/bin/env bash
# Бэкап базы бюджета. Не опция: полугодовая история трат восстановлению
# не подлежит (§12).
#
# Крон раз в сутки:
#   15 4 * * * /opt/budget/deploy/backup.sh >> /var/log/budget-backup.log 2>&1

set -euo pipefail

BACKUP_DIR="${BACKUP_DIR:-/var/backups/budget}"
KEEP_DAYS="${KEEP_DAYS:-30}"
ENV_FILE="${ENV_FILE:-/opt/budget/.env}"
# Куда разворачивается дамп на проверку. База временная, живёт секунды.
CHECK_DB="${CHECK_DB:-budget_restore_check}"

if [[ -f "$ENV_FILE" ]]; then
    # shellcheck disable=SC1090
    set -a && source "$ENV_FILE" && set +a
fi

if [[ -z "${DATABASE_URL:-}" ]]; then
    echo "DATABASE_URL не задан (ни в окружении, ни в $ENV_FILE)" >&2
    exit 1
fi

# Молчаливо сломанный бэкап хуже отсутствующего: на него рассчитывают.
# Про поломку узнаёт владелец — первый в whitelist.
alert() {
    [[ -n "${BOT_TOKEN:-}" && -n "${ALLOWED_USER_IDS:-}" ]] || return 0
    curl -sS --max-time 10 -o /dev/null \
        --data-urlencode "chat_id=${ALLOWED_USER_IDS%%,*}" \
        --data-urlencode "text=$1" \
        "https://api.telegram.org/bot$BOT_TOKEN/sendMessage" || true
}

# $BASH_COMMAND — команда до подстановки переменных, пароль из DATABASE_URL
# в сообщение не попадёт.
trap 'alert "Бэкап бюджета не сделан. Оборвалось на строке $LINENO: $BASH_COMMAND"' ERR

mkdir -p "$BACKUP_DIR"
STAMP="$(date +%Y-%m-%d_%H%M)"
FILE="$BACKUP_DIR/budget-$STAMP.sql.gz"
TMP="$FILE.tmp"

# Недоделанный дамп не должен оставаться на диске ни при какой ошибке.
trap 'rm -f "$TMP"' EXIT

# Сколько записей было на момент снимка: pg_dump берёт согласованный снимок
# в начале работы, поэтому в дампе окажется не меньше этого числа.
BEFORE="$(psql -tAqX "$DATABASE_URL" -c 'select count(*) from transactions')"

# --clean --if-exists: дамп можно накатить на непустую базу.
pg_dump --clean --if-exists --no-owner "$DATABASE_URL" | gzip > "$TMP"

# Пустой дамп — это не бэкап, а иллюзия бэкапа. Проверяем до публикации:
# файл под финальным именем обязан быть годным.
if [[ "$(stat -c %s "$TMP")" -lt 1024 ]]; then
    echo "дамп подозрительно мал, публиковать не буду: $TMP" >&2
    exit 1
fi

# Непроверенный бэкап — обещание, а не бэкап. Разворачиваем во временную базу
# и сверяем количество записей: дамп, который не встаёт или встаёт пустым,
# до каталога не доезжает.
ADMIN_URL="$(printf '%s' "$DATABASE_URL" | sed -E 's#/[^/?]+(\?|$)#/postgres\1#')"
CHECK_URL="$(printf '%s' "$DATABASE_URL" | sed -E "s#/[^/?]+(\?|\$)#/$CHECK_DB\1#")"

# Страховка от опечатки в CHECK_DB: временную базу мы дропаем, и промахнуться
# по боевой нельзя ни при каких обстоятельствах.
if [[ "$CHECK_URL" == "$DATABASE_URL" ]]; then
    echo "CHECK_DB совпадает с боевой базой — проверку не делаю" >&2
    exit 1
fi

drop_check_db() {
    psql -tAqX "$ADMIN_URL" -c "drop database if exists \"$CHECK_DB\"" >/dev/null 2>&1 || true
}
trap 'rm -f "$TMP"; drop_check_db' EXIT

drop_check_db
psql -tAqX "$ADMIN_URL" -c "create database \"$CHECK_DB\"" >/dev/null
gunzip -c "$TMP" | psql -qX --set ON_ERROR_STOP=1 "$CHECK_URL" >/dev/null
AFTER="$(psql -tAqX "$CHECK_URL" -c 'select count(*) from transactions')"
drop_check_db

if [[ "$AFTER" -lt "$BEFORE" ]]; then
    echo "дамп разворачивается неполно: записей $AFTER, ожидалось не меньше $BEFORE" >&2
    exit 1
fi

mv "$TMP" "$FILE"

# Хвосты от прошлых сбоев подбираем заодно.
find "$BACKUP_DIR" -name 'budget-*.sql.gz.tmp' -mmin +60 -delete
find "$BACKUP_DIR" -name 'budget-*.sql.gz' -mtime "+$KEEP_DAYS" -delete

echo "$(date -Is) готов $FILE ($(du -h "$FILE" | cut -f1)), развернулось: $AFTER записей"

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

if [[ -f "$ENV_FILE" ]]; then
    # shellcheck disable=SC1090
    set -a && source "$ENV_FILE" && set +a
fi

if [[ -z "${DATABASE_URL:-}" ]]; then
    echo "DATABASE_URL не задан (ни в окружении, ни в $ENV_FILE)" >&2
    exit 1
fi

mkdir -p "$BACKUP_DIR"
STAMP="$(date +%Y-%m-%d_%H%M)"
FILE="$BACKUP_DIR/budget-$STAMP.sql.gz"
TMP="$FILE.tmp"

# Недоделанный дамп не должен оставаться на диске ни при какой ошибке.
trap 'rm -f "$TMP"' EXIT

# --clean --if-exists: дамп можно накатить на непустую базу.
pg_dump --clean --if-exists --no-owner "$DATABASE_URL" | gzip > "$TMP"

# Пустой дамп — это не бэкап, а иллюзия бэкапа. Проверяем до публикации:
# файл под финальным именем обязан быть годным.
if [[ "$(stat -c %s "$TMP")" -lt 1024 ]]; then
    echo "дамп подозрительно мал, публиковать не буду: $TMP" >&2
    exit 1
fi

mv "$TMP" "$FILE"

# Хвосты от прошлых сбоев подбираем заодно.
find "$BACKUP_DIR" -name 'budget-*.sql.gz.tmp' -mmin +60 -delete
find "$BACKUP_DIR" -name 'budget-*.sql.gz' -mtime "+$KEEP_DAYS" -delete

echo "$(date -Is) готов $FILE ($(du -h "$FILE" | cut -f1))"

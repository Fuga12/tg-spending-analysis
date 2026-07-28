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

# --clean --if-exists: дамп можно накатить на непустую базу.
pg_dump --clean --if-exists --no-owner "$DATABASE_URL" | gzip > "$FILE.tmp"
mv "$FILE.tmp" "$FILE"

# Пустой дамп — это не бэкап, а иллюзия бэкапа.
if [[ "$(stat -c %s "$FILE")" -lt 1024 ]]; then
    echo "дамп подозрительно мал: $FILE" >&2
    exit 1
fi

find "$BACKUP_DIR" -name 'budget-*.sql.gz' -mtime "+$KEEP_DAYS" -delete

echo "$(date -Is) готов $FILE ($(du -h "$FILE" | cut -f1))"

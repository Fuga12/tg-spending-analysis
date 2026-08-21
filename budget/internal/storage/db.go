package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"budget/migrations"
)

// Store — административный доступ к БД. Голый SQL через pgx, никаких ORM.
//
// Здесь живёт только то, что группе не принадлежит: люди, сами группы,
// очередь воркера, общий расход токенов. Всё остальное — через ForGroup.
type Store struct {
	pool *pgxpool.Pool
}

// GroupStore — то же хранилище, но привязанное к одной группе.
//
// Данные группы читаются и пишутся только отсюда, и group_id подставляется
// в каждый запрос сам. Это главная защита от худшего бага мультитенантности:
// запрос без фильтра по группе нельзя написать по невнимательности — для
// этого надо намеренно взять административный Store.
type GroupStore struct {
	pool    *pgxpool.Pool
	groupID int64
}

// ForGroup сужает хранилище до одной группы.
func (s *Store) ForGroup(groupID int64) *GroupStore {
	return &GroupStore{pool: s.pool, groupID: groupID}
}

// GroupID — чью группу обслуживает этот хендл.
func (g *GroupStore) GroupID() int64 { return g.groupID }

// Connect поднимает пул и сразу проверяет связь с базой.
func Connect(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("разбор DATABASE_URL: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("подключение к БД: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping БД: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// isNoRows — «строк нет» как ожидаемый исход, а не ошибка.
func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// Migrate накатывает миграции из embed.FS.
func Migrate(ctx context.Context, dsn string) error {
	db, err := openSQL(dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	return goose.UpContext(ctx, db, ".")
}

// MigrateDown откатывает одну миграцию — для make migrate-down.
func MigrateDown(ctx context.Context, dsn string) error {
	db, err := openSQL(dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	return goose.DownContext(ctx, db, ".")
}

// openSQL открывает database/sql-подключение поверх pgx — goose работает только
// с ним. Для обычных запросов используется пул pgx напрямую.
func openSQL(dsn string) (*sql.DB, error) {
	connCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("разбор DATABASE_URL: %w", err)
	}
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(gooseLogger{})
	if err := goose.SetDialect("postgres"); err != nil {
		return nil, err
	}
	return stdlib.OpenDB(*connCfg), nil
}

// gooseLogger уводит вывод goose в slog: логи должны быть однородными (§0).
type gooseLogger struct{}

func (gooseLogger) Printf(format string, v ...any) {
	slog.Info(strings.TrimSpace(fmt.Sprintf(format, v...)))
}

func (gooseLogger) Fatalf(format string, v ...any) {
	slog.Error(strings.TrimSpace(fmt.Sprintf(format, v...)))
	os.Exit(1)
}

// Pool отдаёт пул напрямую. Нужен только тестам: подготовить и вычистить
// данные, для которых у хранилища нет и не должно быть метода.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Pool отдаёт пул напрямую — по той же причине, что и у Store.
func (g *GroupStore) Pool() *pgxpool.Pool { return g.pool }

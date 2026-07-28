// Package migrations хранит SQL-миграции goose и раздаёт их как embed.FS,
// чтобы бинарь был самодостаточным и на VPS не нужно было тащить каталог.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS

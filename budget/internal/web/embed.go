package web

import "embed"

// dist — собранный фронт. Лежит в репозитории собранным намеренно: бинарь
// должен оставаться самодостаточным, на проде npm нет и не будет.
// Пересобирается через make web-build.
//
//go:embed dist
var dist embed.FS

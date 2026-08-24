package app

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// dist — собранный фронт. Лежит в бинаре, а не рядом файлами: деплой у нас
// «положить один файл и перезапустить», и каталог, который забыли скопировать,
// превращается в белый экран без единой ошибки в логе.
//
//go:embed all:dist
var dist embed.FS

// assets отдаёт фронт. Всё, что не файл, — это index.html: маршрутизация
// внутри приложения своя, и по прямой ссылке на экран сервер обязан вернуть
// то же самое, что по корню.
func assets() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// Каталога нет только если сборка фронта не запускалась — а это
		// ошибка сборки, а не работы.
		panic("dist не собран: " + err.Error())
	}
	files := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path != "" {
			if _, err := fs.Stat(sub, path); err == nil {
				// Файлы с хешем в имени неизменны — их можно кэшировать
				// навсегда. index.html — нельзя: он и есть точка обновления.
				if strings.HasPrefix(path, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		r.URL.Path = "/"
		files.ServeHTTP(w, r)
	})
}

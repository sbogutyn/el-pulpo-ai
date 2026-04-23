package httpserver

import (
	"embed"
	"io/fs"
	"net/http"
)

func fsSub(e embed.FS, dir string) (http.FileSystem, error) {
	sub, err := fs.Sub(e, dir)
	if err != nil {
		return nil, err
	}
	return http.FS(sub), nil
}

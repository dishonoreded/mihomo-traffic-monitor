package api

import (
	"io/fs"
	"net/http"
)

func (srv *server) singlePageApplication() http.Handler {
	fileServer := http.FileServer(http.FS(srv.assets))
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		path := request.URL.Path[1:]
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(srv.assets, path); err != nil {
			clone := request.Clone(request.Context())
			clone.URL.Path = "/"
			fileServer.ServeHTTP(response, clone)
			return
		}
		fileServer.ServeHTTP(response, request)
	})
}

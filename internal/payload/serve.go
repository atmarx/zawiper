package payload

import (
	"net/http"
	"os"
	"path/filepath"
)

// Handler serves a Library to the bays: a set's list, and the files it names,
// with range requests.  Nothing a set doesn't list can be reached, whatever
// else is in the folder.
func (l *Library) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sets/{set}", func(w http.ResponseWriter, r *http.Request) {
		s, err := l.Set(r.PathValue("set"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(s.Text())
	})
	mux.HandleFunc("GET /sets/{set}/files/{path...}", func(w http.ResponseWriter, r *http.Request) {
		s, err := l.Set(r.PathValue("set"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		name := r.PathValue("path")
		file := s.Find(name)
		if file == nil || CheckPath(name) != nil {
			http.Error(w, "that set doesn't list that file", http.StatusNotFound)
			return
		}
		f, err := os.Open(filepath.Join(l.Dir, filepath.FromSlash(s.Root), filepath.FromSlash(file.Path)))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil || !fi.Mode().IsRegular() {
			http.Error(w, "that isn't a regular file", http.StatusNotFound)
			return
		}
		// The bay holds the hash and checks it.  The type never matters.
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, "", fi.ModTime(), f)
	})
	return mux
}

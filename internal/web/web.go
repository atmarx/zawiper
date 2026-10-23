// Package web is zaweb: the page in front of the bay.  It holds no device
// and no privilege.  It asks bayd for state and reads records from a
// read-only mount.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"zawiper/internal/model"
	"zawiper/internal/proto"
	"zawiper/internal/records"
	"zawiper/internal/smart"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Bayd is the part of proto.Client the page needs.
type Bayd interface {
	Call(ctx context.Context, v proto.Verb) (*model.Snapshot, error)
}

type Server struct {
	Bayd    Bayd
	Store   *records.Store
	Hosts   []string // Host headers we answer to; empty answers any (dev only)
	Version string

	pages map[string]*template.Template

	mu         sync.Mutex
	lastReread time.Time
}

var funcs = template.FuncMap{
	"size":     records.Size,
	"when":     records.When,
	"found":    records.FoundLine,
	"bridge":   records.BridgeLine,
	"rotation": records.Rotation,
	"dir":      records.DirName,
	"commas":   func(n int) string { return records.Commas(int64(n)) },
	"deref":    func(b *bool) bool { return b != nil && *b },
	"lower":    func(s model.State) string { return strings.ToLower(string(s)) },
	"grade": func(g smart.Grade) string {
		switch g {
		case smart.Pass:
			return "pass"
		case smart.Notes:
			return "notes"
		case smart.Fail:
			return "fail"
		}
		return "none"
	},
	"tail": func(s string) string { // the last six, the part people read aloud
		if len(s) <= 6 {
			return ""
		}
		return s[:len(s)-6]
	},
	"last6": func(s string) string {
		if len(s) <= 6 {
			return s
		}
		return s[len(s)-6:]
	},
}

func (s *Server) Handler() http.Handler {
	s.pages = map[string]*template.Template{}
	for _, p := range []string{"bay", "drives", "drive", "record"} {
		s.pages[p] = template.Must(template.New("").Funcs(funcs).ParseFS(templateFS,
			"templates/layout.html", "templates/parts.html", "templates/"+p+".html"))
	}
	static, _ := fs.Sub(staticFS, "static")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.bay)
	mux.HandleFunc("GET /fragment/bay", s.bayFragment)
	mux.HandleFunc("GET /api/bay", s.apiBay)
	mux.HandleFunc("POST /api/bay/reread", s.guard(s.reread))
	mux.HandleFunc("GET /drives", s.drives)
	mux.HandleFunc("GET /drives/{serial}", s.drive)
	mux.HandleFunc("GET /drives/{serial}/{id}", s.record)
	mux.HandleFunc("GET /drives/{serial}/{id}/{file}", s.file)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	return s.hosts(headers(mux))
}

// hosts refuses any Host we don't serve.  That closes DNS rebinding: a page
// on evil.example resolving to 192.0.2.18 still says Host: evil.example.
func (s *Server) hosts(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.Hosts) > 0 && !contains(s.Hosts, r.Host) {
			http.Error(w, "wrong host", http.StatusMisdirectedRequest)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func headers(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		h.ServeHTTP(w, r)
	})
}

// guard is for anything that asks bayd to act.  A web page open in some LAN
// browser can make the browser send a POST here.  It can't set a custom
// header or a JSON content type without a CORS preflight, which this server
// never answers, and it can't fake the Origin.
func (s *Server) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		o, err := url.Parse(r.Header.Get("Origin"))
		switch {
		case err != nil || o.Host == "" || o.Host != r.Host:
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
		case !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json"):
			http.Error(w, "JSON only", http.StatusUnsupportedMediaType)
		case r.Header.Get("X-Za-Wiper") != "1":
			http.Error(w, "missing X-Za-Wiper header", http.StatusForbidden)
		default:
			h(w, r)
		}
	}
}

type bayView struct {
	Snap   *model.Snapshot
	Err    string
	Recent []records.Summary
}

func (s *Server) bayData(r *http.Request) bayView {
	v := bayView{}
	snap, err := s.Bayd.Call(r.Context(), proto.Status)
	if err != nil {
		v.Err = err.Error()
	}
	v.Snap = snap
	all, _ := s.Store.Drives()
	if len(all) > 5 {
		all = all[:5]
	}
	v.Recent = all
	return v
}

func (s *Server) bay(w http.ResponseWriter, r *http.Request) {
	s.render(w, "bay", "layout", map[string]any{"Title": "The bay", "Nav": "bay", "V": s.bayData(r), "Version": s.Version})
}

func (s *Server) bayFragment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, "bay", "baycard", s.bayData(r))
}

func (s *Server) apiBay(w http.ResponseWriter, r *http.Request) {
	snap, err := s.Bayd.Call(r.Context(), proto.Status)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, snap)
}

func (s *Server) reread(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if time.Since(s.lastReread) < 2*time.Second {
		s.mu.Unlock()
		http.Error(w, "already re-reading", http.StatusTooManyRequests)
		return
	}
	s.lastReread = time.Now()
	s.mu.Unlock()
	snap, err := s.Bayd.Call(r.Context(), proto.Reread)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, snap)
}

func (s *Server) drives(w http.ResponseWriter, r *http.Request) {
	all, err := s.Store.Drives()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	var shown []records.Summary
	for _, d := range all {
		if q == "" || strings.Contains(strings.ToLower(d.Serial+" "+d.Model+" "+d.Family), strings.ToLower(q)) {
			shown = append(shown, d)
		}
	}
	s.render(w, "drives", "layout", map[string]any{"Title": "Drives", "Nav": "drives", "Drives": shown, "Total": len(all), "Q": q, "Version": s.Version})
}

func (s *Server) drive(w http.ResponseWriter, r *http.Request) {
	serial := r.PathValue("serial")
	sum, err := s.Store.Summary(serial)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ids, _ := s.Store.Records(serial)
	var recs []*records.Record
	for _, id := range ids {
		if rec, err := s.Store.Record(serial, id); err == nil {
			recs = append(recs, rec)
		}
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Started.After(recs[j].Started) })
	s.render(w, "drive", "layout", map[string]any{"Title": sum.Serial, "Nav": "drives", "Sum": sum, "Dir": serial, "Records": recs, "Version": s.Version})
}

func (s *Server) record(w http.ResponseWriter, r *http.Request) {
	serial, id := r.PathValue("serial"), r.PathValue("id")
	rec, err := s.Store.Record(serial, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, "record", "layout", map[string]any{"Title": rec.Serial + " · " + rec.ID, "Nav": "drives", "R": rec, "Dir": serial, "Version": s.Version})
}

func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	ctype, ok := records.Files[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	b, err := os.ReadFile(s.Store.Path(r.PathValue("serial"), r.PathValue("id"), name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ctype)
	if r.URL.Query().Has("download") {
		w.Header().Set("Content-Disposition", `attachment; filename="`+r.PathValue("serial")+"-"+name+`"`)
	}
	w.Write(b)
}

func (s *Server) render(w http.ResponseWriter, page, tmpl string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.pages[page].ExecuteTemplate(w, tmpl, data); err != nil {
		log.Printf("render %s/%s: %v", page, tmpl, err)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

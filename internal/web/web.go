// Package web is zaweb: the page in front of the bays.  It holds no device
// and no privilege.  It asks the hub for state, passes commands down to the
// bays through it, and reads records from a read-only mount.
//
// One shared view (spec §4.3): every page draws only what the bays report.
// There's no per-visitor state, nothing that belongs to one viewer, and no
// control that stops a run.
package web

import (
	"archive/zip"
	"crypto/ed25519"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/atmarx/zawiper/internal/hub"
	"github.com/atmarx/zawiper/internal/model"
	"github.com/atmarx/zawiper/internal/proto"
	"github.com/atmarx/zawiper/internal/records"
	"github.com/atmarx/zawiper/internal/sign"
	"github.com/atmarx/zawiper/internal/smart"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

type Server struct {
	Fleet   hub.Fleet
	Store   *records.Store
	Hosts   []string // Host headers we answer to; empty answers any (dev only)
	Version string
	// Signer signs records as they arrive and is printed on certificates.
	// Nil means unsigned: the certificate says so.
	Signer *sign.Signer

	pages  map[string]*template.Template
	events *events

	mu         sync.Mutex
	lastReread map[string]time.Time // per bay key
}

// pillSet is the pills beside a grade: fails first, then the drive's own
// notes, then whatever isn't the drive's.  More counts what didn't fit.
type pillSet struct {
	Shown []smart.Finding
	More  int
}

// pills orders findings for display and keeps at most max of them (0: all).
func pills(fs []smart.Finding, max int) pillSet {
	rank := func(f smart.Finding) int {
		switch {
		case f.Fail:
			return 0
		case f.About == smart.Drive:
			return 1
		}
		return 2
	}
	s := slices.Clone(fs)
	slices.SortStableFunc(s, func(a, b smart.Finding) int { return rank(a) - rank(b) })
	if max > 0 && len(s) > max {
		return pillSet{s[:max], len(s) - max}
	}
	return pillSet{Shown: s}
}

// pillKind is a pill's look: red and filled for a fail, amber and outlined
// for a note against the drive, cyan and dashed for anything that isn't.
func pillKind(f smart.Finding) string {
	switch {
	case f.Fail:
		return "fail"
	case f.About == smart.Drive:
		return "drive"
	}
	return "other"
}

var funcs = template.FuncMap{
	"pillkind": pillKind,
	"size":     records.Size,
	"surface":  records.Surface,
	"legend":   records.SurfaceLegend,
	"verdict":  records.WipeVerdict,
	"pass":     records.Pass,
	"slowline": records.SlowLine,
	"when":     records.When,
	"found":    records.FoundLine,
	"bridge":   records.BridgeLine,
	"rotation": records.Rotation,
	"dir":      records.DirName,
	"cells":    Cells,
	"trace":    TraceRows,
	"phase":    PhaseWords,
	"rfc3339":  func(t time.Time) string { return t.Format(time.RFC3339Nano) },
	"commas": func(n any) string {
		switch v := n.(type) {
		case int:
			return records.Commas(int64(v))
		case int64:
			return records.Commas(v)
		}
		return fmt.Sprint(n)
	},
	"deref": func(b *bool) bool { return b != nil && *b },
	"lower": func(s model.State) string { return strings.ToLower(string(s)) },
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
	"pills": pills,
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
	for _, p := range []string{"wall", "bay", "drives", "drive", "record"} {
		s.pages[p] = template.Must(template.New("").Funcs(funcs).ParseFS(templateFS,
			"templates/layout.html", "templates/parts.html", "templates/"+p+".html"))
	}
	s.events = newEvents(s.Fleet)
	s.lastReread = map[string]time.Time{}
	static, _ := fs.Sub(staticFS, "static")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.wall)
	mux.HandleFunc("GET /fragment/wall", s.wallFragment)
	mux.HandleFunc("GET /bays/{host}/{name}", s.bay)
	mux.HandleFunc("GET /fragment/bays/{host}/{name}", s.bayFragment)
	mux.HandleFunc("GET /text", s.wallText)
	mux.HandleFunc("GET /bays/{host}/{name}/text", s.bayText)
	mux.HandleFunc("GET /events", s.events.serve)
	mux.HandleFunc("GET /api/frame", s.apiFrame)
	mux.HandleFunc("GET /api/bays", s.apiBays)
	mux.HandleFunc("GET /api/bays/{host}/{name}", s.apiBay)
	// Four verbs and no more.  There is no abort, on purpose (spec §4.3).
	for _, v := range []proto.Verb{proto.Reread, proto.Wipe, proto.Ack, proto.Eject} {
		mux.HandleFunc("POST /api/bays/{host}/{name}/"+string(v), s.guard(s.act(v)))
	}
	mux.HandleFunc("GET /drives", s.drives)
	mux.HandleFunc("GET /drives/{serial}", s.drive)
	mux.HandleFunc("GET /drives/{serial}/{id}", s.record)
	mux.HandleFunc("GET /drives/{serial}/{id}/proof.zip", s.proof)
	mux.HandleFunc("GET /drives/{serial}/{id}/{file}", s.file)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	mux.HandleFunc("GET /za-wiper.pub", s.pubkey)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	return s.hosts(headers(mux))
}

// hosts refuses any Host we don't serve.  That closes DNS rebinding: a page
// on evil.example resolving to the hub's address still says Host: evil.example.
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

// guard is for anything that asks a bay to act.  A web page open in some LAN
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

// lookup finds the bay a URL names.  The key that goes on to the hub is the
// hub's own, never the string from the URL.
func (s *Server) lookup(r *http.Request) (hub.Bay, bool) {
	host, name := r.PathValue("host"), r.PathValue("name")
	if host == "" || name == "" || strings.ContainsAny(host+name, "/\\") {
		return hub.Bay{}, false
	}
	return s.Fleet.Bay(host + "/" + name)
}

// wipeBody is everything a wipe may say: the drive and the insertion its
// confirm was drawn from.  Anything more is refused.
type wipeBody struct {
	Serial  string    `json:"serial"`
	Session time.Time `json:"session"`
}

// act passes one verb down to one bay.  The bay decides; the hub and this
// page only carry the request.
func (s *Server) act(v proto.Verb) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, ok := s.lookup(r)
		if !ok {
			http.NotFound(w, r)
			return
		}
		req := proto.Request{Verb: v}
		if v == proto.Wipe {
			var body wipeBody
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&body); err != nil || body.Serial == "" || body.Session.IsZero() {
				http.Error(w, "A wipe names the drive and the insertion its confirm was drawn from, and nothing else.", http.StatusBadRequest)
				return
			}
			req.Serial, req.Session = body.Serial, body.Session
		}
		if !b.Online {
			http.Error(w, "This bay's link is down.", http.StatusConflict)
			return
		}
		if v == proto.Reread {
			s.mu.Lock()
			if time.Since(s.lastReread[b.Key]) < 2*time.Second {
				s.mu.Unlock()
				http.Error(w, "already re-reading", http.StatusTooManyRequests)
				return
			}
			s.lastReread[b.Key] = time.Now()
			s.mu.Unlock()
		}
		snap, err := s.Fleet.Call(r.Context(), b.Key, req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, snap)
	}
}

func (s *Server) recent(n int) []records.Summary {
	if s.Store == nil {
		return nil
	}
	all, _ := s.Store.Drives()
	if len(all) > n {
		all = all[:n]
	}
	return all
}

func (s *Server) wall(w http.ResponseWriter, r *http.Request) {
	s.render(w, "wall", "layout", map[string]any{
		"Title": "The wall", "Nav": "wall", "W": wallOf(s.Fleet.Bays(), time.Now()),
		"Recent": s.recent(5), "Version": s.Version,
	})
}

func (s *Server) wallFragment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, "wall", "tiles", wallOf(s.Fleet.Bays(), time.Now()))
}

func (s *Server) bay(w http.ResponseWriter, r *http.Request) {
	b, ok := s.lookup(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	v := viewOf(b, time.Now())
	v.Past = s.past(b)
	var recent []records.Summary
	if v.State == model.Empty {
		recent = s.recent(5)
	}
	s.render(w, "bay", "layout", map[string]any{
		"Title": b.Key, "Nav": "wall", "V": v, "Recent": recent, "Version": s.Version,
	})
}

func (s *Server) bayFragment(w http.ResponseWriter, r *http.Request) {
	b, ok := s.lookup(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	v := viewOf(b, time.Now())
	v.Past = s.past(b)
	s.render(w, "bay", "baybody", v)
}

// apiFrame is the poll fallback: the same frame /events sends.
func (s *Server) apiFrame(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(buildFrame(s.Fleet))
}

func (s *Server) apiBays(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Fleet.Bays())
}

func (s *Server) apiBay(w http.ResponseWriter, r *http.Request) {
	b, ok := s.lookup(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, b)
}

// A host's bays, for the wall.
type hostGroup struct {
	Host  string
	Tiles []bayView
}

func wallOf(bays []hub.Bay, now time.Time) []hostGroup {
	var out []hostGroup
	for _, b := range bays {
		if len(out) == 0 || out[len(out)-1].Host != b.Host {
			out = append(out, hostGroup{Host: b.Host})
		}
		g := &out[len(out)-1]
		g.Tiles = append(g.Tiles, viewOf(b, now))
	}
	return out
}

// bayView is one bay, ready to draw.
type bayView struct {
	hub.Bay
	State  model.State // Offline when the link is down, whatever Snap says
	Sig    string
	Live   *Live // the surface map and its numbers, when there's a job
	Path   string
	API    string
	Wipe   bool   // the Wipe button is live
	WhyNot string // and if not, why
	// Skew: the disk side and the link are different builds, as a bay is
	// after an install that left a running bayd@ alone.
	Skew bool
	// Past is what the hub already knows about the drive in the bay.
	Past *pastView
}

// pastView is a drive's history, for a bay page: has it been through
// before, and was it wiped?
type pastView struct {
	Serial   string
	Earlier  int             // records before this read's own
	LastWipe *records.Record // the newest wipe, whether or not it finished
}

// past looks the bay's drive up in the records.  Nil when there's no drive,
// or it has never been through any bay before this read.
func (s *Server) past(b hub.Bay) *pastView {
	d := b.Snap.Drive
	if d == nil || d.Serial() == "" || s.Store == nil {
		return nil
	}
	ids, _ := s.Store.Records(d.Serial())
	p := &pastView{Serial: d.Serial()}
	for _, id := range ids {
		if id == b.Snap.RecordID {
			continue
		}
		p.Earlier++
		if p.LastWipe == nil && strings.HasSuffix(id, "-wipe") {
			p.LastWipe, _ = s.Store.Record(p.Serial, id)
		}
	}
	if p.Earlier == 0 {
		return nil
	}
	return p
}

func viewOf(b hub.Bay, now time.Time) bayView {
	v := bayView{Bay: b, State: stateOf(b), Sig: Sig(b)}
	v.Path = "/bays/" + url.PathEscape(b.Host) + "/" + url.PathEscape(b.Name)
	v.API = "/api" + v.Path
	if j := b.Snap.Job; j != nil {
		v.Live = LiveOf(j, b.Snap.Drive, now)
		// Once it's over, the bottom line says how it ended, not what it was doing.
		switch v.State {
		case model.Done:
			v.Live.Phase = "Done.  Every block written, read back, and compared."
		case model.Failed:
			v.Live.Phase = "Failed during: " + PhaseWords(j.Phase)
		case model.Interrupted:
			v.Live.Phase = "Interrupted during: " + PhaseWords(j.Phase)
		case model.Alarm:
			v.Live.Phase = "Stopped by bayd during: " + PhaseWords(j.Phase)
		}
	}
	v.Wipe, v.WhyNot = wipeable(b)
	v.Skew = b.Online && b.Snap.Software != "" && strings.TrimPrefix(b.Snap.Software, "bayd ") != b.Version
	return v
}

func stateOf(b hub.Bay) model.State {
	if !b.Online {
		return model.Offline
	}
	return b.Snap.State
}

// wipeable says whether the Wipe button should be live.  It's only the
// page's opinion: bayd checks everything again when the wipe arrives.
func wipeable(b hub.Bay) (bool, string) {
	s := b.Snap
	switch {
	case !b.Online:
		return false, "This bay's link is down."
	case s.State != model.Present:
		return false, "There's no drive ready."
	case len(s.Refusals) > 0:
		return false, "Za Wiper won't act on this drive."
	case s.Drive == nil || s.Drive.Serial() == "":
		return false, "The drive didn't give a serial to confirm against."
	case s.Session.IsZero():
		return false, "The bay didn't say when this drive went in."
	case !slices.Contains(b.Caps, "wipe"):
		return false, "This bay isn't set up to wipe."
	}
	return true, ""
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
	data := map[string]any{"Title": rec.Serial + " · " + rec.ID, "Nav": "drives", "R": rec, "Dir": serial, "Version": s.Version}
	if c := records.NewCertificate(rec, s.Signer.Seal(s.Store, serial, id)); c != nil {
		data["Cert"] = c
	}
	s.render(w, "record", "layout", data)
}

// pubkey is the key certificates are signed with, as one authorized_keys
// line: what `ssh-keygen -Y verify` wants in its allowed-signers file.
func (s *Server) pubkey(w http.ResponseWriter, r *http.Request) {
	if s.Signer == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(sign.AuthorizedKey(s.Signer.Key.Public().(ed25519.PublicKey)) + "\n"))
}

// proof is everything a buyer needs to check a record without reaching this
// hub: the record's files, its signature, and the key it was signed with.
// Records stay home; this is what gets sent when someone asks.
func (s *Server) proof(w http.ResponseWriter, r *http.Request) {
	serial, id := r.PathValue("serial"), r.PathValue("id")
	if _, err := s.Store.Record(serial, id); err != nil {
		http.NotFound(w, r)
		return
	}
	names := make([]string, 0, len(records.Files))
	for name := range records.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	dir := serial + "-" + id + "/"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+serial+"-"+id+`.zip"`)
	z := zip.NewWriter(w)
	add := func(name string, b []byte) {
		if f, err := z.Create(dir + name); err == nil {
			f.Write(b)
		}
	}
	for _, name := range names {
		if b, err := os.ReadFile(s.Store.Path(serial, id, name)); err == nil {
			add(name, b)
		}
	}
	if s.Signer != nil {
		add("za-wiper.pub", []byte(sign.AuthorizedKey(s.Signer.Key.Public().(ed25519.PublicKey))+"\n"))
	}
	if err := z.Close(); err != nil {
		log.Printf("proof %s/%s: %v", serial, id, err)
	}
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

// zaweb serves the Za Wiper page.  Unprivileged: it reaches the drive only
// by asking bayd, and reads records from a read-only mount.
package main

import (
	"flag"
	"log"
	"net/http"
	"strings"
	"time"
	_ "time/tzdata" // the container has no zoneinfo; TZ still works

	"zawiper/internal/proto"
	"zawiper/internal/records"
	"zawiper/internal/web"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	listen := flag.String("listen", ":8460", "address to serve on")
	socket := flag.String("socket", "/run/zawiper/bayd.sock", "bayd's socket")
	root := flag.String("records", "/srv/zawiper", "where records live (read-only is fine)")
	hosts := flag.String("hosts", "zawiper.example.org", "comma-separated Host headers to answer; empty answers any (dev only)")
	flag.Parse()

	s := &web.Server{
		Bayd:    &proto.Client{Socket: *socket},
		Store:   &records.Store{Root: *root},
		Version: "zaweb " + version + " (" + commit + ")",
	}
	for _, h := range strings.Split(*hosts, ",") {
		if h = strings.TrimSpace(h); h != "" {
			s.Hosts = append(s.Hosts, h)
		}
	}
	srv := &http.Server{
		Addr:              *listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("%s on %s, hosts %v", s.Version, *listen, s.Hosts)
	log.Fatal(srv.ListenAndServe())
}

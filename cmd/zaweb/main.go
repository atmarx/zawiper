// zaweb is the Za Wiper hub's page.  Unprivileged: it reaches drives only by
// asking the bays, through the hub, and reads records from a read-only mount.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	_ "time/tzdata" // the container has no zoneinfo; TZ still works

	"zawiper/internal/hub"
	"zawiper/internal/proto"
	"zawiper/internal/records"
	"zawiper/internal/web"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	listen := flag.String("listen", ":8460", "address to serve the page on")
	root := flag.String("records", "/srv/zawiper", "where records live (read-only is fine)")
	hosts := flag.String("hosts", "zawiper.example.org", "comma-separated Host headers to answer; empty answers any (dev only)")
	demo := flag.Bool("demo", false, "serve a pretend fleet of four bays, for designing the page without hardware")
	linkAddr := flag.String("link-addr", fmt.Sprintf(":%d", proto.LinkPort), "address the bays dial in to (TLS)")
	tlsCA := flag.String("tls-ca", "/etc/zawiper/ca.pem", "the CA that signs bay certificates")
	tlsCert := flag.String("tls-cert", "/etc/zawiper/hub.pem", "the hub's certificate for the link")
	tlsKey := flag.String("tls-key", "/etc/zawiper/hub-key.pem", "the hub's key for the link")
	flag.Parse()

	var fleet hub.Fleet
	if *demo {
		fleet = web.NewDemoFleet()
		if *root == "/srv/zawiper" {
			if _, err := os.Stat(*root); err != nil {
				*root = os.TempDir() + "/zawiper-demo"
			}
		}
		log.Printf("demo fleet: nothing here touches a drive")
	} else {
		// TODO(phase 2 merge): the real hub.  Something like
		//   h, err := hub.New(hub.Config{Addr: *linkAddr, CA: *tlsCA, Cert: *tlsCert, Key: *tlsKey, Store: store})
		//   go h.Serve(ctx)
		//   fleet = h
		// Until that lands, there's no fleet to serve.
		log.Fatalf("zaweb: the hub isn't wired in yet (link %s, CA %s, cert %s, key %s); run with -demo",
			*linkAddr, *tlsCA, *tlsCert, *tlsKey)
	}

	s := &web.Server{
		Fleet:   fleet,
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

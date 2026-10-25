// zaweb is the Za Wiper hub: the page, the bays' links, and the records they
// send.  Unprivileged, with no devices: it reaches a drive only by asking the
// bay that holds it.
package main

import (
	"context"
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
	root := flag.String("records", "/srv/zawiper", "where records live; the hub files them as bays send them")
	hosts := flag.String("hosts", "zawiper.example.org", "comma-separated Host headers to answer; empty answers any (dev only)")
	demo := flag.Bool("demo", false, "serve a pretend fleet of four bays, for designing the page without hardware")
	linkAddr := flag.String("link-addr", fmt.Sprintf(":%d", proto.LinkPort), "address the bays dial in to (TLS)")
	tlsCA := flag.String("tls-ca", "/etc/zawiper/tls/ca.crt", "the CA that signs bay certificates")
	tlsCert := flag.String("tls-cert", "/etc/zawiper/tls/hub.crt", "the hub's certificate for the link")
	tlsKey := flag.String("tls-key", "/etc/zawiper/tls/hub.key", "the hub's key for the link")
	flag.Parse()

	var fleet hub.Fleet
	store := &records.Store{Root: *root}
	if *demo {
		fleet = web.NewDemoFleet()
		if *root == "/srv/zawiper" {
			if _, err := os.Stat(*root); err != nil {
				store.Root = os.TempDir() + "/zawiper-demo"
			}
		}
		log.Printf("demo fleet: nothing here touches a drive")
	} else {
		h, err := hub.New(hub.Config{Addr: *linkAddr, CAFile: *tlsCA, CertFile: *tlsCert, KeyFile: *tlsKey, Store: store})
		if err != nil {
			log.Fatalf("hub: %v", err)
		}
		go func() { log.Fatalf("hub: %v", h.Run(context.Background())) }()
		log.Printf("bays dial in on %s", *linkAddr)
		fleet = h
	}

	s := &web.Server{
		Fleet:   fleet,
		Store:   store,
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

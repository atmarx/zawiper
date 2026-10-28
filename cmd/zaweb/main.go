// zaweb is the Za Wiper hub: the page, the bays' links, and the records they
// send.  Unprivileged, with no devices: it reaches a drive only by asking the
// bay that holds it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata" // the container has no zoneinfo; TZ still works

	"zawiper/internal/hub"
	"zawiper/internal/proto"
	"zawiper/internal/records"
	"zawiper/internal/sign"
	"zawiper/internal/web"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	listen := flag.String("listen", ":8460", "address to serve the page on")
	root := flag.String("records", "/srv/zawiper", "where records live; the hub files them as bays send them")
	hosts := flag.String("hosts", "", "comma-separated Host headers to answer: the name browsers use for the page, e.g. zawiper.example.org (required, except with -demo)")
	demo := flag.Bool("demo", false, "serve a pretend fleet of four bays, for designing the page without hardware")
	linkAddr := flag.String("link-addr", fmt.Sprintf(":%d", proto.LinkPort), "address the bays dial in to (TLS)")
	tlsCA := flag.String("tls-ca", "/etc/zawiper/tls/ca.crt", "the CA that signs bay certificates")
	tlsCert := flag.String("tls-cert", "/etc/zawiper/tls/hub.crt", "the hub's certificate for the link")
	tlsKey := flag.String("tls-key", "/etc/zawiper/tls/hub.key", "the hub's key for the link")
	signKey := flag.String("sign-key", "/srv/zawiper/keys/za-wiper", "the key records are signed with; made on first start if it's missing")
	site := flag.String("site-url", "", "a public address for the hub, printed on certificates with a QR code; empty keeps records home, and buyers get proof.zip on request")
	flag.Parse()

	var fleet hub.Fleet
	var signer *sign.Signer
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
		var err error
		if signer, err = loadSigner(*signKey, *site); err != nil {
			log.Fatalf("signing key: %v", err)
		}
		h, err := hub.New(hub.Config{Addr: *linkAddr, CAFile: *tlsCA, CertFile: *tlsCert, KeyFile: *tlsKey, Store: store,
			OnStored: func(serial, id string) {
				if err := signer.SignRecord(store, serial, id); err != nil {
					log.Printf("sign %s/%s: %v", serial, id, err)
				}
			}})
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
		Signer:  signer,
	}
	for _, h := range strings.Split(*hosts, ",") {
		if h = strings.TrimSpace(h); h != "" {
			s.Hosts = append(s.Hosts, h)
		}
	}
	// A page that answers any Host can be reached by DNS rebinding, so only
	// the demo, which touches no drive, may.
	if len(s.Hosts) == 0 && !*demo {
		log.Fatal("-hosts: name the host browsers use for the page, e.g. -hosts zawiper.example.org")
	}
	srv := &http.Server{
		Addr:              *listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("%s on %s, hosts %v", s.Version, *listen, s.Hosts)
	log.Fatal(srv.ListenAndServe())
}

// loadSigner reads the signing key, or makes one the first time.  The key
// lives with the records, so it's in the same backup.
func loadSigner(path, site string) (*sign.Signer, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		pub, err := sign.GenerateKey(path)
		if err != nil {
			return nil, err
		}
		log.Printf("made a new signing key at %s: %s", path, sign.AuthorizedKey(pub))
	}
	key, err := sign.LoadKey(path)
	if err != nil {
		return nil, err
	}
	s := &sign.Signer{Key: key, SiteURL: strings.TrimSuffix(site, "/")}
	if s.SiteURL != "" {
		s.KeyURL = s.SiteURL + "/za-wiper.pub"
	}
	return s, nil
}

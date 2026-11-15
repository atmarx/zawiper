// bayd-link is a bay's network side, one per bay (bayd-link@<bay>).  It dials
// out to the hub over TLS, reports what the disk side says, passes the hub's
// commands down to it, and uploads finished records.  It has no device
// access: the hub reaches a disk only through the disk side's checks.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/atmarx/zawiper/internal/config"
	"github.com/atmarx/zawiper/internal/link"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	bay := flag.String("bay", "", "this bay's name, as in its certificate (bay/<host>/<name>)")
	hub := flag.String("hub", "", "the hub, host:8461")
	ca := flag.String("ca", "/etc/zawiper/tls/ca.crt", "the CA the hub's certificate chains to")
	cert := flag.String("cert", "", "this bay's certificate (default /etc/zawiper/tls/<bay>.crt)")
	key := flag.String("key", "", "this bay's key (default /etc/zawiper/tls/<bay>.key)")
	socket := flag.String("socket", "", "the disk side's socket (default /run/zawiper/<bay>/bayd.sock)")
	state := flag.String("state", "", "the job state file (default /var/lib/zawiper/<bay>/job.json)")
	spool := flag.String("spool", "", "finished records waiting for the hub (default /var/lib/zawiper/<bay>/spool)")
	caps := flag.String("caps", "", "what this bay can do, comma-separated: wipe, load, sas (default the caps in the bay's config, as bayd reads them)")
	payloads := flag.String("payload-socket", "", "where the disk side asks for payload bytes (default /run/zawiper-link/<bay>/payload.sock)")
	payloadHub := flag.String("payload-hub", "", "the hub's payload port (default the hub's host, port 8462)")
	flag.Parse()

	if *bay == "" || *hub == "" {
		log.Fatal("bayd-link needs -bay and -hub")
	}
	orDefault(cert, "/etc/zawiper/tls/%s.crt", *bay)
	orDefault(key, "/etc/zawiper/tls/%s.key", *bay)
	orDefault(socket, "/run/zawiper/%s/bayd.sock", *bay)
	orDefault(state, "/var/lib/zawiper/%s/job.json", *bay)
	orDefault(spool, "/var/lib/zawiper/%s/spool", *bay)
	orDefault(payloads, "/run/zawiper-link/%s/payload.sock", *bay)
	capList := splitCaps(*caps)
	if *caps == "" {
		// The same file bayd reads, so the hub offers what the disk side
		// will actually do.  A bay with no caps there only reads.
		cfg, err := config.LoadBay(config.BayPath(*bay))
		if err != nil {
			log.Fatalf("config: %v (or say -caps)", err)
		}
		capList = cfg.Caps
	}
	if *payloadHub == "" {
		h, _, err := net.SplitHostPort(*hub)
		if err != nil {
			log.Fatalf("-hub %q: %v", *hub, err)
		}
		*payloadHub = net.JoinHostPort(h, "8462")
	}

	tc, cn, err := link.ClientTLS(*ca, *cert, *key)
	if err != nil {
		log.Fatal(err)
	}
	host, name, err := link.ParseCN(cn)
	if err != nil {
		log.Fatal(err)
	}
	if name != *bay {
		log.Fatalf("%s is the certificate for bay %q, not %q", *cert, name, *bay)
	}

	l := &link.Link{
		Bay: name, Host: host, Hub: *hub, TLS: tc,
		Socket: *socket, State: *state, Spool: *spool,
		Caps: capList, Version: fmt.Sprintf("%s (%s)", version, commit),
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("bayd-link %s for bay/%s/%s, hub %s", version, host, name, *hub)
	log.Printf("caps: %s", strings.Join(capList, ","))
	if slices.Contains(l.Caps, "load") {
		p := &link.PayloadProxy{Hub: *payloadHub, TLS: tc, Socket: *payloads}
		go func() {
			if err := p.Serve(ctx); err != nil {
				log.Printf("payload socket: %v", err)
			}
		}()
	}
	if err := l.Run(ctx); err != nil {
		log.Fatal(err)
	}
}

func orDefault(p *string, format, bay string) {
	if *p == "" {
		*p = fmt.Sprintf(format, bay)
	}
}

func splitCaps(s string) []string {
	var out []string
	for _, c := range strings.Split(s, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

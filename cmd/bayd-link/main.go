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
	"os/signal"
	"strings"
	"syscall"

	"zawiper/internal/link"
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
	caps := flag.String("caps", "wipe", "what this bay can do, comma-separated: wipe, load, sas")
	flag.Parse()

	if *bay == "" || *hub == "" {
		log.Fatal("bayd-link needs -bay and -hub")
	}
	orDefault(cert, "/etc/zawiper/tls/%s.crt", *bay)
	orDefault(key, "/etc/zawiper/tls/%s.key", *bay)
	orDefault(socket, "/run/zawiper/%s/bayd.sock", *bay)
	orDefault(state, "/var/lib/zawiper/%s/job.json", *bay)
	orDefault(spool, "/var/lib/zawiper/%s/spool", *bay)

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
		Caps: splitCaps(*caps), Version: fmt.Sprintf("%s (%s)", version, commit),
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("bayd-link %s for bay/%s/%s, hub %s", version, host, name, *hub)
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

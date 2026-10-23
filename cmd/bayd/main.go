// bayd watches the Za Wiper bay.  It runs as root, owns the drive, and
// answers zaweb over a unix socket.  Phase 1: it reads; nothing writes.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"zawiper/internal/bay"
	"zawiper/internal/model"
	"zawiper/internal/proto"
	"zawiper/internal/records"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "ports" {
		ports()
		return
	}
	cfgPath := flag.String("config", "/etc/zawiper/bay.toml", "bay config: the pinned USB port")
	socket := flag.String("socket", "/run/zawiper/bayd.sock", "unix socket for zaweb")
	gid := flag.Int("socket-gid", -1, "group that may use the socket (zaweb's)")
	root := flag.String("records", "/srv/zawiper", "where records live")
	devBay := flag.String("dev-bay", "", "dev mode: a file that stands in for the drive")
	devSmart := flag.String("dev-smart", "", "dev mode: smartctl -j output to report")
	flag.Parse()

	var src bay.Source
	name := "dev bay"
	if *devBay != "" {
		src = &bay.Dev{File: *devBay, Smart: *devSmart}
	} else {
		cfg, err := readConfig(*cfgPath)
		if err != nil {
			log.Fatalf("config: %v (run `bayd ports` to find the bay's port)", err)
		}
		name = cfg["name"]
		src = &bay.Sys{Port: cfg["port"], SmartType: cfg["smart_type"]}
	}

	host, _ := os.Hostname()
	store := &records.Store{Root: *root}
	software := fmt.Sprintf("bayd %s (%s)", version, commit)
	b := &bay.Bay{Name: name, Src: src, OnRead: func(s *model.Snapshot) (string, error) {
		r := &records.Record{
			Serial: s.Drive.Serial(), Started: s.Session, Finished: s.ReadAt,
			Host: host, Bay: name, Software: software,
			Drive: *s.Drive, Health: s.Drive.Health, Refusals: s.Refusals,
		}
		err := store.SaveInspect(r, s.SmartJSON, s.SmartText)
		return r.ID, err
	}}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := os.MkdirAll(filepath.Dir(*socket), 0o755); err != nil {
		log.Fatal(err)
	}
	go b.Run(ctx)
	log.Printf("%s watching %q, socket %s, records %s", software, name, *socket, *root)
	if err := proto.Serve(ctx, *socket, *gid, b); err != nil {
		log.Fatal(err)
	}
}

// readConfig reads the flat `key = "value"` lines bay.toml needs.  The file
// is root-owned and written once, at provisioning.
func readConfig(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected key = \"value\"", path, n)
		}
		v = strings.TrimSpace(v)
		if uq, err := strconv.Unquote(v); err == nil {
			v = uq
		}
		cfg[strings.TrimSpace(k)] = v
	}
	if cfg["port"] == "" {
		return nil, fmt.Errorf("%s: no port", path)
	}
	if cfg["name"] == "" {
		cfg["name"] = "the bay"
	}
	return cfg, sc.Err()
}

// ports lists USB block devices and the port each sits on, to help whoever
// provisions the bay write bay.toml.
func ports() {
	ents, _ := os.ReadDir("/sys/block")
	found := false
	for _, e := range ents {
		real, err := filepath.EvalSymlinks("/sys/block/" + e.Name())
		if err != nil {
			continue
		}
		segs := strings.Split(real, "/")
		port := ""
		for i, s := range segs {
			if strings.HasPrefix(s, "usb") {
				for _, t := range segs[i+1:] {
					if strings.Contains(t, ":") {
						break
					}
					port = t
				}
				break
			}
		}
		if port == "" {
			continue
		}
		found = true
		model, _ := os.ReadFile("/sys/block/" + e.Name() + "/device/model")
		fmt.Printf("/dev/%-6s port %-8s %s\n", e.Name(), port, strings.TrimSpace(string(model)))
	}
	if !found {
		fmt.Println("No USB block devices.  Is the caddy passed through, with a drive in it?")
		return
	}
	fmt.Println("\nWrite /etc/zawiper/bay.toml with the bay's port, e.g.:\n  name = \"the bay\"\n  port = \"10-1\"\n  smart_type = \"sat\"")
}

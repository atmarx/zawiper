// bayd is a Za Wiper bay's disk side.  It runs as root, one per bay, owns the
// drive, reads it, wipes it when its link asks with a matching confirm, and
// answers over a unix socket.  Records go into the bay's spool, which the
// link ships to the hub.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"zawiper/internal/bay"
	"zawiper/internal/config"
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
	if len(os.Args) > 1 && os.Args[1] == "browse" {
		browse(os.Args[2:])
		return
	}
	name := flag.String("bay", "", "the bay's name; picks its config, socket, state file and spool")
	cfgPath := flag.String("config", "", "bay config: the pinned port (default /etc/zawiper/bays/<bay>.toml)")
	socket := flag.String("socket", "", "unix socket for the link (default /run/zawiper/<bay>/bayd.sock)")
	gid := flag.Int("socket-gid", -1, "group that may use the socket (the link's)")
	statePath := flag.String("state", "", "the job's state file (default /var/lib/zawiper/<bay>/job.json)")
	spool := flag.String("records", "", "the record spool the link ships from (default /var/lib/zawiper/<bay>/spool)")
	devBay := flag.String("dev-bay", "", "dev mode: a file that stands in for the drive, wiped for real")
	devSmart := flag.String("dev-smart", "", "dev mode: smartctl -j output to report")
	devBlock := flag.Int64("dev-block", 1<<20, "dev mode: bytes per surface-map block, small for speed")
	flag.Parse()

	dev := *devBay != ""
	if *name == "" && dev {
		*name = "dev"
	}
	if *name == "" {
		log.Fatal("bayd -bay <name>: say which bay.  Its config is /etc/zawiper/bays/<name>.toml.")
	}
	if !records.Valid(*name) {
		log.Fatalf("%q can't name a bay: letters, digits, dots, dashes and underscores only.", *name)
	}
	// Dev mode keeps everything beside the file, so it runs without root.
	run, lib := "/run/zawiper/"+*name, "/var/lib/zawiper/"+*name
	if dev {
		abs, err := filepath.Abs(*devBay)
		if err != nil {
			log.Fatal(err)
		}
		run = filepath.Join(filepath.Dir(abs), "zawiper-"+*name)
		lib = run
	}
	or(cfgPath, config.BayPath(*name))
	or(socket, filepath.Join(run, "bayd.sock"))
	or(statePath, filepath.Join(lib, "job.json"))
	or(spool, filepath.Join(lib, "spool"))

	var src bay.Source
	var shares []string
	readOnly := false
	if dev {
		if *devBlock <= 0 || *devBlock%512 != 0 {
			log.Fatalf("-dev-block %d: want a positive whole number of 512-byte sectors.", *devBlock)
		}
		src = &bay.Dev{File: *devBay, Smart: *devSmart}
	} else {
		cfg, err := config.LoadBay(*cfgPath)
		if err != nil {
			log.Fatalf("config: %v (run `bayd ports` to find the bay's port)", err)
		}
		if cfg.Name != *name {
			log.Fatalf("%s names bay %q, not %q.", *cfgPath, cfg.Name, *name)
		}
		sys, err := bay.NewSys(cfg)
		if err != nil {
			log.Fatalf("config: %v", err)
		}
		src, readOnly = sys, !cfg.Can("wipe")
		shares = config.Siblings(filepath.Dir(*cfgPath), cfg)
	}

	host, _ := os.Hostname()
	software := fmt.Sprintf("bayd %s (%s)", version, commit)
	sp := &bay.Spool{Root: *spool}
	b := &bay.Bay{Name: *name, Src: src, StatePath: *statePath, Spool: sp,
		Host: host, Software: software, Direct: !dev, ReadOnly: readOnly, Shares: shares}
	if dev {
		b.Block = *devBlock
	}
	b.OnRead = func(s *model.Snapshot) (string, error) {
		r := &records.Record{
			Serial: s.Drive.Serial(), Started: s.Session, Finished: s.ReadAt,
			Host: host, Bay: *name, Software: software,
			Drive: *s.Drive, Health: s.Drive.Health, Refusals: s.Refusals,
		}
		err := sp.SaveInspect(r, s.SmartJSON, s.SmartText)
		return r.ID, err
	}

	for _, d := range []string{filepath.Dir(*socket), filepath.Dir(*statePath), *spool} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			log.Fatal(err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go b.Run(ctx)
	log.Printf("%s watching bay %q, socket %s, state %s, spool %s", software, *name, *socket, *statePath, *spool)
	err := proto.Serve(ctx, *socket, *gid, b)
	// A running job stops at its next buffer and stays RUNNING in the state
	// file: the next start reports it INTERRUPTED, like a power cut.
	b.Wait()
	if err != nil {
		log.Fatal(err)
	}
}

func or(p *string, def string) {
	if *p == "" {
		*p = def
	}
}

// ports lists USB block devices and the port each sits on, to help whoever
// provisions the bay write its config.
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
		// device is the SCSI device, "8:0:0:1": a dock with two slots shows
		// them as LUNs 0 and 1 of one USB device.
		lun := "?"
		if d, err := filepath.EvalSymlinks("/sys/block/" + e.Name() + "/device"); err == nil {
			if f := strings.Split(filepath.Base(d), ":"); len(f) == 4 {
				lun = f[3]
			}
		}
		fmt.Printf("/dev/%-6s port %-8s lun %-3s %s\n", e.Name(), port, lun, strings.TrimSpace(string(model)))
	}
	if !found {
		fmt.Println("No USB block devices.  Is the caddy passed through, with a drive in it?")
		return
	}
	fmt.Println("\nWrite /etc/zawiper/bays/<bay>.toml with the bay's port, e.g.:\n  name = \"bay1\"\n  usb_port = \"10-1\"\n  caps = \"wipe\"\nIf one port shows more than one LUN, the dock has a slot per LUN: write a bay\nfor each, with usb_lun = \"0\", usb_lun = \"1\" and so on.  Every slot on a dock\ngoes when the dock resets, so swap drives only when all its bays are idle.")
}

// browse mounts a bay's filesystems read-only for a person to look through
// before a wipe, or with -u takes them down again.
//
//	bayd browse bay1      mount under /mnt/zawiper/bay1
//	bayd browse -u bay1   unmount
func browse(args []string) {
	fs := flag.NewFlagSet("browse", flag.ExitOnError)
	un := fs.Bool("u", false, "unmount what browse mounted")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: bayd browse [-u] <bay>")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	name := fs.Arg(0)
	if err := config.ValidName(name); err != nil {
		log.Fatal(err)
	}
	if os.Geteuid() != 0 {
		log.Fatal("browse mounts and unmounts filesystems: run it as root")
	}
	dir := filepath.Join(bay.BrowseRoot, name)
	if *un {
		done, err := bay.Unbrowse(dir)
		for _, mp := range done {
			fmt.Println("unmounted", mp)
		}
		if err != nil {
			log.Fatal(err)
		}
		if len(done) == 0 {
			fmt.Println("Nothing was mounted under", dir)
		}
		fmt.Println("The bay can wipe again.  The wall's confirm checks for mounts when it starts.")
		return
	}
	cfg, err := config.LoadBay(config.BayPath(name))
	if err != nil {
		log.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join("/var/lib/zawiper", name, "job.json")); err == nil {
		var jf model.JobFile
		if json.Unmarshal(b, &jf) == nil && jf.State == model.Running {
			log.Fatalf("bay %s is wiping.  Nothing to browse.", name)
		}
	}
	sys, err := bay.NewSys(cfg)
	if err != nil {
		log.Fatal(err)
	}
	lines, err := sys.Browse(context.Background(), dir)
	for _, l := range lines {
		fmt.Println(" ", l)
	}
	if err != nil {
		log.Fatal(err)
	}
	host, _ := os.Hostname()
	fmt.Printf("\nRead-only, noexec, nosuid, nodev.  From elsewhere: sftp://root@%s%s\n", strings.ToLower(host), dir)
	fmt.Printf("The bay won't wipe while these are mounted.  When you're done:  bayd browse -u %s\n", name)
}

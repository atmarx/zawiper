// zwipe wipes the drive in the bay, by hand, until bayd learns to.  It runs
// the same read bayd does and refuses on the same grounds, then wants the
// drive's serial typed on the command line: that's the confirmation.
//
//	systemctl stop bayd
//	systemd-run --unit=zwipe /usr/local/bin/zwipe -serial 9VPAX9NA
//	journalctl -fu zwipe
//	systemctl start bayd
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"zawiper/internal/bay"
	"zawiper/internal/config"
	"zawiper/internal/model"
	"zawiper/internal/records"
	"zawiper/internal/smart"
	"zawiper/internal/wipe"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	cfgPath := flag.String("config", "/etc/zawiper/bay.toml", "bay config: the pinned USB port")
	root := flag.String("records", "/srv/zawiper", "where records live")
	socket := flag.String("socket", "/run/zawiper/bayd.sock", "bayd's socket, which must be quiet")
	serial := flag.String("serial", "", "the serial of the drive to wipe, as the drive reports it")
	flag.Parse()
	log.SetFlags(0)

	if *serial == "" {
		log.Fatal("zwipe -serial <SERIAL>: say which drive.  The serial is on the drive's label and on the bay page.")
	}
	// bayd re-reading mid-wipe would set the drive read-only under us.
	if c, err := net.Dial("unix", *socket); err == nil {
		c.Close()
		log.Fatal("bayd is watching the bay.  `systemctl stop bayd` first, and start it again after.")
	}
	cfg, err := config.Read(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	src := &bay.Sys{Port: cfg["port"], SmartType: cfg["smart_type"]}
	devs, err := src.Devices()
	if err != nil || len(devs) == 0 {
		log.Fatalf("Nothing in the bay (%v).", err)
	}
	session := time.Now().UTC()
	snap := bay.Read(ctx, src, devs)
	if snap.State != model.Present || snap.Drive == nil {
		log.Fatalf("The bay isn't ready: %s %s", snap.State, snap.Message)
	}
	if len(snap.Refusals) > 0 {
		for _, r := range snap.Refusals {
			log.Print(r)
		}
		log.Fatal("Refused.")
	}
	d := snap.Drive
	if d.Serial() != *serial {
		log.Fatalf("The bay holds %s, not %s.  Not wiping.", d.Serial(), *serial)
	}
	before := d.SMART
	log.Printf("%s %s, %s, %s on %s", d.SMART.Model, d.Serial(), records.Size(d.Size), d.Health.Grade, d.Dev)
	log.Printf("Found: %s", records.FoundLine(d))

	dev := devs[0]
	if err := src.SetRW(dev); err != nil {
		log.Fatal(err)
	}
	t0 := time.Now()
	job := &wipe.Job{Path: d.Dev, Size: d.Size, Direct: true,
		OnCell: func(pass string, i, n int, c wipe.Cell) {
			s := c.Write
			if pass == "verify" {
				s = c.Read
			}
			left := time.Duration(float64(time.Since(t0)) / float64(progress(pass, i, n)) * (1 - progress(pass, i, n)))
			mark := ""
			if c.Bad > 0 || c.Err != "" {
				mark = fmt.Sprintf("  BAD %d bytes %s", c.Bad, c.Err)
			}
			log.Printf("%-6s %4d/%d  %5.1fs  %4.0f MB/s  %3.0f%%  ETA %s%s", pass, i+1, n, s,
				float64(wipe.Block)/s/1e6, 100*progress(pass, i, n), left.Round(time.Minute), mark)
		}}
	res, err := job.Run(ctx)
	if err != nil {
		src.SetRO(dev)
		log.Fatalf("Couldn't start the wipe: %v", err)
	}
	src.Forget(dev)
	src.SetRO(dev)

	// SMART afterwards, from a context the stop signal can't cancel: a
	// stopped wipe still gets its record.
	js, text, err := src.SMART(context.Background(), dev)
	after, perr := smart.Parse(js)
	if err != nil || perr != nil {
		log.Printf("SMART after the wipe: %v %v", err, perr)
		after = before
	}
	health := smart.Assess(after)
	fails, notes := smart.Rising(before, after)
	health.Notes = append(health.Notes, notes...)
	if len(notes) > 0 && health.Grade == smart.Pass {
		health.Grade = smart.Notes
	}
	if len(fails) > 0 {
		health.Notes = append(health.Notes, fails...)
		health.Grade = smart.Fail
	}
	switch {
	case res.Bad > 0:
		health.Notes = append(health.Notes, fmt.Sprintf("%s bytes didn't read back as written.", records.Commas(res.Bad)))
		health.Grade = smart.Fail
	case res.Aborted != "":
		health.Notes = append(health.Notes, "The wipe was "+res.Aborted+", so it proves nothing about the rest of the drive.")
	}
	if slow := records.SlowLine(res); slow != "none" {
		health.Notes = append(health.Notes, "Slow blocks: "+slow+".")
		if health.Grade == smart.Pass {
			health.Grade = smart.Notes
		}
	}
	d.SMART, d.Health, d.ReadOnly = after, health, true

	host, _ := os.Hostname()
	rec := &records.Record{
		Serial: d.Serial(), Started: session, Finished: time.Now().UTC(),
		Host: host, Bay: cfg["name"], Software: fmt.Sprintf("zwipe %s (%s)", version, commit),
		Drive: *d, Health: health, Wrote: res.Written > 0, Wipe: res, Before: before,
	}
	store := &records.Store{Root: *root}
	err = store.SaveWipe(rec, map[string][]byte{
		"smart-before.json": snap.SmartJSON, "smart-before.txt": snap.SmartText,
		"smart.json": js, "smart.txt": text,
	})
	if err != nil {
		log.Fatalf("The wipe ran but the record didn't save: %v", err)
	}
	log.Printf("Wipe %s.  Write %s, verify %s.  Grade %s.", records.WipeVerdict(res), records.Pass(res, false), records.Pass(res, true), health.Grade)
	log.Printf("Record: %s", store.Path(records.DirName(rec.Serial), rec.ID, "record.md"))
	if !res.OK() {
		os.Exit(1)
	}
}

// progress is how far through both passes, 0 to 1.
func progress(pass string, i, n int) float64 {
	p := float64(i+1) / float64(n) / 2
	if pass == "verify" {
		p += .5
	}
	return p
}

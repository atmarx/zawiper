// zpayload makes a set for Wipe + Load (spec §6.3): it walks a folder, hashes
// every file, and writes the set into payloads.txt at the top of the payload
// folder, replacing a set of the same name and keeping the others.
//
//	zpayload -set isos -label ISOS /mnt/nas/payloads
//	zpayload -set mega -label THE-ARK -root mega -exclude .ark /mnt/nas/payloads
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/atmarx/zawiper/internal/payload"
)

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(s string) error { *l = append(*l, s); return nil }

func main() {
	log.SetFlags(0)
	name := flag.String("set", "", "the set's name, as the confirm shows it (required)")
	label := flag.String("label", "", "the volume label, up to 11 characters (required)")
	desc := flag.String("description", "", "a line about it, for the confirm")
	root := flag.String("root", "", "the set's folder inside the payload folder; a file's path under it is where it lands on the drive")
	var exclude list
	flag.Var(&exclude, "exclude", "a name or relative path to leave out; repeat for more")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: zpayload -set NAME -label LABEL [flags] <payload-folder>")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *name == "" || *label == "" || flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	dir := flag.Arg(0)
	s := payload.Set{Name: *name, Description: *desc, Label: *label, Filesystem: "exfat", Root: *root}

	set, warn, err := payload.Scan(filepath.Join(dir, *root), s, exclude, func(p string) { fmt.Fprintln(os.Stderr, "hashing", p) })
	if err != nil {
		log.Fatal(err)
	}
	for _, w := range warn {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}

	path := filepath.Join(dir, payload.FileName)
	var sets []payload.Set
	if f, err := os.Open(path); err == nil {
		sets, err = payload.Parse(f)
		f.Close()
		if err != nil {
			log.Fatalf("%s: %v.  Fix it, or move it aside.", path, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		log.Fatal(err)
	}
	var out bytes.Buffer
	placed := false
	for i := range sets {
		if sets[i].Name == set.Name {
			sets[i], placed = *set, true
		}
		out.Write(sets[i].Text())
		out.WriteByte('\n')
	}
	if !placed {
		out.Write(set.Text())
	}
	// Whatever went in has to parse back, or every bay would refuse it.
	if _, err := payload.Parse(bytes.NewReader(out.Bytes())); err != nil {
		log.Fatalf("that would make a file bays refuse: %v", err)
	}
	if err := os.WriteFile(path+".tmp", out.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s: set %s, %d files, %d bytes, list %s\n", path, set.Name, len(set.Files), set.Bytes(), set.Hash())
}

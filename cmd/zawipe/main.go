// zawipe is the hub's plain-text look at a terminal: the wall as a DIR
// listing, or one bay scrolling the way CHKDSK did.  It only reads.  A wipe
// still starts from the page, confirmed against the drive's serial.
//
//	zawipe -hub https://zawiper.example.org             every bay, a line each
//	zawipe -hub https://zawiper.example.org lab-a/bay1  one bay, following it
//	zawipe -once lab-a\bay1                             one bay, as it stands
//
// -hub defaults to $ZAWIPER_HUB.  On a terminal, a pass's progress counts
// up in place; piped into a file, every step gets its own line.  It exits 0
// when the hub answered, 1 when it didn't, and 2 on a bad command line.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, tty(os.Stdout))) }

func run(args []string, stdout, stderr io.Writer, inPlace bool) int {
	fs := flag.NewFlagSet("zawipe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hub := fs.String("hub", os.Getenv("ZAWIPER_HUB"), "the hub's page, e.g. https://zawiper.example.org (default $ZAWIPER_HUB)")
	once := fs.Bool("once", false, "print the bay as it stands and stop, instead of following it")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "zawipe %s (%s): the hub in plain text\n\nusage: zawipe [-hub URL] [-once] [host/bay]\n\n", version, commit)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *hub == "" || fs.NArg() > 1 {
		fs.Usage()
		return 2
	}
	base, err := url.Parse(strings.TrimRight(*hub, "/"))
	if err != nil || base.Host == "" {
		fmt.Fprintf(stderr, "zawipe: %q isn't a URL\n", *hub)
		return 2
	}
	path, follow := "/text", false
	if fs.NArg() == 1 {
		host, name, ok := strings.Cut(strings.ReplaceAll(fs.Arg(0), `\`, "/"), "/")
		if !ok || host == "" || name == "" || strings.Contains(name, "/") {
			fmt.Fprintf(stderr, "zawipe: a bay is host/name, like lab-a/bay1\n")
			return 2
		}
		path, follow = "/bays/"+url.PathEscape(host)+"/"+url.PathEscape(name)+"/text", !*once
	}
	u := base.String() + path
	if follow {
		u += "?follow"
	}
	resp, err := http.Get(u)
	if err != nil {
		fmt.Fprintf(stderr, "zawipe: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "zawipe: the hub said %s\n", resp.Status)
		return 1
	}
	if err := copyLines(stdout, resp.Body, inPlace); err != nil {
		fmt.Fprintf(stderr, "zawipe: %v\n", err)
		return 1
	}
	return 0
}

// copyLines prints the hub's text.  inPlace turns a run of progress lines
// for one pass into a single line that counts up, with a carriage return.
func copyLines(w io.Writer, r io.Reader, inPlace bool) error {
	sc := bufio.NewScanner(r)
	var held string // a progress line printed without its newline yet
	for sc.Scan() {
		line := sc.Text()
		if inPlace && held != "" && progress(line) == progress(held) {
			fmt.Fprintf(w, "\r%s", line)
			held = line
			continue
		}
		if held != "" {
			fmt.Fprintln(w)
			held = ""
		}
		if inPlace && progress(line) != "" {
			fmt.Fprint(w, line)
			held = line
			continue
		}
		fmt.Fprintln(w, line)
	}
	if held != "" {
		fmt.Fprintln(w)
	}
	return sc.Err()
}

// progress is what a pass's progress line says before its percentage, or ""
// if line isn't one.
func progress(line string) string {
	head, _, ok := strings.Cut(line, "... ")
	if !ok || !strings.HasSuffix(line, " percent completed.") {
		return ""
	}
	return head
}

func tty(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

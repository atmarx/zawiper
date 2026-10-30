package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const stream = "Za Wiper  bay1 on lab-a\n\nWriting the keystream... 98 percent completed.\n    Block 913 of 932.\n" +
	"Writing the keystream... 99 percent completed.\nWriting the keystream... 100 percent completed.\n" +
	"Reading it back and comparing... 0 percent completed.\n"

// On a terminal a pass counts up on one line; into a file, a line a step.
func TestCopyLines(t *testing.T) {
	var term, file bytes.Buffer
	copyLines(&term, strings.NewReader(stream), true)
	copyLines(&file, strings.NewReader(stream), false)
	if file.String() != stream {
		t.Errorf("piped output changed:\n%q", file.String())
	}
	want := "Za Wiper  bay1 on lab-a\n\nWriting the keystream... 98 percent completed.\n    Block 913 of 932.\n" +
		"Writing the keystream... 99 percent completed.\rWriting the keystream... 100 percent completed.\n" +
		"Reading it back and comparing... 0 percent completed.\n"
	if term.String() != want {
		t.Errorf("terminal output:\n%q\nwant\n%q", term.String(), want)
	}
}

func TestRun(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.RequestURI()
		w.Write([]byte("ok\n"))
	}))
	defer srv.Close()
	for _, c := range []struct {
		args []string
		code int
		path string
	}{
		{[]string{"-hub", srv.URL}, 0, "/text"},
		{[]string{"-hub", srv.URL, "lab-a/bay1"}, 0, "/bays/lab-a/bay1/text?follow"},
		{[]string{"-hub", srv.URL, "-once", `lab-a\bay1`}, 0, "/bays/lab-a/bay1/text"},
		{[]string{"-hub", srv.URL, "bay1"}, 2, ""},
		{[]string{"-hub", srv.URL, "a/b/c"}, 2, ""},
		{[]string{"lab-a/bay1"}, 2, ""},
	} {
		asked = ""
		var out, errs bytes.Buffer
		t.Setenv("ZAWIPER_HUB", "")
		if code := run(c.args, &out, &errs, false); code != c.code || asked != c.path {
			t.Errorf("%q: exit %d asking %q, want %d asking %q (%s)", c.args, code, asked, c.code, c.path, errs.String())
		}
	}
}

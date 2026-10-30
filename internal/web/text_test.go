package web

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/atmarx/zawiper/internal/hub"
	"github.com/atmarx/zawiper/internal/model"
)

// at is p percent of size, rounded up so the text says p.
func at(size int64, p int64) int64 { return (size*p + 99) / 100 }

// running puts a wipe on bay1, in phase, done bytes through both passes.
func running(phase string, done int64) func(*hub.Bay) {
	return func(b *hub.Bay) {
		total := b.Snap.Drive.Size
		b.Snap.State = model.Running
		b.Snap.Job = &model.Job{Kind: "wipe", Phase: phase, Serial: b.Snap.Drive.Serial(), Session: session,
			Started: t0, Map: strings.Repeat(".", 932), Done: done, Total: total, MBps: 150, ETA: 3600}
	}
}

func TestTextWall(t *testing.T) {
	_, h, _ := setup(t)
	w := get(h, "/text")
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content type %q", ct)
	}
	body := w.Body.String()
	for _, want := range []string{`C:\ZAWIPER> ZAWIPE` + "\n", `lab-a\bay1  PRESENT`, `lab-a\bay2  EMPTY`, `lab-b\sas0  OFFLINE`, "3 bays\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("wall text lacks %q:\n%s", want, body)
		}
	}
	for _, l := range strings.Split(body, "\n") {
		if strings.HasSuffix(l, " ") {
			t.Errorf("trailing space: %q", l)
		}
	}
}

func TestTextBay(t *testing.T) {
	f, h, drv := setup(t)
	body := get(h, "/bays/lab-a/bay1/text").Body.String()
	for _, want := range []string{`C:\ZAWIPER> ZAWIPE lab-a\bay1` + "\n\n", "Drive " + drv.Serial(), "Health: ", `Looks like backup media: labelled "backup-gamma".`, "Ready."} {
		if !strings.Contains(body, want) {
			t.Errorf("bay text lacks %q:\n%s", want, body)
		}
	}
	if w := get(h, "/bays/lab-a/nope/text"); w.Code != http.StatusNotFound {
		t.Errorf("a bay that isn't there: %d", w.Code)
	}

	f.set("lab-a/bay1", running("write", at(drv.Size, 55)))
	body = get(h, "/bays/lab-a/bay1/text").Body.String()
	for _, want := range []string{`ZAWIPE lab-a\bay1 /WIPE /V:FULL` + "\n", "Writing the keystream... 55 percent completed.\n", "to go."} {
		if !strings.Contains(body, want) {
			t.Errorf("running text lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Ready.") {
		t.Errorf("a running bay says it's ready:\n%s", body)
	}

	f.set("lab-a/bay1", running("verify", drv.Size+at(drv.Size, 25)))
	body = get(h, "/bays/lab-a/bay1/text").Body.String()
	for _, want := range []string{"Writing the keystream... 100 percent completed.\n", "Reading it back and comparing... 25 percent completed.\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("read-back text lacks %q:\n%s", want, body)
		}
	}
}

// A follower sees each step once: the pass counting up, the write's last
// line when the read-back starts, the ending, and a fresh command line when
// the bay moves on to something new.
func TestFollow(t *testing.T) {
	f, _, drv := setup(t)
	bay := func(mod func(*hub.Bay)) hub.Bay {
		f.set("lab-a/bay1", mod)
		b, _ := f.Bay("lab-a/bay1")
		return b
	}
	lines, live := transcript(bay(running("write", at(drv.Size, 55))))
	step := func(b hub.Bay, want ...string) {
		t.Helper()
		var out []string
		out, lines, live = follow(lines, live, b)
		if !slices.Equal(out, want) {
			t.Errorf("printed %q, want %q", out, want)
		}
	}
	step(bay(running("write", at(drv.Size, 55))))
	step(bay(running("write", at(drv.Size, 56))), "Writing the keystream... 56 percent completed.")
	step(bay(running("verify", drv.Size)),
		"Writing the keystream... 100 percent completed.", "Reading it back and comparing... 0 percent completed.")
	step(bay(func(b *hub.Bay) {
		b.Snap.State = model.Done
		b.Snap.Job.Done, b.Snap.Job.Ended, b.Snap.Job.RecordID = 2*drv.Size, t0.Add(4*time.Hour), "r1"
	}), "Reading it back and comparing... 100 percent completed.", "",
		"Every block written, read back, and compared.", "Took 4:00:00, 139 MB/s on average.",
		health(drv)[0],
		"Record r1, filed under "+drv.Serial()+".  It names the level and the grade.")

	out, _, _ := follow(lines, live, bay(func(b *hub.Bay) {
		b.Snap.State, b.Snap.Job, b.Snap.Drive = model.Empty, nil, nil
	}))
	if len(out) < 3 || out[1] != `C:\ZAWIPER> ZAWIPE lab-a\bay1` || out[len(out)-1] != "No drive in the bay." {
		t.Errorf("a pulled drive printed %q", out)
	}
}

// ?follow holds the response open and sends each change as it happens.
func TestTextFollowStream(t *testing.T) {
	f, h, drv := setup(t)
	f.set("lab-a/bay1", running("write", at(drv.Size, 10)))
	srv := httptest.NewServer(h)
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/bays/lab-a/bay1/text?follow", nil)
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	until := func(want string) {
		t.Helper()
		timeout := time.After(5 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("stream ended before %q", want)
				}
				if l == want {
					return
				}
			case <-timeout:
				t.Fatalf("no %q", want)
			}
		}
	}
	until("Writing the keystream... 10 percent completed.")
	f.set("lab-a/bay1", running("write", at(drv.Size, 11)))
	until("Writing the keystream... 11 percent completed.")
}

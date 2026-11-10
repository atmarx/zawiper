package payload

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sum = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

func one(line string) string {
	return "set a\n  label ARK\n  file " + line + "\n"
}

func TestParse(t *testing.T) {
	sets, err := Parse(strings.NewReader(`# a comment
set mega
  description The whole thing
  label THE-ARK
  filesystem exfat
  file ` + sum + `  4  has space/file name.zim

set mini
  label THE-ARK
  file ` + sum + ` 4 b
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 2 || sets[0].Name != "mega" || sets[1].Filesystem != "exfat" {
		t.Fatalf("got %+v", sets)
	}
	if got := sets[0].Files[0].Path; got != "has space/file name.zim" {
		t.Errorf("path %q", got)
	}
	// The stamp is the set, and it parses back to the same hash.
	back, err := Parse(strings.NewReader(string(sets[0].Text())))
	if err != nil || back[0].Hash() != sets[0].Hash() {
		t.Errorf("round trip: %v, %s != %s", err, back[0].Hash(), sets[0].Hash())
	}
}

func TestParseRefuses(t *testing.T) {
	for name, in := range map[string]string{
		"traversal":    one(sum + " 4 ../../etc/passwd"),
		"inner":        one(sum + " 4 a/../../b"),
		"absolute":     one(sum + " 4 /etc/passwd"),
		"backslash":    one(sum + ` 4 a\b`),
		"colon":        one(sum + " 4 a:b"),
		"control":      one(sum + " 4 a\tb"),
		"unclean":      one(sum + " 4 a//b"),
		"dot":          one(sum + " 4 ./a"),
		"trailing dot": one(sum + " 4 a."),
		"short hash":   one("abc 4 a"),
		"upper hash":   one(strings.ToUpper(sum) + " 4 a"),
		"bad size":     one(sum + " -4 a"),
		"no path":      one(sum + " 4"),
		"reserved":     one(sum + " 4 " + Ballast),
		"reserved low": one(sum + " 4 za-wiper.json"),
		"before set":   "label ARK\n",
		"unknown key":  "set a\n  owner me\n",
		"two sets":     "set a\n  label A\n  file " + sum + " 1 x\nset a\n  label A\n  file " + sum + " 1 x\n",
		"no label":     "set a\n  file " + sum + " 1 x\n",
		"no files":     "set a\n  label A\n",
		"filesystem":   "set a\n  filesystem ext4\n",
		"long label":   "set a\n  label ABCDEFGHIJKL\n",
		"case twins":   "set a\n  label A\n  file " + sum + " 1 x\n  file " + sum + " 1 X\n",
	} {
		if _, err := Parse(strings.NewReader(in)); err == nil {
			t.Errorf("%s: parsed %q", name, in)
		}
	}
}

func TestBiggestFits(t *testing.T) {
	file := func(n int64) []File { return []File{{SHA256: sum, Size: n, Path: "x"}} }
	const gb = int64(1e9)
	sets := []Set{
		{Name: "mini", Label: "A", Filesystem: "exfat", Files: file(224 * gb)},
		{Name: "medium", Label: "A", Filesystem: "exfat", Files: file(445 * gb)},
		{Name: "mega", Label: "A", Filesystem: "exfat", Files: file(868 * gb)},
	}
	for drive, want := range map[int64]int{1000 * gb: 2, 500 * gb: 1, 250 * gb: 0, 100 * gb: -1, 449 * gb: 0} {
		if got := Biggest(sets, drive); got != want {
			t.Errorf("%d GB: picked %d, want %d", drive/gb, got, want)
		}
	}
}

func TestHandlerServesOnlyWhatsListed(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "listed.bin"), []byte("hello world"), 0o644)
	os.WriteFile(filepath.Join(dir, "secret"), []byte("no"), 0o644)
	txt := "set a\n  label A\n  file " + sum + " 11 listed.bin\n"
	os.WriteFile(filepath.Join(dir, FileName), []byte(txt), 0o644)
	srv := httptest.NewServer((&Library{Dir: dir}).Handler())
	defer srv.Close()

	get := func(p string, hdr ...string) (int, string) {
		req, _ := http.NewRequest("GET", srv.URL+p, nil)
		if len(hdr) > 0 {
			req.Header.Set("Range", hdr[0])
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, body := get("/sets/a/files/listed.bin"); code != 200 || body != "hello world" {
		t.Errorf("listed: %d %q", code, body)
	}
	if code, body := get("/sets/a/files/listed.bin", "bytes=6-"); code != 206 || body != "world" {
		t.Errorf("range: %d %q", code, body)
	}
	for _, p := range []string{"/sets/a/files/secret", "/sets/a/files/" + FileName, "/sets/a/files/..%2Fsecret", "/sets/b/files/listed.bin"} {
		if code, _ := get(p); code == 200 {
			t.Errorf("%s served", p)
		}
	}
	if code, body := get("/sets/a"); code != 200 || !strings.Contains(body, "listed.bin") {
		t.Errorf("list: %d %q", code, body)
	}
}

func TestScan(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "a.iso"), []byte("test"), 0o644)
	os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!"), 0o755)
	os.Symlink("sub", filepath.Join(dir, "linkdir"))
	os.Symlink("sub/a.iso", filepath.Join(dir, "linkfile"))
	s, warn, err := Scan(dir, Set{Name: "x", Label: "X", Filesystem: "exfat"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Files) != 3 {
		t.Fatalf("files: %+v", s.Files)
	}
	if f := s.Find("sub/a.iso"); f == nil || f.SHA256 != sum || f.Size != 4 {
		t.Errorf("hash: %+v", f)
	}
	if s.Find("linkfile") == nil {
		t.Error("a symlink to a file wasn't followed")
	}
	all := strings.Join(warn, "\n")
	if !strings.Contains(all, "linkdir") || !strings.Contains(all, "run.sh") {
		t.Errorf("warnings: %s", all)
	}
	if _, err := Parse(strings.NewReader(string(s.Text()))); err != nil {
		t.Errorf("Scan wrote something Parse refuses: %v", err)
	}
}

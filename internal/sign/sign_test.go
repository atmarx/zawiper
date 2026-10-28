package sign

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"zawiper/internal/model"
	"zawiper/internal/records"
	"zawiper/internal/smart"
	"zawiper/internal/wipe"
)

func key(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keys", "za-wiper")
	if _, err := GenerateKey(path); err != nil {
		t.Fatal(err)
	}
	k, err := LoadKey(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, k
}

func TestSignAndVerify(t *testing.T) {
	path, k := key(t)
	if _, err := GenerateKey(path); err == nil {
		t.Error("GenerateKey overwrote a key")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("private key mode %v", fi.Mode().Perm())
	}
	line, _ := os.ReadFile(path + ".pub")
	pub, err := ParseAuthorizedKey(line)
	if err != nil || !pub.Equal(k.Public()) {
		t.Fatalf("the .pub doesn't hold the key: %v", err)
	}
	if !strings.HasPrefix(string(line), "ssh-ed25519 AAAA") || !strings.HasSuffix(string(line), " za-wiper\n") {
		t.Errorf(".pub is %q", line)
	}

	msg := []byte(`{"serial":"9VPAX9NA"}` + "\n")
	sig := Sign(k, msg)
	if !bytes.HasPrefix(sig, []byte("-----BEGIN SSH SIGNATURE-----\n")) {
		t.Errorf("not armored: %s", sig)
	}
	if err := Verify(pub, msg, sig); err != nil {
		t.Errorf("good signature: %v", err)
	}
	if err := Verify(pub, append(msg, ' '), sig); err == nil {
		t.Error("a changed record verified")
	}
	other, _, _ := ed25519.GenerateKey(nil)
	if err := Verify(other, msg, sig); err == nil || !strings.Contains(err.Error(), "not by the key you gave") {
		t.Errorf("someone else's key: %v", err)
	}
	if signer, _ := SignedBy(sig); !signer.Equal(pub) {
		t.Error("SignedBy")
	}
	if _, err := ParsePrivateKey([]byte("-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----\n")); err == nil {
		t.Error("parsed garbage as a key")
	}
}

// ssh-keygen is the verifier a buyer already has.  Check both directions:
// it accepts what we sign, and we accept what it signs with our key file.
func TestOpenSSH(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen here")
	}
	path, k := key(t)
	dir := t.TempDir()
	msg := []byte("{\"schema\":1}\n")
	rec := filepath.Join(dir, "record.json")
	os.WriteFile(rec, msg, 0o644)
	os.WriteFile(rec+".sig", Sign(k, msg), 0o644)
	allowed := filepath.Join(dir, "allowed_signers")
	pub := k.Public().(ed25519.PublicKey)
	os.WriteFile(allowed, []byte(Principal+" "+AuthorizedKey(pub)+"\n"), 0o644)

	run := func(stdin []byte, args ...string) (string, error) {
		cmd := exec.Command("ssh-keygen", args...)
		cmd.Stdin = bytes.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run(msg, "-Y", "verify", "-f", allowed, "-I", Principal, "-n", Namespace, "-s", rec+".sig")
	if err != nil || !strings.Contains(out, "Good \"za-wiper\" signature") {
		t.Errorf("ssh-keygen -Y verify: %v\n%s", err, out)
	}
	if _, err := run(append(msg, 'x'), "-Y", "verify", "-f", allowed, "-I", Principal, "-n", Namespace, "-s", rec+".sig"); err == nil {
		t.Error("ssh-keygen accepted a changed record")
	}
	if _, err := run(msg, "-Y", "verify", "-f", allowed, "-I", Principal, "-n", "git", "-s", rec+".sig"); err == nil {
		t.Error("ssh-keygen accepted the signature in another namespace")
	}

	// Our private key file is one OpenSSH reads.
	out, err = run(nil, "-y", "-f", path)
	if err != nil || strings.Fields(out)[1] != strings.Fields(AuthorizedKey(pub))[1] {
		t.Errorf("ssh-keygen -y: %v %s", err, out)
	}
	out, err = run(nil, "-l", "-f", path+".pub")
	if err != nil || !strings.Contains(out, Fingerprint(pub)) {
		t.Errorf("fingerprint %s, ssh-keygen says %s", Fingerprint(pub), out)
	}
	ours, _ := os.ReadFile(rec + ".sig")
	os.Remove(rec + ".sig")
	if out, err := run(nil, "-Y", "sign", "-f", path, "-n", Namespace, rec); err != nil {
		t.Fatalf("ssh-keygen -Y sign: %v %s", err, out)
	}
	theirs, _ := os.ReadFile(rec + ".sig")
	// Ed25519 is deterministic, so the same key over the same record makes
	// the same signature, down to the armor.
	if !bytes.Equal(theirs, ours) {
		t.Errorf("ssh-keygen signed differently:\n%s\nours:\n%s", theirs, ours)
	}
	if err := Verify(pub, msg, theirs); err != nil {
		t.Errorf("ssh-keygen's own signature: %v", err)
	}
}

func TestSignRecord(t *testing.T) {
	_, k := key(t)
	store := &records.Store{Root: t.TempDir()}
	js, err := os.ReadFile("../smart/testdata/st31000528as.json")
	if err != nil {
		t.Fatal(err)
	}
	info, _ := smart.Parse(js)
	t0 := time.Date(2026, 10, 24, 9, 15, 40, 0, time.UTC)
	size := info.CapacityBytes
	r := &records.Record{Serial: info.Serial, Started: t0, Finished: t0.Add(time.Hour), Host: "lab-a", Bay: "bay1",
		Software: "bayd test", Drive: model.Drive{Size: size, SMART: info, Capacity: &model.Capacity{Method: "hdparm -N; no DCO", Visible: size / 512, Native: size / 512}}, Health: smart.Assess(info),
		Wrote: true, Before: info,
		Wipe: &wipe.Result{Size: size, Block: 1 << 30, Written: size, Checked: size, Started: t0, Wrote: t0, Verified: t0,
			Cells: []wipe.Cell{{Write: 1, Read: 1}}, Key: strings.Repeat("ab", 32), IV: strings.Repeat("cd", 16)}}
	if err := store.SaveWipe(r, map[string][]byte{}); err != nil {
		t.Fatal(err)
	}
	s := &Signer{Key: k, KeyURL: "https://zawiper.example/za-wiper.pub", SiteURL: "https://zawiper.example"}
	if seal := s.Seal(store, r.Serial, r.ID); seal.Signed() || !strings.Contains(seal.Zverify, "-no-sig") {
		t.Errorf("unsigned record: %+v", seal)
	}
	if err := s.SignRecord(store, r.Serial, r.ID); err != nil {
		t.Fatal(err)
	}
	seal := s.Seal(store, r.Serial, r.ID)
	if !seal.Signed() || seal.Fingerprint != Fingerprint(k.Public().(ed25519.PublicKey)) {
		t.Fatalf("seal %+v", seal)
	}
	if want := "zverify -pubkey https://zawiper.example/za-wiper.pub https://zawiper.example/drives/9VPAX9NA/2026-10-24T09-15-40Z-wipe/record.json /dev/sdX"; seal.Zverify != want {
		t.Errorf("zverify line %q", seal.Zverify)
	}
	md, _ := os.ReadFile(store.Path(r.Serial, r.ID, "record.md"))
	for _, want := range []string{"Certificate of sanitization", "| Level | **Clear** |", seal.Fingerprint, "ssh-keygen -Y verify -f allowed_signers -I za-wiper -n za-wiper -s record.json.sig < record.json", "echo 'za-wiper ssh-ed25519 AAAA", "https://zawiper.example/za-wiper.pub"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("record.md lacks %q", want)
		}
	}
	if strings.Contains(string(md), "Not signed yet") {
		t.Error("a signed record.md says it isn't")
	}

	// Anyone editing record.json afterwards breaks the seal, and the page
	// must say so rather than print it as good.
	p := store.Path(r.Serial, r.ID, "record.json")
	b, _ := os.ReadFile(p)
	os.WriteFile(p, bytes.Replace(b, []byte(`"bay1"`), []byte(`"bay2"`), 1), 0o644)
	if seal := s.Seal(store, r.Serial, r.ID); seal.Signed() || !strings.Contains(seal.Problem, "changed after signing") {
		t.Errorf("tampered record: %+v", seal)
	}
	// So does a signature from some other key.
	other := &Signer{Key: ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, 32))}
	if err := other.SignRecord(store, r.Serial, r.ID); err != nil {
		t.Fatal(err)
	}
	if seal := s.Seal(store, r.Serial, r.ID); seal.Signed() || !strings.Contains(seal.Problem, "isn't this hub's key") {
		t.Errorf("foreign key: %+v", seal)
	}
	if err := s.SignRecord(store, r.Serial, "nope"); err == nil {
		t.Error("signed a record that isn't there")
	}
}

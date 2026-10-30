package sign

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/atmarx/zawiper/internal/records"
)

// Signer is the hub's signing key, and where buyers find its public half
// and the records.  Both addresses are printed on certificates; either can
// be empty, and the certificate leaves that line out.
type Signer struct {
	Key ed25519.PrivateKey
	// KeyURL is where the public key is published, as one
	// `ssh-ed25519 AAAA… za-wiper` line.
	KeyURL string
	// SiteURL is the hub's address with no trailing slash, as in
	// https://zawiper.example.org.  Records live under /drives/ on it.
	SiteURL string
}

// SigFile sits beside record.json.
const SigFile = "record.json.sig"

// SignRecord signs a record's record.json as it stands on disk, writes
// record.json.sig beside it, and renders record.md again with the
// signature block.  serial is the record's folder name, as Store.Path
// takes it.  The hub calls this whenever a record arrives, and again if it
// ever rewrites one.
func (s *Signer) SignRecord(store *records.Store, serial, id string) error {
	js, err := os.ReadFile(store.Path(serial, id, "record.json"))
	if err != nil {
		return err
	}
	var r records.Record
	if err := json.Unmarshal(js, &r); err != nil {
		return fmt.Errorf("%s/%s: %w", serial, id, err)
	}
	sig := Sign(s.Key, js)
	if err := writeAtomic(store.Path(serial, id, SigFile), sig); err != nil {
		return err
	}
	return writeAtomic(store.Path(serial, id, "record.md"), []byte(records.MarkdownSealed(&r, s.seal(serial, id, js, sig))))
}

// Seal says what a record's certificate should print about its signature.
// It checks the signature on file against record.json as it stands now and
// against this hub's key, so a stale or foreign signature is reported, not
// printed as good.  A nil Signer still reads the signature; it just can't
// say where anything is published.
func (s *Signer) Seal(store *records.Store, serial, id string) *records.Seal {
	js, err := os.ReadFile(store.Path(serial, id, "record.json"))
	if err != nil {
		return nil
	}
	sig, _ := os.ReadFile(store.Path(serial, id, SigFile))
	return s.seal(serial, id, js, sig)
}

func (s *Signer) seal(serial, id string, js, sig []byte) *records.Seal {
	out := &records.Seal{}
	var keyURL, site string
	if s != nil {
		keyURL, site = s.KeyURL, s.SiteURL
	}
	out.KeyURL = keyURL
	recordJSON := "record.json"
	if site != "" {
		out.RecordURL = site + "/drives/" + serial + "/" + id
		recordJSON = out.RecordURL + "/record.json"
	}
	pubArg := "za-wiper.pub"
	if keyURL != "" {
		pubArg = keyURL
	}
	out.Zverify = "zverify -pubkey " + pubArg + " " + recordJSON + " /dev/sdX"
	if len(sig) == 0 {
		out.Zverify = "zverify -no-sig " + recordJSON + " /dev/sdX"
		return out
	}

	pub, err := SignedBy(sig)
	if err != nil {
		out.Problem = "record.json.sig can't be read: " + err.Error() + "."
		return out
	}
	out.Fingerprint, out.PublicKey = Fingerprint(pub), AuthorizedKey(pub)
	if s != nil && s.Key != nil && !pub.Equal(s.Key.Public()) {
		out.Problem = "It was made with " + out.Fingerprint + ", which isn't this hub's key."
		return out
	}
	if err := Verify(pub, js, sig); err != nil {
		out.Problem = upper(err.Error()) + "."
		return out
	}
	out.SSHKeygen = AllowedSigners(pub) + "\n" + VerifyCommand
	return out
}

// AllowedSigners is the shell line that makes an allowed_signers file
// trusting this key for records.
func AllowedSigners(pub ed25519.PublicKey) string {
	return "echo '" + Principal + " " + strings.TrimSuffix(AuthorizedKey(pub), " "+Principal) + "' > allowed_signers"
}

// VerifyCommand checks record.json against its signature with OpenSSH.
const VerifyCommand = "ssh-keygen -Y verify -f allowed_signers -I " + Principal + " -n " + Namespace + " -s " + SigFile + " < record.json"

func upper(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// writeAtomic is the store's own write: a temp file, synced, renamed over.
func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// Package sign signs records the way `ssh-keygen -Y sign` does, so a buyer
// can check one with nothing but OpenSSH:
//
//	echo 'za-wiper ssh-ed25519 AAAA…' > allowed_signers
//	ssh-keygen -Y verify -f allowed_signers -I za-wiper -n za-wiper -s record.json.sig < record.json
//
// The format is SSHSIG (OpenSSH's PROTOCOL.sshsig) over Ed25519, written in
// plain Go so the hub needs no ssh-keygen and no key agent.  The private key
// is kept in OpenSSH's own unencrypted format, so `ssh-keygen -y -f` can
// print its public half and an operator can sign by hand in a pinch.
package sign

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Namespace is the SSHSIG namespace for records.  A signature made for one
// namespace never verifies in another, so a record signature can't be
// replayed as, say, a git commit signature.
const Namespace = "za-wiper"

// Principal names the key in an allowed_signers file.
const Principal = "za-wiper"

const (
	keyType   = "ssh-ed25519"
	sigMagic  = "SSHSIG"
	sigBegin  = "-----BEGIN SSH SIGNATURE-----"
	sigEnd    = "-----END SSH SIGNATURE-----"
	keyBegin  = "-----BEGIN OPENSSH PRIVATE KEY-----"
	keyEnd    = "-----END OPENSSH PRIVATE KEY-----"
	keyMagic  = "openssh-key-v1\x00"
	hashAlg   = "sha512"
	armorCols = 70 // what ssh-keygen writes
)

// GenerateKey makes a signing key.  It writes the private key to path
// (mode 0600, OpenSSH format) and the public key to path+".pub" as one
// `ssh-ed25519 AAAA… za-wiper` line, and refuses to overwrite either.
func GenerateKey(path string) (ed25519.PublicKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := writeNew(path, MarshalPrivateKey(priv, Principal), 0o600); err != nil {
		return nil, err
	}
	if err := writeNew(path+".pub", []byte(AuthorizedKey(pub)+"\n"), 0o644); err != nil {
		return nil, err
	}
	return pub, nil
}

func writeNew(path string, b []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// LoadKey reads a private key GenerateKey wrote.
func LoadKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	k, err := ParsePrivateKey(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return k, nil
}

// AuthorizedKey is the public key as one line: `ssh-ed25519 AAAA… za-wiper`.
func AuthorizedKey(pub ed25519.PublicKey) string {
	return keyType + " " + base64.StdEncoding.EncodeToString(wirePub(pub)) + " " + Principal
}

// ParseAuthorizedKey finds an ssh-ed25519 key in a line from a .pub,
// authorized_keys or allowed_signers file.  Anything around the key type and
// the base64 (a principal, options, a comment) is ignored.
func ParseAuthorizedKey(line []byte) (ed25519.PublicKey, error) {
	f := strings.Fields(string(line))
	for i := 0; i+1 < len(f); i++ {
		if f[i] != keyType {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(f[i+1])
		if err != nil {
			return nil, fmt.Errorf("the key's base64 is damaged: %w", err)
		}
		return parseWirePub(b)
	}
	return nil, errors.New("no ssh-ed25519 public key in it")
}

// Fingerprint is the key's SHA-256 fingerprint as `ssh-keygen -l` prints it.
func Fingerprint(pub ed25519.PublicKey) string {
	h := sha256.Sum256(wirePub(pub))
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(h[:])
}

// Sign returns an armored SSH signature over msg.
func Sign(key ed25519.PrivateKey, msg []byte) []byte {
	sig := ed25519.Sign(key, signedData(msg))
	var blob []byte
	blob = append(blob, sigMagic...)
	blob = binary.BigEndian.AppendUint32(blob, 1)
	blob = appendString(blob, wirePub(key.Public().(ed25519.PublicKey)))
	blob = appendString(blob, []byte(Namespace))
	blob = appendString(blob, nil) // reserved
	blob = appendString(blob, []byte(hashAlg))
	blob = appendString(blob, appendString(appendString(nil, []byte(keyType)), sig))
	return armor(sigBegin, sigEnd, blob)
}

// Verify checks an armored signature over msg against the key you trust.
func Verify(pub ed25519.PublicKey, msg, armored []byte) error {
	signer, sig, alg, err := parseSig(armored)
	if err != nil {
		return err
	}
	if !bytes.Equal(signer, pub) {
		return fmt.Errorf("it was signed by %s, not by the key you gave (%s)", Fingerprint(signer), Fingerprint(pub))
	}
	if !ed25519.Verify(pub, signedDataAlg(msg, alg), sig) {
		return errors.New("the signature doesn't match the file: it was changed after signing, or signed for something else")
	}
	return nil
}

// SignedBy returns the public key a signature claims to be from.  That proves
// nothing on its own; it's for printing the fingerprint beside a signature
// that Verify has already checked.
func SignedBy(armored []byte) (ed25519.PublicKey, error) {
	pub, _, _, err := parseSig(armored)
	return pub, err
}

// signedData is what SSHSIG actually signs: the namespace and a hash of the
// message, never the message itself.
func signedData(msg []byte) []byte { return signedDataAlg(msg, hashAlg) }

func signedDataAlg(msg []byte, alg string) []byte {
	var h []byte
	if alg == "sha256" {
		s := sha256.Sum256(msg)
		h = s[:]
	} else {
		s := sha512.Sum512(msg)
		h = s[:]
	}
	var b []byte
	b = append(b, sigMagic...)
	b = appendString(b, []byte(Namespace))
	b = appendString(b, nil)
	b = appendString(b, []byte(alg))
	return appendString(b, h)
}

func parseSig(armored []byte) (pub ed25519.PublicKey, sig []byte, alg string, err error) {
	blob, err := dearmor(armored, sigBegin, sigEnd)
	if err != nil {
		return nil, nil, "", err
	}
	r := reader{b: blob}
	if string(r.bytes(len(sigMagic))) != sigMagic {
		return nil, nil, "", errors.New("not an SSH signature")
	}
	if v := r.uint32(); v != 1 {
		return nil, nil, "", fmt.Errorf("SSH signature version %d; this knows version 1", v)
	}
	pubWire, ns, _, algB, sigWire := r.string(), r.string(), r.string(), r.string(), r.string()
	if r.err != nil {
		return nil, nil, "", errors.New("the SSH signature is truncated")
	}
	if string(ns) != Namespace {
		return nil, nil, "", fmt.Errorf("signed for %q, not %q", ns, Namespace)
	}
	if alg = string(algB); alg != "sha512" && alg != "sha256" {
		return nil, nil, "", fmt.Errorf("unknown hash %q", alg)
	}
	if pub, err = parseWirePub(pubWire); err != nil {
		return nil, nil, "", err
	}
	sr := reader{b: sigWire}
	if t := sr.string(); string(t) != keyType {
		return nil, nil, "", fmt.Errorf("a %q signature; this checks %s only", t, keyType)
	}
	if sig = sr.string(); sr.err != nil || len(sig) != ed25519.SignatureSize {
		return nil, nil, "", errors.New("the signature itself is the wrong size")
	}
	return pub, sig, alg, nil
}

// MarshalPrivateKey writes key in OpenSSH's unencrypted private key format.
func MarshalPrivateKey(key ed25519.PrivateKey, comment string) []byte {
	pub := key.Public().(ed25519.PublicKey)
	var check [4]byte
	rand.Read(check[:])
	var priv []byte
	priv = append(priv, check[:]...)
	priv = append(priv, check[:]...)
	priv = appendString(priv, []byte(keyType))
	priv = appendString(priv, pub)
	priv = appendString(priv, key) // seed then public key, 64 bytes
	priv = appendString(priv, []byte(comment))
	for i := byte(1); len(priv)%8 != 0; i++ {
		priv = append(priv, i)
	}
	var b []byte
	b = append(b, keyMagic...)
	b = appendString(b, []byte("none")) // cipher
	b = appendString(b, []byte("none")) // kdf
	b = appendString(b, nil)            // kdf options
	b = binary.BigEndian.AppendUint32(b, 1)
	b = appendString(b, wirePub(pub))
	b = appendString(b, priv)
	return armor(keyBegin, keyEnd, b)
}

// ParsePrivateKey reads an unencrypted OpenSSH Ed25519 private key.
func ParsePrivateKey(pem []byte) (ed25519.PrivateKey, error) {
	b, err := dearmor(pem, keyBegin, keyEnd)
	if err != nil {
		return nil, err
	}
	r := reader{b: b}
	if string(r.bytes(len(keyMagic))) != keyMagic {
		return nil, errors.New("not an OpenSSH private key")
	}
	cipher, kdf, _ := r.string(), r.string(), r.string()
	if string(cipher) != "none" || string(kdf) != "none" {
		return nil, errors.New("the key has a passphrase; the hub signs unattended, so it needs one without")
	}
	if n := r.uint32(); n != 1 {
		return nil, fmt.Errorf("%d keys in one file; want 1", n)
	}
	r.string() // the public key, repeated inside
	pr := reader{b: r.string()}
	if r.err != nil {
		return nil, errors.New("the private key is truncated")
	}
	if c1, c2 := pr.uint32(), pr.uint32(); c1 != c2 {
		return nil, errors.New("the private key's check bytes disagree")
	}
	if t := pr.string(); string(t) != keyType {
		return nil, fmt.Errorf("a %q key; this signs with %s only", t, keyType)
	}
	pub, priv := pr.string(), pr.string()
	if pr.err != nil || len(priv) != ed25519.PrivateKeySize || !bytes.Equal(priv[32:], pub) {
		return nil, errors.New("the private key is damaged")
	}
	k := ed25519.NewKeyFromSeed(priv[:32])
	if !bytes.Equal(k.Public().(ed25519.PublicKey), pub) {
		return nil, errors.New("the private key doesn't match its public half")
	}
	return k, nil
}

func wirePub(pub ed25519.PublicKey) []byte {
	return appendString(appendString(nil, []byte(keyType)), pub)
}

func parseWirePub(b []byte) (ed25519.PublicKey, error) {
	r := reader{b: b}
	t, k := r.string(), r.string()
	if r.err != nil || string(t) != keyType || len(k) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("not an %s public key", keyType)
	}
	return ed25519.PublicKey(k), nil
}

func armor(begin, end string, blob []byte) []byte {
	s := base64.StdEncoding.EncodeToString(blob)
	var b strings.Builder
	b.WriteString(begin + "\n")
	for len(s) > armorCols {
		b.WriteString(s[:armorCols] + "\n")
		s = s[armorCols:]
	}
	b.WriteString(s + "\n" + end + "\n")
	return []byte(b.String())
}

func dearmor(text []byte, begin, end string) ([]byte, error) {
	s := string(text)
	i := strings.Index(s, begin)
	j := strings.Index(s, end)
	if i < 0 || j < i {
		return nil, fmt.Errorf("no %s block", strings.Trim(begin, "- "))
	}
	body := strings.Join(strings.Fields(s[i+len(begin):j]), "")
	b, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return nil, fmt.Errorf("the %s block's base64 is damaged", strings.Trim(begin, "- "))
	}
	return b, nil
}

func appendString(b, s []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(s)))
	return append(b, s...)
}

// reader walks SSH wire format.  The first short read sets err and every
// later read returns nothing, so callers check once at the end.
type reader struct {
	b   []byte
	err error
}

func (r *reader) bytes(n int) []byte {
	if r.err != nil || n < 0 || n > len(r.b) {
		r.err = errors.New("short")
		return nil
	}
	out := r.b[:n]
	r.b = r.b[n:]
	return out
}

func (r *reader) uint32() uint32 {
	b := r.bytes(4)
	if b == nil {
		return 0
	}
	return binary.BigEndian.Uint32(b)
}

func (r *reader) string() []byte {
	n := r.uint32()
	if r.err != nil || int64(n) > int64(len(r.b)) {
		r.err = errors.New("short")
		return nil
	}
	return r.bytes(int(n))
}

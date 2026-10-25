// Package testpki makes a throwaway CA and certificates for tests of the
// link, written as PEM files the way they'd sit in /etc/zawiper/tls.
package testpki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type CA struct {
	Dir    string
	CAFile string // ca.crt
	cert   *x509.Certificate
	key    *ecdsa.PrivateKey
}

// New makes a CA in a fresh temp dir.
func New(t *testing.T) *CA {
	t.Helper()
	key := newKey(t)
	tmpl := &x509.Certificate{
		SerialNumber:          serial(t),
		Subject:               pkix.Name{CommonName: "zawiper test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	c := &CA{Dir: t.TempDir(), cert: cert, key: key}
	c.CAFile = filepath.Join(c.Dir, "ca.crt")
	writePEM(t, c.CAFile, "CERTIFICATE", der)
	return c
}

// Server issues the hub's certificate, good for localhost and 127.0.0.1.
func (c *CA) Server(t *testing.T, name string) (certFile, keyFile string) {
	return c.issue(t, name, &x509.Certificate{
		Subject:     pkix.Name{CommonName: "zaweb"},
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
}

// Client issues a bay's certificate with the given CN, e.g. bay/dock1/a.
func (c *CA) Client(t *testing.T, name, cn string) (certFile, keyFile string) {
	return c.issue(t, name, &x509.Certificate{
		Subject:     pkix.Name{CommonName: cn},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
}

func (c *CA) issue(t *testing.T, name string, tmpl *x509.Certificate) (string, string) {
	t.Helper()
	key := newKey(t)
	tmpl.SerialNumber = serial(t)
	tmpl.NotBefore, tmpl.NotAfter = time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	tmpl.KeyUsage = x509.KeyUsageDigitalSignature
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile := filepath.Join(c.Dir, name+".crt")
	keyFile := filepath.Join(c.Dir, name+".key")
	writePEM(t, certFile, "CERTIFICATE", der)
	writePEM(t, keyFile, "EC PRIVATE KEY", kb)
	return certFile, keyFile
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func serial(t *testing.T) *big.Int {
	t.Helper()
	n, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func writePEM(t *testing.T, path, kind string, der []byte) {
	t.Helper()
	b := pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

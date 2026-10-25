package link

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
)

// ClientTLS loads a bay's certificate and the CA the hub's certificate must
// chain to.  It also returns the certificate's CN, bay/<host>/<name>, which
// is who the hub will take this bay to be.
func ClientTLS(caFile, certFile, keyFile string) (*tls.Config, string, error) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, "", fmt.Errorf("can't read the hub's CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, "", fmt.Errorf("%s holds no certificates", caFile)
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, "", fmt.Errorf("can't load this bay's certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, "", err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      pool,
		Certificates: []tls.Certificate{cert},
	}, leaf.Subject.CommonName, nil
}

// ParseCN splits bay/<host>/<name>.
func ParseCN(cn string) (host, name string, err error) {
	parts := strings.Split(cn, "/")
	if len(parts) != 3 || parts[0] != "bay" || parts[1] == "" || parts[2] == "" {
		return "", "", fmt.Errorf("certificate CN %q isn't bay/<host>/<name>", cn)
	}
	return parts[1], parts[2], nil
}

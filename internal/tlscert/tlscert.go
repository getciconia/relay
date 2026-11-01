// Package tlscert gives the tracker port its TLS certificate: the user's own
// (a certificate and key file, picked up again when they change, e.g. after
// a renewal), or a self-signed one the relay makes once and keeps next to its
// database. A self-signed certificate works for trackers that are given it
// as their trusted CA, which is how Teltonika trackers are usually set up.
package tlscert

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Source provides the certificate for every TLS handshake.
type Source struct {
	certFile, keyFile string
	selfSigned        bool

	mu      sync.Mutex
	cert    *tls.Certificate
	modTime time.Time
}

// Info describes the certificate in use, for the panel.
type Info struct {
	SelfSigned  bool
	Subject     string
	Names       []string
	NotAfter    time.Time
	Fingerprint string // SHA-256 of the certificate, "AB:CD:…"
	PEM         []byte // the certificate (never the key)
}

// Load uses certFile and keyFile when both are set; otherwise a self-signed
// certificate in dir (tracker-tls.crt / tracker-tls.key), made on first use
// for hosts (names or IP addresses the trackers use).
func Load(certFile, keyFile, dir string, hosts []string) (*Source, error) {
	s := &Source{certFile: certFile, keyFile: keyFile}
	if certFile == "" || keyFile == "" {
		if certFile != "" || keyFile != "" {
			return nil, errors.New("set both the TLS certificate and the key file, or neither")
		}
		s.selfSigned = true
		s.certFile = filepath.Join(dir, "tracker-tls.crt")
		s.keyFile = filepath.Join(dir, "tracker-tls.key")
		if _, err := os.Stat(s.certFile); errors.Is(err, os.ErrNotExist) {
			if err := makeSelfSigned(s.certFile, s.keyFile, hosts); err != nil {
				return nil, err
			}
		}
	}
	if _, err := s.current(); err != nil {
		return nil, err
	}
	return s, nil
}

// Config is the TLS configuration for the tracker listener.
func (s *Source) Config() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return s.current()
		},
	}
}

// current returns the certificate, loading the files again if they changed.
func (s *Source) current() (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := os.Stat(s.certFile)
	if err != nil {
		if s.cert != nil {
			return s.cert, nil // keep serving the last good one
		}
		return nil, err
	}
	if s.cert != nil && st.ModTime().Equal(s.modTime) {
		return s.cert, nil
	}
	cert, err := tls.LoadX509KeyPair(s.certFile, s.keyFile)
	if err != nil {
		if s.cert != nil {
			return s.cert, nil
		}
		return nil, fmt.Errorf("TLS certificate: %w", err)
	}
	if cert.Leaf == nil {
		cert.Leaf, _ = x509.ParseCertificate(cert.Certificate[0])
	}
	s.cert, s.modTime = &cert, st.ModTime()
	return s.cert, nil
}

// Info describes the certificate in use.
func (s *Source) Info() (Info, error) {
	cert, err := s.current()
	if err != nil {
		return Info{}, err
	}
	leaf := cert.Leaf
	sum := sha256.Sum256(leaf.Raw)
	hexSum := strings.ToUpper(hex.EncodeToString(sum[:]))
	pairs := make([]string, 0, len(hexSum)/2)
	for i := 0; i < len(hexSum); i += 2 {
		pairs = append(pairs, hexSum[i:i+2])
	}
	names := append([]string{}, leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		names = append(names, ip.String())
	}
	return Info{
		SelfSigned:  s.selfSigned,
		Subject:     leaf.Subject.CommonName,
		Names:       names,
		NotAfter:    leaf.NotAfter,
		Fingerprint: strings.Join(pairs, ":"),
		PEM:         pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}),
	}, nil
}

// makeSelfSigned writes a 10-year RSA 2048 certificate that is its own CA,
// so a tracker can be given it as the CA to trust. RSA rather than ECDSA:
// it's what tracker modems support most widely.
func makeSelfSigned(certFile, keyFile string, hosts []string) error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Ciconia Relay", Organization: []string{"Ciconia Relay (self-signed)"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	if len(hosts) == 0 {
		hosts = []string{"ciconia-relay"}
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else if h != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(certFile), 0o755); err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		return err
	}
	return os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

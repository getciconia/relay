package tlscert

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSelfSignedIsMadeOnceAndTrustedAsCA(t *testing.T) {
	dir := t.TempDir()
	s, err := Load("", "", dir, []string{"relay.example", "192.0.2.7"})
	if err != nil {
		t.Fatal(err)
	}
	info, err := s.Info()
	if err != nil {
		t.Fatal(err)
	}
	if !info.SelfSigned || len(info.Names) != 2 || len(info.Fingerprint) != 95 || info.NotAfter.Before(time.Now().AddDate(9, 0, 0)) {
		t.Fatalf("info %+v", info)
	}
	if st, _ := os.Stat(filepath.Join(dir, "tracker-tls.key")); st.Mode().Perm() != 0o600 {
		t.Errorf("key file mode %v", st.Mode().Perm())
	}
	again, _ := Load("", "", dir, nil)
	if i2, _ := again.Info(); i2.Fingerprint != info.Fingerprint {
		t.Error("a second start made a new certificate")
	}

	// A client that trusts this certificate as its CA connects.
	ln, err := tls.Listen("tcp", "127.0.0.1:0", s.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			c.Write([]byte{1})
			c.Close()
		}
	}()
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(info.PEM)
	c, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{RootCAs: pool, ServerName: "relay.example"})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	defer c.Close()
	b := make([]byte, 1)
	if _, err := c.Read(b); err != nil || b[0] != 1 {
		t.Errorf("read %v %v", b, err)
	}
	if c.ConnectionState().Version < tls.VersionTLS12 {
		t.Error("below TLS 1.2")
	}
}

func TestOwnCertificateIsReloadedWhenItChanges(t *testing.T) {
	dir := t.TempDir()
	// Make two certificates to use as "the user's own".
	makeSelfSigned(filepath.Join(dir, "a.crt"), filepath.Join(dir, "a.key"), []string{"a.example"})
	makeSelfSigned(filepath.Join(dir, "b.crt"), filepath.Join(dir, "b.key"), []string{"b.example"})
	cert, key := filepath.Join(dir, "own.crt"), filepath.Join(dir, "own.key")
	copyFile(t, filepath.Join(dir, "a.crt"), cert)
	copyFile(t, filepath.Join(dir, "a.key"), key)

	s, err := Load(cert, key, filepath.Join(dir, "unused"), nil)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := s.Info()
	if first.SelfSigned || first.Names[0] != "a.example" {
		t.Fatalf("own certificate %+v", first)
	}
	copyFile(t, filepath.Join(dir, "b.key"), key)
	copyFile(t, filepath.Join(dir, "b.crt"), cert)
	future := time.Now().Add(time.Minute)
	os.Chtimes(cert, future, future)
	if renewed, _ := s.Info(); renewed.Names[0] != "b.example" {
		t.Errorf("not reloaded: %v", renewed.Names)
	}
	if _, err := os.Stat(filepath.Join(dir, "unused")); err == nil {
		t.Error("made a self-signed certificate although one was given")
	}
	if _, err := Load(cert, "", dir, nil); err == nil {
		t.Error("certificate without key accepted")
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

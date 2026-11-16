package server

import (
	"crypto/rand"
	"crypto/rsa"
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

func TestCertificateTLSServerNamePrefersUsableDNSName(t *testing.T) {
	path := writeTestCertificate(t, []string{"localhost", "center.home.arpa"}, []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("192.168.31.176")})

	name, err := certificateTLSServerName(path)
	if err != nil {
		t.Fatal(err)
	}
	if name != "center.home.arpa" {
		t.Fatalf("TLS server name = %q, want center.home.arpa", name)
	}
}

func TestCertificateTLSServerNameUsesNonLoopbackIPWhenNoDNSName(t *testing.T) {
	path := writeTestCertificate(t, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("192.168.31.176")})

	name, err := certificateTLSServerName(path)
	if err != nil {
		t.Fatal(err)
	}
	if name != "192.168.31.176" {
		t.Fatalf("TLS server name = %q, want 192.168.31.176", name)
	}
}

func TestCertificateTLSServerNameRejectsCertificateWithoutRemoteSAN(t *testing.T) {
	path := writeTestCertificate(t, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	if _, err := certificateTLSServerName(path); err == nil {
		t.Fatal("expected certificate without SAN to be rejected")
	}
}

func writeTestCertificate(t *testing.T, dnsNames []string, addresses []net.IP) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "test center"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     dnsNames,
		IPAddresses: addresses,
	}, &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "test center"}}, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "center.crt")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

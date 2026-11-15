package tls

import (
	cryptotls "crypto/tls"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureCertificateCreatesAndReusesPair(t *testing.T) {
	directory := t.TempDir()
	certFile := filepath.Join(directory, "server.crt")
	keyFile := filepath.Join(directory, "server.key")
	if err := EnsureCertificate(certFile, keyFile, "observer.example.lan"); err != nil {
		t.Fatal(err)
	}
	firstCert, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cryptotls.LoadX509KeyPair(certFile, keyFile); err != nil {
		t.Fatal(err)
	}
	if err := EnsureCertificate(certFile, keyFile, "changed.example.lan"); err != nil {
		t.Fatal(err)
	}
	secondCert, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstCert) != string(secondCert) {
		t.Fatal("existing certificate pair was unexpectedly replaced")
	}
}

func TestEnsureCertificateRejectsPartialPair(t *testing.T) {
	directory := t.TempDir()
	certFile := filepath.Join(directory, "server.crt")
	keyFile := filepath.Join(directory, "server.key")
	if err := os.WriteFile(certFile, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureCertificate(certFile, keyFile, "observer.example.lan"); err == nil {
		t.Fatal("expected incomplete certificate pair to be rejected")
	}
}

func TestEnsureCertificateMigratesCompleteSeedPair(t *testing.T) {
	seedDir := t.TempDir()
	seedCert := filepath.Join(seedDir, "server.crt")
	seedKey := filepath.Join(seedDir, "server.key")
	if err := EnsureCertificate(seedCert, seedKey, "observer.example.lan"); err != nil {
		t.Fatal(err)
	}
	destinationDir := t.TempDir()
	destinationCert := filepath.Join(destinationDir, "server.crt")
	destinationKey := filepath.Join(destinationDir, "server.key")
	if err := EnsureCertificateFromSeed(destinationCert, destinationKey, "new.example.lan", seedDir); err != nil {
		t.Fatal(err)
	}
	seedBytes, err := os.ReadFile(seedCert)
	if err != nil {
		t.Fatal(err)
	}
	destinationBytes, err := os.ReadFile(destinationCert)
	if err != nil {
		t.Fatal(err)
	}
	if string(seedBytes) != string(destinationBytes) {
		t.Fatal("seed certificate was not migrated")
	}
	if _, err := cryptotls.LoadX509KeyPair(destinationCert, destinationKey); err != nil {
		t.Fatal(err)
	}
}

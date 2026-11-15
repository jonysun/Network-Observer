package tls

import (
	"crypto/rand"
	"crypto/rsa"
	cryptotls "crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnsureCertificate keeps an existing certificate pair and creates a local
// self-signed pair only when both files are absent. It is intended for the
// Center's writable TLS volume so a plain Compose start is self-contained.
func EnsureCertificate(certFile, keyFile, host string) error {
	return EnsureCertificateFromSeed(certFile, keyFile, host, "")
}

// EnsureCertificateFromSeed migrates a complete certificate pair from seedDir
// before generating a new pair. This keeps upgrades from changing the Center
// identity when an older deployment stored certificates in a bind mount.
func EnsureCertificateFromSeed(certFile, keyFile, host, seedDir string) error {
	certFile = strings.TrimSpace(certFile)
	keyFile = strings.TrimSpace(keyFile)
	if certFile == "" || keyFile == "" {
		return errors.New("TLS certificate and key paths are required")
	}
	certExists := fileExists(certFile)
	keyExists := fileExists(keyFile)
	if certExists && keyExists {
		return nil
	}
	if certExists || keyExists {
		return fmt.Errorf("TLS certificate pair is incomplete: %s and %s must both exist", certFile, keyFile)
	}
	seedDir = strings.TrimSpace(seedDir)
	if seedDir != "" {
		seedCert := filepath.Join(seedDir, filepath.Base(certFile))
		seedKey := filepath.Join(seedDir, filepath.Base(keyFile))
		seedCertExists := fileExists(seedCert)
		seedKeyExists := fileExists(seedKey)
		if seedCertExists != seedKeyExists {
			return fmt.Errorf("TLS seed certificate pair is incomplete: %s and %s must both exist", seedCert, seedKey)
		}
		if seedCertExists && seedKeyExists {
			if err := copyPair(seedCert, seedKey, certFile, keyFile); err != nil {
				return fmt.Errorf("migrate TLS certificate pair from seed: %w", err)
			}
			return nil
		}
	}
	if strings.TrimSpace(host) == "" {
		host = "observer.example.lan"
	}
	if err := os.MkdirAll(filepath.Dir(certFile), 0o755); err != nil {
		return fmt.Errorf("create TLS certificate directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o755); err != nil {
		return fmt.Errorf("create TLS key directory: %w", err)
	}

	privateKey, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return fmt.Errorf("generate TLS private key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return fmt.Errorf("generate TLS certificate serial: %w", err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(825 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	if parsed := net.ParseIP(host); parsed != nil {
		template.IPAddresses = append(template.IPAddresses, parsed)
	} else {
		template.DNSNames = append(template.DNSNames, host)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return fmt.Errorf("create TLS certificate: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return fmt.Errorf("encode TLS private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := atomicWrite(certFile, certPEM, 0o644); err != nil {
		return fmt.Errorf("write TLS certificate: %w", err)
	}
	if err := atomicWrite(keyFile, keyPEM, 0o600); err != nil {
		_ = os.Remove(certFile)
		return fmt.Errorf("write TLS private key: %w", err)
	}
	if _, err := cryptotls.LoadX509KeyPair(certFile, keyFile); err != nil {
		_ = os.Remove(certFile)
		_ = os.Remove(keyFile)
		return fmt.Errorf("validate generated TLS pair: %w", err)
	}
	return nil
}

func copyPair(sourceCert, sourceKey, destinationCert, destinationKey string) error {
	certPEM, err := os.ReadFile(sourceCert)
	if err != nil {
		return fmt.Errorf("read certificate seed: %w", err)
	}
	keyPEM, err := os.ReadFile(sourceKey)
	if err != nil {
		return fmt.Errorf("read private key seed: %w", err)
	}
	if _, err := cryptotls.X509KeyPair(certPEM, keyPEM); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destinationCert), 0o755); err != nil {
		return fmt.Errorf("create TLS certificate directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(destinationKey), 0o755); err != nil {
		return fmt.Errorf("create TLS key directory: %w", err)
	}
	if err := atomicWrite(destinationCert, certPEM, 0o644); err != nil {
		return fmt.Errorf("write migrated TLS certificate: %w", err)
	}
	if err := atomicWrite(destinationKey, keyPEM, 0o600); err != nil {
		_ = os.Remove(destinationCert)
		return fmt.Errorf("write migrated TLS private key: %w", err)
	}
	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".network-observer-tls-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

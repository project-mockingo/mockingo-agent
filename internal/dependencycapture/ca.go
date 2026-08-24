package dependencycapture

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
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
	"sync"
	"time"
)

type CertificateAuthority struct {
	certificate *x509.Certificate
	privateKey  *rsa.PrivateKey
	cacheMu     sync.Mutex
	cache       map[string]tls.Certificate
}

func LoadOrCreateCA(directory string) (*CertificateAuthority, string, error) {
	if directory == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return nil, "", fmt.Errorf("locate user configuration directory: %w", err)
		}
		directory = filepath.Join(configDir, "mockingo", "capture")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, "", fmt.Errorf("create capture CA directory: %w", err)
	}
	_ = os.Chmod(directory, 0700)
	certPath, keyPath := filepath.Join(directory, "ca.crt"), filepath.Join(directory, "ca.key")
	ca, err := loadCA(certPath, keyPath)
	if err == nil {
		return ca, certPath, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	if _, certErr := os.Stat(certPath); certErr == nil {
		return nil, "", errors.New("capture CA private key is missing; restore ca.key or remove ca.crt to generate a new CA")
	}
	if _, keyErr := os.Stat(keyPath); keyErr == nil {
		return nil, "", errors.New("capture CA certificate is missing; restore ca.crt or remove ca.key to generate a new CA")
	}
	certPEM, keyPEM, err := generateCA()
	if err != nil {
		return nil, "", err
	}
	if err := writeNewFile(keyPath, keyPEM, 0600); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, "", fmt.Errorf("write capture CA private key: %w", err)
	}
	if err := writeNewFile(certPath, certPEM, 0644); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, "", fmt.Errorf("write capture CA certificate: %w", err)
	}
	ca, err = loadCA(certPath, keyPath)
	if err != nil {
		return nil, "", err
	}
	return ca, certPath, nil
}

func writeNewFile(path string, value []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	written := false
	defer func() {
		_ = file.Close()
		if !written {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(value); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	written = true
	return nil
}

func generateCA() ([]byte, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return nil, nil, fmt.Errorf("generate capture CA key: %w", err)
	}
	now := time.Now().UTC()
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "Mockingo Local Dependency Capture CA", Organization: []string{"Mockingo Local Development"}},
		NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create capture CA certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("encode capture CA private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

func loadCA(certPath, keyPath string) (*CertificateAuthority, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(keyPath, 0600)
	certBlock, rest := pem.Decode(certPEM)
	if certBlock == nil || len(rest) != 0 || certBlock.Type != "CERTIFICATE" {
		return nil, errors.New("capture CA certificate is invalid")
	}
	certificate, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil || !certificate.IsCA {
		return nil, errors.New("capture CA certificate is invalid")
	}
	keyBlock, rest := pem.Decode(keyPEM)
	if keyBlock == nil || len(rest) != 0 {
		return nil, errors.New("capture CA private key is invalid")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, errors.New("capture CA private key is invalid")
	}
	privateKey, ok := parsed.(*rsa.PrivateKey)
	publicKey, publicOK := certificate.PublicKey.(*rsa.PublicKey)
	if !ok || !publicOK || privateKey.Validate() != nil || privateKey.PublicKey.N.Cmp(publicKey.N) != 0 {
		return nil, errors.New("capture CA certificate and private key do not match")
	}
	return &CertificateAuthority{certificate: certificate, privateKey: privateKey, cache: make(map[string]tls.Certificate)}, nil
}

func (ca *CertificateAuthority) CertificateFor(host string) (tls.Certificate, error) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	ca.cacheMu.Lock()
	defer ca.cacheMu.Unlock()
	if certificate, ok := ca.cache[host]; ok {
		return certificate, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate leaf key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now().UTC()
	notAfter := now.AddDate(0, 1, 0)
	if notAfter.After(ca.certificate.NotAfter) {
		notAfter = ca.certificate.NotAfter
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: host}, NotBefore: now.Add(-time.Hour), NotAfter: notAfter, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.certificate, &key.PublicKey, ca.privateKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create leaf certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("encode leaf key: %w", err)
	}
	certificate, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		return tls.Certificate{}, err
	}
	ca.cache[host] = certificate
	return certificate, nil
}

func randomSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	return serial, nil
}

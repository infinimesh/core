package acme

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("expected missing file")
	}

	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil {
		t.Fatal("expected bad json")
	}

	none := writeACME(t, []Certificate{{
		Domain:      Domain{Main: "api.example.com"},
		Certificate: base64.StdEncoding.EncodeToString([]byte("nope")),
		Key:         base64.StdEncoding.EncodeToString([]byte("nope")),
	}})
	if _, err := Load(none); err == nil || err.Error() != "mqtt certificate not found" {
		t.Fatal(err)
	}

	certPEM, keyPEM := pairPEM(t)
	badCert := writeACME(t, []Certificate{{
		Domain:      Domain{Main: "mqtt.example.com"},
		Certificate: "!!!",
		Key:         base64.StdEncoding.EncodeToString(keyPEM),
	}})
	if _, err := Load(badCert); err == nil {
		t.Fatal("expected bad certificate")
	}

	badKey := writeACME(t, []Certificate{{
		Domain:      Domain{Main: "mqtt.example.com"},
		Certificate: base64.StdEncoding.EncodeToString(certPEM),
		Key:         "!!!",
	}})
	if _, err := Load(badKey); err == nil {
		t.Fatal("expected bad key")
	}

	okPath := writeACME(t, []Certificate{
		{Domain: Domain{Main: "other.example.com"}, Certificate: "eA==", Key: "eA=="},
		{
			Domain:      Domain{Main: "mqtt.example.com"},
			Certificate: base64.StdEncoding.EncodeToString(certPEM),
			Key:         base64.StdEncoding.EncodeToString(keyPEM),
		},
	})
	got, err := Load(okPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Certificate) == 0 {
		t.Fatal("empty certificate")
	}
}

func writeACME(t *testing.T, certs []Certificate) string {
	t.Helper()
	raw, err := json.Marshal(ACME{Letsencrypt: letsencrypt{
		Account:      Account{Email: "a@b.c", PrivateKey: "k"},
		Certificates: certs,
	}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "acme.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func pairPEM(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

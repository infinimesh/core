/*
Copyright © 2018-2023 Infinite Devices GmbH

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func coreRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func TestParseHandshakeTimeout(t *testing.T) {
	got, err := parseHandshakeTimeout("30s")
	if err != nil || got != 30*time.Second {
		t.Fatalf("30s: %v %v", got, err)
	}
	got, err = parseHandshakeTimeout("1m")
	if err != nil || got != time.Minute {
		t.Fatalf("1m: %v %v", got, err)
	}
	got, err = parseHandshakeTimeout("2m")
	if err != nil || got != maxHandshakeTimeout {
		t.Fatalf("2m: %v %v", got, err)
	}
	for _, raw := range []string{"", "30", "0s", "-1s", "2m1s", "3m"} {
		if _, err := parseHandshakeTimeout(raw); err == nil {
			t.Fatalf("%q accepted", raw)
		}
	}
}

func TestPlaintextIsOptIn(t *testing.T) {
	if plaintextRequested("") || plaintextRequested("0") || plaintextRequested("false") {
		t.Fatal("plaintext defaulted on")
	}
	if !plaintextRequested("1") || !plaintextRequested("true") {
		t.Fatal("plaintext opt-in rejected")
	}
	if !strings.Contains(plaintextWarning(), "1883 is plaintext") {
		t.Fatal(plaintextWarning())
	}
}

func TestWaitHandshakeTimesOut(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	cert, err := tls.LoadX509KeyPair(filepath.Join(coreRoot(t), "hack", "server.crt"), filepath.Join(coreRoot(t), "hack", "server.key"))
	if err != nil {
		t.Fatal(err)
	}
	tlsSrv := tls.Server(server, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	start := time.Now()
	err = waitHandshake(tlsSrv, 50*time.Millisecond)
	if err == nil || err.Error() != "handshake failed due to timeout" {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("timeout took %s", time.Since(start))
	}
}

func TestHackECCertIsP256WithLocalhostSAN(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(coreRoot(t), "hack", "server-ec.crt"))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatal("no pem")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if cert.PublicKeyAlgorithm != x509.ECDSA {
		t.Fatalf("algorithm %s", cert.PublicKeyAlgorithm)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		t.Fatalf("curve %T %v", cert.PublicKey, pub)
	}
	if err := cert.VerifyHostname("localhost"); err != nil {
		t.Fatal(err)
	}
	if err := cert.VerifyHostname("127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if cert.IsCA {
		t.Fatal("test cert is a CA")
	}
}

func TestLoadServerCertsECDSABesideRSA(t *testing.T) {
	root := coreRoot(t)
	rsaCert := filepath.Join(root, "hack", "server.crt")
	rsaKey := filepath.Join(root, "hack", "server.key")
	ecCert := filepath.Join(root, "hack", "server-ec.crt")
	ecKey := filepath.Join(root, "hack", "server-ec.key")

	certs, err := loadServerCerts("", rsaCert, rsaKey, ecCert, ecKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(certs) != 2 {
		t.Fatalf("certs %d", len(certs))
	}
	if err := requireECDSA(certs[1]); err != nil {
		t.Fatal(err)
	}
	if certs[0].Leaf != nil && certs[0].Leaf.PublicKeyAlgorithm == x509.ECDSA {
		t.Fatal("first certificate is the ECDSA pair")
	}

	if _, err := loadServerCerts("", rsaCert, rsaKey, ecCert, ""); err == nil {
		t.Fatal("missing key accepted")
	}
	if _, err := loadServerCerts("", rsaCert, rsaKey, "", ecKey); err == nil {
		t.Fatal("missing cert accepted")
	}
	if _, err := loadServerCerts("", rsaCert, rsaKey, rsaCert, rsaKey); err == nil {
		t.Fatal("RSA certificate accepted as ECDSA")
	}

	cfg := serverTLSConfig(certs)
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion %x", cfg.MinVersion)
	}
	if cfg.ClientAuth != tls.RequireAnyClientCert {
		t.Fatalf("ClientAuth %v", cfg.ClientAuth)
	}
}

func TestServerSelectsECDSAOrRSA(t *testing.T) {
	root := coreRoot(t)
	certs, err := loadServerCerts("",
		filepath.Join(root, "hack", "server.crt"), filepath.Join(root, "hack", "server.key"),
		filepath.Join(root, "hack", "server-ec.crt"), filepath.Join(root, "hack", "server-ec.key"))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverTLSConfig(certs))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	device := newDeviceCert(t)

	ecdsaSuites := []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256}
	rsaSuites := []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256}
	if alg := dialServerKey(t, ln, device, ecdsaSuites); alg != x509.ECDSA {
		t.Fatalf("ECDSA client got %s", alg)
	}
	if alg := dialServerKey(t, ln, device, rsaSuites); alg != x509.RSA {
		t.Fatalf("RSA client got %s", alg)
	}
}

func TestTLS11Rejected(t *testing.T) {
	root := coreRoot(t)
	cert, err := tls.LoadX509KeyPair(filepath.Join(root, "hack", "server-ec.crt"), filepath.Join(root, "hack", "server-ec.key"))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverTLSConfig([]tls.Certificate{cert}))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	errc := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			errc <- err
			return
		}
		errc <- waitHandshake(conn.(*tls.Conn), 2*time.Second)
		_ = conn.Close()
	}()
	client, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS10,
		MaxVersion:         tls.VersionTLS11,
		Certificates:       []tls.Certificate{newDeviceCert(t)},
	})
	if err == nil {
		_ = client.Close()
		t.Fatal("TLS 1.1 dial succeeded")
	}
	if err := <-errc; err == nil {
		t.Fatal("server accepted TLS 1.1")
	}
}

func TestResumedSessionKeepsClientFingerprint(t *testing.T) {
	for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		t.Run(tls.VersionName(version), func(t *testing.T) {
			resumeKeepsFingerprint(t, version)
		})
	}
}

func resumeKeepsFingerprint(t *testing.T, version uint16) {
	t.Helper()
	root := coreRoot(t)
	serverCert, err := tls.LoadX509KeyPair(filepath.Join(root, "hack", "server-ec.crt"), filepath.Join(root, "hack", "server-ec.key"))
	if err != nil {
		t.Fatal(err)
	}
	pemBytes, err := os.ReadFile(filepath.Join(root, "hack", "server-ec.crt"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("pool")
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverTLSConfig([]tls.Certificate{serverCert}))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	device := newDeviceCert(t)
	want := getFingerprint(device.Leaf.Raw)
	cache := tls.NewLRUClientSessionCache(4)
	clientCfg := &tls.Config{
		Certificates:       []tls.Certificate{device},
		RootCAs:            pool,
		ServerName:         "localhost",
		ClientSessionCache: cache,
		MinVersion:         version,
		MaxVersion:         version,
	}

	type got struct {
		fp      []byte
		resumed bool
		err     error
	}
	accepted := make(chan got, 2)
	go func() {
		for i := 0; i < 2; i++ {
			raw, err := ln.Accept()
			if err != nil {
				accepted <- got{err: err}
				return
			}
			conn := raw.(*tls.Conn)
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			if err := waitHandshake(conn, 5*time.Second); err != nil {
				accepted <- got{err: err}
				_ = conn.Close()
				return
			}
			fp, err := clientFingerprint(conn)
			resumed := conn.ConnectionState().DidResume
			if _, werr := conn.Write([]byte{1}); werr != nil && err == nil {
				err = werr
			}
			buf := []byte{0}
			if _, rerr := io.ReadFull(conn, buf); rerr != nil && err == nil {
				err = rerr
			}
			_ = conn.Close()
			accepted <- got{fp: fp, resumed: resumed, err: err}
		}
	}()

	var first, second got
	for i := 0; i < 2; i++ {
		conn, err := tls.Dial("tcp", ln.Addr().String(), clientCfg)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(conn, []byte{0}); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
		select {
		case result := <-accepted:
			if result.err != nil {
				t.Fatal(result.err)
			}
			if i == 0 {
				first = result
			} else {
				second = result
			}
		case <-time.After(5 * time.Second):
			t.Fatal("server did not finish")
		}
	}
	if first.resumed {
		t.Fatal("first handshake resumed")
	}
	if !second.resumed {
		t.Fatal("second handshake did not resume")
	}
	if !bytes.Equal(first.fp, want) || !bytes.Equal(second.fp, want) {
		t.Fatalf("fingerprint full %x resume %x want %x", first.fp, second.fp, want)
	}
}

func dialServerKey(t *testing.T, ln net.Listener, device tls.Certificate, suites []uint16) x509.PublicKeyAlgorithm {
	t.Helper()
	errc := make(chan error, 1)
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			errc <- err
			return
		}
		conn := raw.(*tls.Conn)
		err = waitHandshake(conn, 5*time.Second)
		if err == nil {
			_, err = conn.Write([]byte{1})
		}
		_ = conn.Close()
		errc <- err
	}()
	conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
		InsecureSkipVerify: true,
		Certificates:       []tls.Certificate{device},
		MinVersion:         tls.VersionTLS12,
		MaxVersion:         tls.VersionTLS12,
		CipherSuites:       suites,
	})
	if err != nil {
		t.Fatal(err)
	}
	alg := conn.ConnectionState().PeerCertificates[0].PublicKeyAlgorithm
	buf := []byte{0}
	_, _ = io.ReadFull(conn, buf)
	_ = conn.Close()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	return alg
}

func newDeviceCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "device"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

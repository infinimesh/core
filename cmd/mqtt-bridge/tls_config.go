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
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/infinimesh/core/pkg/mqtt/acme"
)

// crypto/tls issues session tickets for 7 days and rotates the key every
// 24 hours. Keys stay in this process. A resumed connection still has the
// client certificate from the first handshake.
var tlsHandshakeTimeout = 30 * time.Second

const maxHandshakeTimeout = 2 * time.Minute

func parseHandshakeTimeout(raw string) (time.Duration, error) {
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("TLS_HANDSHAKE_TIMEOUT: %w", err)
	}
	if d <= 0 {
		return 0, errors.New("TLS_HANDSHAKE_TIMEOUT must be positive")
	}
	if d > maxHandshakeTimeout {
		return 0, fmt.Errorf("TLS_HANDSHAKE_TIMEOUT must be at most %s", maxHandshakeTimeout)
	}
	return d, nil
}

func plaintextRequested(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "t", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func plaintextWarning() string {
	return "MQTT port 1883 is plaintext. Credentials and payloads are visible on the network. Leave MQTT_PLAINTEXT unset to keep it off."
}

func waitHandshake(conn *tls.Conn, timeout time.Duration) error {
	errc := make(chan error, 1)
	go func() {
		errc <- conn.Handshake()
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-errc:
		return err
	case <-timer.C:
		_ = conn.Close()
		return errors.New("handshake failed due to timeout")
	}
}

func loadServerCerts(acmePath, certFile, keyFile, ecCertFile, ecKeyFile string) ([]tls.Certificate, error) {
	var certs []tls.Certificate
	if acmePath != "" {
		cert, err := acme.Load(acmePath)
		if err != nil {
			return nil, err
		}
		certs = append(certs, cert)
	} else {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, err
		}
		certs = append(certs, cert)
	}
	if ecCertFile == "" && ecKeyFile == "" {
		return certs, nil
	}
	if ecCertFile == "" || ecKeyFile == "" {
		return nil, errors.New("TLS_EC_CERT_FILE and TLS_EC_KEY_FILE must both be set")
	}
	ec, err := tls.LoadX509KeyPair(ecCertFile, ecKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load ECDSA certificate: %w", err)
	}
	if err := requireECDSA(ec); err != nil {
		return nil, err
	}
	certs = append(certs, ec)
	return certs, nil
}

func requireECDSA(cert tls.Certificate) error {
	leaf := cert.Leaf
	if leaf == nil {
		if len(cert.Certificate) == 0 {
			return errors.New("TLS_EC_CERT_FILE has no certificate")
		}
		parsed, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return fmt.Errorf("TLS_EC_CERT_FILE: %w", err)
		}
		leaf = parsed
	}
	if leaf.PublicKeyAlgorithm != x509.ECDSA {
		return errors.New("TLS_EC_CERT_FILE is not an ECDSA certificate")
	}
	return nil
}

func serverTLSConfig(certs []tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: certs,
		MinVersion:   tls.VersionTLS12,
		ClientAuth:   tls.RequireAnyClientCert,
	}
}

func clientFingerprint(conn *tls.Conn) ([]byte, error) {
	peers := conn.ConnectionState().PeerCertificates
	if len(peers) == 0 {
		return nil, errors.New("no certificate given")
	}
	return getFingerprint(peers[0].Raw), nil
}

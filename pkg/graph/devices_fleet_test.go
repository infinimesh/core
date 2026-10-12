package graph

import (
	"testing"

	devpb "github.com/infinimesh/proto/node/devices"
)

func TestCertificateAcceptsRolloverAndRevocation(t *testing.T) {
	current := []byte{1}
	old := []byte{2}
	cert := &devpb.Certificate{Fingerprint: current, RolloverFingerprint: old}
	if !certificateAccepts(cert, current) || !certificateAccepts(cert, old) {
		t.Fatal("both fingerprints are valid during rollover")
	}
	cert.RevokedFingerprints = [][]byte{old}
	if certificateAccepts(cert, old) {
		t.Fatal("revoked rollover fingerprint was accepted")
	}
	if !certificateAccepts(cert, current) {
		t.Fatal("current fingerprint should stay valid")
	}
	cert.Revoked = true
	if certificateAccepts(cert, current) {
		t.Fatal("revoked certificate was accepted")
	}
}

func TestDiffTags(t *testing.T) {
	added, removed := diffTags([]string{"a", "b"}, []string{"b", "c"})
	if len(added) != 1 || added[0] != "c" || len(removed) != 1 || removed[0] != "a" {
		t.Fatalf("added %v removed %v", added, removed)
	}
}

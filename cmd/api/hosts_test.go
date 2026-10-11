package main

import "testing"

func TestDefaultServiceHosts(t *testing.T) {
	if accountsConnection != "api:8000" {
		t.Fatalf("REGISTRY_HOST default %q", accountsConnection)
	}
	if namespacesConnection != "api:8000" {
		t.Fatalf("NAMESPACES_HOST default %q", namespacesConnection)
	}
}

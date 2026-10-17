package config

import (
	"testing"

	"gopkg.in/yaml.v2"
)

func TestYAML(t *testing.T) {
	var c Config
	raw := []byte("client_id: id\nclient_secret: sec\nredirect_url: http://localhost/cb\nscopes: [read]\nstate: st\napi_url: http://api\nauth_url: http://auth\ntoken_url: http://token\norganization_mapping:\n  acme:\n    ns: ns1\n    level: 2\n")
	if err := yaml.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if c.ClientId != "id" || c.ClientSecret != "sec" || c.RedirectUrl != "http://localhost/cb" {
		t.Fatalf("%+v", c)
	}
	if len(c.Scopes) != 1 || c.Scopes[0] != "read" || c.State != "st" {
		t.Fatalf("%+v", c)
	}
	if c.ApiUrl != "http://api" || c.AuthUrl != "http://auth" || c.TokenUrl != "http://token" {
		t.Fatalf("%+v", c)
	}
	org := c.OrganizationMapping["acme"]
	if org.Namespace != "ns1" || org.Level != 2 {
		t.Fatalf("%+v", org)
	}
}

package credentials

import (
	"os"
	"strings"
	"testing"

	"github.com/infinimesh/proto/node/accounts"
	"github.com/spf13/viper"
)

func TestLDAPConfDefaultIsEmpty(t *testing.T) {
	if os.Getenv("LDAP_CONF") != "" {
		t.Skip()
	}
	got := viper.GetString("LDAP_CONF")
	if got != "" || strings.Contains(got, "/home/") {
		t.Fatalf("LDAP_CONF default %q", got)
	}
	if LDAP_CONFIGURED {
		t.Fatal("LDAP is configured with an empty LDAP_CONF")
	}
}

func TestLDAPAuth(t *testing.T) {
	// TODO: Fix this test
	t.SkipNow()

	t.Log("LDAP Configured", LDAP_CONFIGURED)
	if !LDAP_CONFIGURED {
		t.Fail()
	}

	t.Log("Test Make Credentials with no provider key")
	_, err := MakeCredentials(&accounts.Credentials{
		Type: "ldap", Data: []string{"user"},
	}, log)
	if err == nil {
		t.Fatalf("Expected error but could create credentials with no Provider Key")
	}

	t.Log("Test Make Credentials with wrong provider key")
	_, err = MakeCredentials(&accounts.Credentials{
		Type: "ldap", Data: []string{"user", "unexistent"},
	}, log)
	if err == nil {
		t.Fatalf("Expected error but could create credentials with wrong Provider Key")
	}

	t.Log("Test Make Credentials")
	cred, err := MakeCredentials(&accounts.Credentials{
		Type: "ldap", Data: []string{"user", "local"},
	}, log)
	if err != nil {
		t.Fatalf("Couldn't create credentials: %v", err)
	}

	t.Log("Test Authorize with Wrong Password")
	ok := cred.Authorize("user", "wrongpassword")
	if ok {
		t.Fatalf("Could authorize with wrong credentials")
	}

	t.Log("Test Authorize with Correct Password")
	ok = cred.Authorize("user", "password")
	if !ok {
		t.Fatalf("Couldn't authorize with correct credentials")
	}
}

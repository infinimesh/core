package main

import (
	"encoding/base64"
	"testing"

	"github.com/infinimesh/core/v3/pkg/mqtt/packet"
	"github.com/stretchr/testify/assert"
)

func TestVerifyBasicAuth(t *testing.T) {
	user := "user"
	pass := []byte{0x01, 0x02}
	payload := base64.StdEncoding.EncodeToString(pass)
	p := &packet.Connect{
		Username: user,
		Password: payload,
	}
	fp, err := verifyBasicAuth(p)
	assert.NoError(t, err)
	assert.Equal(t, pass, fp)
}

func TestVerifyBasicAuth_MissingFields(t *testing.T) {
	p := &packet.Connect{}
	_, err := verifyBasicAuth(p)
	assert.Error(t, err)
}

func TestGetFingerprint(t *testing.T) {
	data := []byte("hello")
	fp := getFingerprint(data)
	assert.Len(t, fp, 32)
}

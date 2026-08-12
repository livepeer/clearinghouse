package auth0mgmt

import (
	"crypto/rand"
	"encoding/base64"
)

func randomPassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b) + "Aa1!", nil
}

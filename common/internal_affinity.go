package common

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"hash"
	"os"
	"strings"
	"sync"
)

const InternalAffinityHeader = "X-NewAPI-Affinity-Key"

var affinitySecretMu sync.RWMutex

func initializeAffinitySecret() {
	value := strings.TrimSpace(os.Getenv("AFFINITY_SECRET"))
	if value == "" {
		value = CryptoSecret
	}
	affinitySecretMu.Lock()
	affinitySecretValue = value
	affinitySecretMu.Unlock()
}

func affinitySecret() []byte {
	affinitySecretMu.RLock()
	value := affinitySecretValue
	affinitySecretMu.RUnlock()
	if value == "" {
		value = CryptoSecret
	}
	return []byte(value)
}

// NewInternalAffinityHasher returns a keyed streaming digest without exposing
// the affinity secret to relay packages.
func NewInternalAffinityHasher() hash.Hash {
	return hmac.New(sha256.New, affinitySecret())
}

// ReloadInternalAffinitySecret refreshes the cached affinity key during process
// bootstrap; it does not expose the raw key.
func ReloadInternalAffinitySecret() {
	initializeAffinitySecret()
}

func compactAffinityMAC(secret []byte, value string) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)[:16]
}

func SignInternalAffinitySource(source string) string {
	secret := affinitySecret()
	payload := base64.RawURLEncoding.EncodeToString(compactAffinityMAC(secret, source))
	signature := base64.RawURLEncoding.EncodeToString(compactAffinityMAC(secret, "v1."+payload))
	return "v1." + payload + "." + signature
}

func VerifyInternalAffinityHeader(value string) (string, bool) {
	if len(value) > 128 {
		return "", false
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] != "v1" {
		return "", false
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payloadBytes) != 16 {
		return "", false
	}
	signatureBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signatureBytes) != 16 {
		return "", false
	}
	expected := compactAffinityMAC(affinitySecret(), "v1."+parts[1])
	if !hmac.Equal(signatureBytes, expected) {
		return "", false
	}
	return parts[1], true
}

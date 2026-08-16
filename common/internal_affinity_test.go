package common

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInternalAffinityHeaderSignAndVerify(t *testing.T) {
	t.Setenv("AFFINITY_SECRET", "test-affinity-secret")
	ReloadInternalAffinitySecret()

	header := SignInternalAffinitySource("token|model|protocol|source")
	payload, valid := VerifyInternalAffinityHeader(header)

	require.True(t, valid)
	assert.NotEmpty(t, payload)
	assert.Len(t, strings.Split(header, "."), 3)
	_, valid = VerifyInternalAffinityHeader(header + "tampered")
	assert.False(t, valid)
	_, valid = VerifyInternalAffinityHeader("v2." + strings.TrimPrefix(header, "v1."))
	assert.False(t, valid)
	_, valid = VerifyInternalAffinityHeader(strings.Repeat("x", 129))
	assert.False(t, valid)
}

func TestInternalAffinityKeyIsIndependentFromCryptoSecret(t *testing.T) {
	originalCrypto, originalAffinity := CryptoSecret, affinitySecretValue
	t.Cleanup(func() { CryptoSecret, affinitySecretValue = originalCrypto, originalAffinity })
	t.Setenv("AFFINITY_SECRET", "dedicated-affinity-secret")
	ReloadInternalAffinitySecret()
	first := SignInternalAffinitySource("stable-source")
	CryptoSecret = "rotated-crypto-secret"
	second := SignInternalAffinitySource("stable-source")
	assert.Equal(t, first, second)
}

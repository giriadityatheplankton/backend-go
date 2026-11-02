package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseStaticTokens(t *testing.T) {
	tok := "0123456789abcdef01234567"
	got, err := parseStaticTokens(tok + ";svc;t1;orders:write,orders:read|" + tok + "x;svc2;;users:read")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "svc", got[tok].Subject)
	assert.Equal(t, "t1", got[tok].TenantID)
	assert.Equal(t, []string{"orders:write", "orders:read"}, got[tok].Scopes)
	assert.Equal(t, "", got[tok+"x"].TenantID)

	for name, raw := range map[string]string{
		"empty":        "",
		"short token":  "abc;svc;t;s",
		"missing part": tok + ";svc;t",
	} {
		_, err := parseStaticTokens(raw)
		assert.Error(t, err, name)
	}
}

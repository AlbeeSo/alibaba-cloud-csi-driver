package proxy

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMountRequestExtensionsPreserveFutureFieldsAndCanonicalizeKnownFields(t *testing.T) {
	var m MountRequest
	require.NoError(t, json.Unmarshal([]byte(`{"Fstype":"ossfs2","MountFlags":["ro"],"futureOption":{"enabled":true}}`), &m))
	require.Equal(t, "ossfs2", m.Fstype)
	require.Equal(t, []string{"ro"}, m.MountFlags)
	require.Len(t, m.Extensions, 1, "case-insensitive known fields must not become duplicate extensions")
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	require.JSONEq(t, `{"fstype":"ossfs2","mountFlags":["ro"],"futureOption":{"enabled":true}}`, string(raw))
}

func TestMountRequestExtensionsCannotOverrideOmittedKnownFields(t *testing.T) {
	m := MountRequest{Extensions: map[string]json.RawMessage{"FdPassing": json.RawMessage(`true`), "TARGET": json.RawMessage(`"outside"`)}}
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	var decoded MountRequest
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.False(t, decoded.FdPassing)
	require.Empty(t, decoded.Target)
}

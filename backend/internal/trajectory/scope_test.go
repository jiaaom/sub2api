package trajectory

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScopeDecide(t *testing.T) {
	cases := []struct {
		name    string
		scopes  []Scope
		group   int64
		key     int64
		decided bool
	}{
		{name: "nothing configured defaults off", group: 1, key: 2},
		{name: "global off overrides group on",
			scopes: []Scope{{Type: ScopeGlobal, Enabled: false}, {Type: ScopeGroup, ID: 1, Enabled: true}}, group: 1, key: 2},
		{name: "global on group on",
			scopes: []Scope{{Type: ScopeGlobal, Enabled: true}, {Type: ScopeGroup, ID: 1, Enabled: true}}, group: 1, key: 2, decided: true},
		{name: "global on group unset defaults off",
			scopes: []Scope{{Type: ScopeGlobal, Enabled: true}}, group: 1, key: 2},
		{name: "key override on beats group off",
			scopes: []Scope{{Type: ScopeGlobal, Enabled: true}, {Type: ScopeGroup, ID: 1, Enabled: false}, {Type: ScopeAPIKey, ID: 2, Enabled: true}}, group: 1, key: 2, decided: true},
		{name: "key override off beats group on",
			scopes: []Scope{{Type: ScopeGlobal, Enabled: true}, {Type: ScopeGroup, ID: 1, Enabled: true}, {Type: ScopeAPIKey, ID: 2, Enabled: false}}, group: 1, key: 2},
		{name: "other key override does not leak",
			scopes: []Scope{{Type: ScopeGlobal, Enabled: true}, {Type: ScopeAPIKey, ID: 3, Enabled: true}}, group: 1, key: 2},
		{name: "ungrouped key needs key override",
			scopes: []Scope{{Type: ScopeGlobal, Enabled: true}, {Type: ScopeAPIKey, ID: 2, Enabled: true}}, group: 0, key: 2, decided: true},
		{name: "key override cannot bypass global off",
			scopes: []Scope{{Type: ScopeAPIKey, ID: 2, Enabled: true}}, group: 1, key: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.decided, newScopeSnapshot(tc.scopes).decide(tc.group, tc.key))
		})
	}
}

func TestValidateScope(t *testing.T) {
	require.NoError(t, validateScope(ScopeGlobal, 0))
	require.Error(t, validateScope(ScopeGlobal, 1))
	require.NoError(t, validateScope(ScopeGroup, 5))
	require.Error(t, validateScope(ScopeGroup, 0))
	require.NoError(t, validateScope(ScopeAPIKey, 7))
	require.Error(t, validateScope(ScopeAPIKey, -1))
	require.Error(t, validateScope("user", 1))
}

func TestRecorderActiveRequiresGlobalAndHealthyStorage(t *testing.T) {
	rec := newTestRecorder(t, nil)
	require.False(t, rec.active())
	rec.SetScopes(enabledScopes())
	require.True(t, rec.active())
	rec.fail("test", errTest)
	require.False(t, rec.active(), "degraded storage must stop recording")
	require.True(t, rec.Stats().Degraded)
	require.Contains(t, rec.Stats().DegradedReason, "test")
	var nilRec *Recorder
	require.False(t, nilRec.active())
}

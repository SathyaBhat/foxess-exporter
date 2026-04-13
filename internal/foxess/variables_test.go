package foxess_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sathyabhat/foxess-exporter/internal/foxess"
)

func TestWatchedVariables_NoDuplicates(t *testing.T) {
	seen := make(map[string]int)
	for i, v := range foxess.WatchedVariables {
		if prev, ok := seen[v]; ok {
			t.Errorf("WatchedVariables[%d] = %q is a duplicate of index %d", i, v, prev)
		}
		seen[v] = i
	}
}

func TestWatchedVariables_NotEmpty(t *testing.T) {
	assert.NotEmpty(t, foxess.WatchedVariables, "WatchedVariables must not be empty")
}

// TestWatchedVariables_AllHaveFriendlyName verifies that every variable polled
// by the exporter has a human-readable label defined for Grafana legends.
func TestWatchedVariables_AllHaveFriendlyName(t *testing.T) {
	for _, v := range foxess.WatchedVariables {
		name, ok := foxess.FriendlyName[v]
		assert.True(t, ok, "WatchedVariable %q has no entry in FriendlyName", v)
		assert.NotEmpty(t, name, "FriendlyName[%q] must not be empty", v)
	}
}

// TestFriendlyName_NoOrphanEntries ensures every entry in FriendlyName
// corresponds to a variable that is actually watched.
func TestFriendlyName_NoOrphanEntries(t *testing.T) {
	watchedSet := make(map[string]bool, len(foxess.WatchedVariables))
	for _, v := range foxess.WatchedVariables {
		watchedSet[v] = true
	}
	for k := range foxess.FriendlyName {
		assert.True(t, watchedSet[k],
			"FriendlyName has entry %q that is not in WatchedVariables", k)
	}
}

func TestReportVariables_NoDuplicates(t *testing.T) {
	seen := make(map[string]bool)
	for _, v := range foxess.ReportVariables {
		require.False(t, seen[v], "ReportVariables contains duplicate %q", v)
		seen[v] = true
	}
}

func TestReportVariables_ContainsExpectedVariables(t *testing.T) {
	required := []string{
		"generation",
		"feedin",
		"gridConsumption",
		"chargeEnergyToTal",
		"dischargeEnergyToTal",
	}
	varSet := make(map[string]bool, len(foxess.ReportVariables))
	for _, v := range foxess.ReportVariables {
		varSet[v] = true
	}
	for _, r := range required {
		assert.True(t, varSet[r], "ReportVariables must include %q", r)
	}
}

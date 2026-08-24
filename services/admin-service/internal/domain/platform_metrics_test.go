package domain_test

import (
	"testing"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

func TestMetricRegistryHoldsItsInvariants(t *testing.T) {
	if err := domain.ValidateMetricDefinitions(); err != nil {
		t.Fatalf("ValidateMetricDefinitions: %v", err)
	}
}

// Every card must be able to say what it counts and over which window. A
// number on an operational dashboard whose definition nobody can state is a
// number nobody can act on.
func TestEveryMetricStatesWhatItCountsAndOverWhichWindow(t *testing.T) {
	for _, definition := range domain.MetricDefinitions() {
		if definition.Definition == "" {
			t.Errorf("%s has no stated definition", definition.Key)
		}
		switch definition.Window {
		case domain.MetricWindowInstant, domain.MetricWindowLast24h, domain.MetricWindowCumulative:
		default:
			t.Errorf("%s has an undeclared window %q", definition.Key, definition.Window)
		}
		switch definition.Unit {
		case domain.MetricUnitCount, domain.MetricUnitBytes:
		default:
			t.Errorf("%s has an undeclared unit %q", definition.Key, definition.Unit)
		}
	}
}

func sampleCounters() domain.PlatformCounters {
	return domain.PlatformCounters{
		UsersTotal: 12, UsersActiveNow: 3, UsersActive24h: 7,
		ChannelsActive: 5, GroupsActive: 2, DirectActive: 9,
		Messages24h: 431, CallsActive: 1, Uploads24h: 8,
		FilesBlocked24h: 2, LinksBlocked24h: 4, StorageBytes: 1 << 30,
	}
}

func TestPlatformMetricsCarriesEveryObservedCounter(t *testing.T) {
	metrics := domain.PlatformMetrics(sampleCounters(), true)
	if len(metrics) != len(domain.MetricDefinitions()) {
		t.Fatalf("expected one metric per definition, got %d", len(metrics))
	}
	values := make(map[domain.MetricKey]int64, len(metrics))
	for _, metric := range metrics {
		if !metric.Available {
			t.Errorf("%s should be available", metric.Definition.Key)
		}
		values[metric.Definition.Key] = metric.Value
	}
	expected := map[domain.MetricKey]int64{
		domain.MetricUsersTotal: 12, domain.MetricUsersActiveNow: 3, domain.MetricUsersActive24h: 7,
		domain.MetricChannelsActive: 5, domain.MetricGroupsActive: 2, domain.MetricDirectActive: 9,
		domain.MetricMessages24h: 431, domain.MetricCallsActive: 1, domain.MetricUploads24h: 8,
		domain.MetricFilesBlocked24h: 2, domain.MetricLinksBlocked24h: 4, domain.MetricStorageBytes: 1 << 30,
	}
	for key, want := range expected {
		if values[key] != want {
			t.Errorf("expected %s to be %d, got %d", key, want, values[key])
		}
	}
}

// A failed aggregate must never arrive as zeros. "Nothing happened" and "we
// could not find out" look identical on a card and mean opposite things.
func TestUnavailableCountersAreNotReportedAsZero(t *testing.T) {
	metrics := domain.PlatformMetrics(sampleCounters(), false)
	for _, metric := range metrics {
		if metric.Available {
			t.Fatalf("%s claims to be available after a failed aggregate", metric.Definition.Key)
		}
		if metric.Value != 0 {
			t.Fatalf("%s carries a value it never observed", metric.Definition.Key)
		}
	}
}

// Storage capacity is the one place the issue explicitly forbids an estimate:
// the object store does not report a trustworthy total, so no percentage may
// be derived from one.
func TestStorageReportsUsageWithoutClaimingACapacity(t *testing.T) {
	for _, definition := range domain.MetricDefinitions() {
		if definition.Key != domain.MetricStorageBytes {
			continue
		}
		if definition.Capacity {
			t.Fatal("storage must not claim a known capacity; no trustworthy total exists")
		}
		if definition.Unit != domain.MetricUnitBytes {
			t.Fatalf("storage must be reported in bytes, got %s", definition.Unit)
		}
		return
	}
	t.Fatal("the storage metric is not declared")
}

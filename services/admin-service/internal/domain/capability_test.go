package domain_test

import (
	"testing"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

func TestCapabilitySet_ZeroValueGrantsNothing(t *testing.T) {
	var set domain.CapabilitySet

	if set.Has(domain.CapabilityAuditRead) {
		t.Fatal("the zero value must grant nothing: an unloaded principal is a denied principal")
	}
	if !set.IsEmpty() {
		t.Fatal("expected the zero value to report itself empty")
	}
	if len(set.Effective()) != 0 {
		t.Fatalf("expected no effective capabilities, got %v", set.Effective())
	}
}

func TestCapabilitySet_DeniesWhatWasNotGranted(t *testing.T) {
	set := domain.NewCapabilitySet([]domain.Capability{domain.CapabilityUsersRead})

	if !set.Has(domain.CapabilityUsersRead) {
		t.Fatal("expected the granted capability to be held")
	}
	if set.Has(domain.CapabilityUsersManage) {
		t.Fatal("read must not imply manage")
	}
	if set.Has(domain.CapabilityAuditRead) {
		t.Fatal("an unrelated capability must be denied")
	}
}

func TestCapabilitySet_SuperuserImpliesEveryKnownCapability(t *testing.T) {
	set := domain.NewCapabilitySet([]domain.Capability{domain.CapabilitySuperuser})

	for _, capability := range []domain.Capability{
		domain.CapabilityUsersRead, domain.CapabilityUsersManage,
		domain.CapabilityChannelsRead, domain.CapabilityChannelsManage,
		domain.CapabilitySecurityRead, domain.CapabilitySecurityManage,
		domain.CapabilityIntegrationsRead, domain.CapabilityIntegrationsManage,
		domain.CapabilityInfrastructureRead, domain.CapabilityInfrastructureManage,
		domain.CapabilityAuditRead, domain.CapabilityConfigRead, domain.CapabilityConfigManage,
	} {
		if !set.Has(capability) {
			t.Fatalf("superuser must imply %q", capability)
		}
	}
}

// An unknown capability is the shape a route-guard typo takes. It must be
// refused even for a superuser: otherwise the typo would silently pass for the
// one principal most able to cause damage, and the mistake would never surface.
func TestCapabilitySet_UnknownCapabilityIsRefusedEvenForSuperuser(t *testing.T) {
	superuser := domain.NewCapabilitySet([]domain.Capability{domain.CapabilitySuperuser})
	limited := domain.NewCapabilitySet([]domain.Capability{domain.CapabilityUsersRead})

	if superuser.Has("admin.users.delete-everything") {
		t.Fatal("superuser must not be granted a capability the platform does not define")
	}
	if limited.Has("") {
		t.Fatal("the empty capability must never be granted")
	}
}

// A stored grant this build does not understand must not widen access, and must
// not lock the administrator out of the grants it does understand either.
func TestNewCapabilitySet_DiscardsUnknownGrants(t *testing.T) {
	set := domain.NewCapabilitySet([]domain.Capability{
		domain.CapabilityAuditRead,
		"admin.from.the.future",
	})

	if !set.Has(domain.CapabilityAuditRead) {
		t.Fatal("expected the known grant to survive")
	}
	if set.Has("admin.from.the.future") {
		t.Fatal("expected the unknown grant to be discarded")
	}
	effective := set.Effective()
	if len(effective) != 1 || effective[0] != domain.CapabilityAuditRead {
		t.Fatalf("unknown grants must not reach the payload, got %v", effective)
	}
}

func TestCapabilitySet_EffectiveIsSortedAndDeduplicated(t *testing.T) {
	set := domain.NewCapabilitySet([]domain.Capability{
		domain.CapabilityUsersRead,
		domain.CapabilityAuditRead,
		domain.CapabilityUsersRead,
	})

	effective := set.Effective()
	if len(effective) != 2 {
		t.Fatalf("expected 2 effective capabilities, got %v", effective)
	}
	if effective[0] != domain.CapabilityAuditRead || effective[1] != domain.CapabilityUsersRead {
		t.Fatalf("expected a stable sorted payload, got %v", effective)
	}
}

func TestIsKnownCapability(t *testing.T) {
	if !domain.IsKnownCapability(domain.CapabilityConfigManage) {
		t.Fatal("expected a defined capability to be known")
	}
	if domain.IsKnownCapability("admin.unknown") {
		t.Fatal("expected an undefined capability to be unknown")
	}
}

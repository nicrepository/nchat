package domain

import "sort"

// Capability is a single named administrative permission.
//
// Capabilities are the only unit of administrative authorization in NChat.
// There is deliberately no "is admin" boolean anywhere in this service: being
// a platform administrator is being a principal that holds capabilities, and
// every privileged endpoint names the one it requires.
type Capability string

const (
	// CapabilitySuperuser implies every other capability. It exists so an
	// operator can grant full platform administration without enumerating a
	// list that grows with every future issue; it is not a bypass of the
	// evaluator, which still refuses capabilities it does not know.
	CapabilitySuperuser Capability = "admin.superuser"

	CapabilityUsersRead            Capability = "admin.users.read"
	CapabilityUsersManage          Capability = "admin.users.manage"
	CapabilityChannelsRead         Capability = "admin.channels.read"
	CapabilityChannelsManage       Capability = "admin.channels.manage"
	CapabilitySecurityRead         Capability = "admin.security.read"
	CapabilitySecurityManage       Capability = "admin.security.manage"
	CapabilityIntegrationsRead     Capability = "admin.integrations.read"
	CapabilityIntegrationsManage   Capability = "admin.integrations.manage"
	CapabilityInfrastructureRead   Capability = "admin.infrastructure.read"
	CapabilityInfrastructureManage Capability = "admin.infrastructure.manage"
	CapabilityAuditRead            Capability = "admin.audit.read"
	CapabilityConfigRead           Capability = "admin.config.read"
	CapabilityConfigManage         Capability = "admin.config.manage"
)

// knownCapabilities mirrors the CHECK constraint on
// auth.admin_role_capabilities (migration 000008). The database refuses to
// store anything outside it and this evaluator refuses to honour anything
// outside it, so the two cannot drift into a state where a row grants
// something the code does not understand.
var knownCapabilities = map[Capability]struct{}{
	CapabilitySuperuser:            {},
	CapabilityUsersRead:            {},
	CapabilityUsersManage:          {},
	CapabilityChannelsRead:         {},
	CapabilityChannelsManage:       {},
	CapabilitySecurityRead:         {},
	CapabilitySecurityManage:       {},
	CapabilityIntegrationsRead:     {},
	CapabilityIntegrationsManage:   {},
	CapabilityInfrastructureRead:   {},
	CapabilityInfrastructureManage: {},
	CapabilityAuditRead:            {},
	CapabilityConfigRead:           {},
	CapabilityConfigManage:         {},
}

// IsKnownCapability reports whether the platform defines this capability.
func IsKnownCapability(capability Capability) bool {
	_, ok := knownCapabilities[capability]
	return ok
}

// CapabilitySet is the effective set of capabilities held by a principal.
//
// The zero value is a valid, empty set that grants nothing, which is what
// makes "no principal loaded" and "principal with no roles" the same answer:
// denied.
type CapabilitySet struct {
	held map[Capability]struct{}
}

// NewCapabilitySet builds a set from stored grants, discarding anything the
// platform does not define. Discarding rather than erroring is deliberate: a
// capability this build has never heard of must not widen access, and it must
// not lock an administrator out of the ones it does understand either.
func NewCapabilitySet(granted []Capability) CapabilitySet {
	held := make(map[Capability]struct{}, len(granted))
	for _, capability := range granted {
		if IsKnownCapability(capability) {
			held[capability] = struct{}{}
		}
	}
	return CapabilitySet{held: held}
}

// Has answers the single authorization question this service asks.
//
// Deny by default, in three ways that must all stay true:
//   - a capability the platform does not define is refused, even to a
//     superuser, so a typo in a route guard fails closed instead of matching
//     a stray grant;
//   - an empty or nil set refuses everything;
//   - only an explicit grant, or the superuser grant, allows.
func (s CapabilitySet) Has(capability Capability) bool {
	if !IsKnownCapability(capability) {
		return false
	}
	if _, ok := s.held[capability]; ok {
		return true
	}
	_, superuser := s.held[CapabilitySuperuser]
	return superuser
}

// Effective returns the capabilities the frontend may use to adapt its
// navigation, sorted so the payload is stable across requests.
//
// This is presentation data. A client that edits it changes what its own
// sidebar renders and nothing else: every endpoint re-evaluates Has against
// the set loaded from the database on that request.
func (s CapabilitySet) Effective() []Capability {
	effective := make([]Capability, 0, len(s.held))
	for capability := range s.held {
		effective = append(effective, capability)
	}
	sort.Slice(effective, func(i, j int) bool { return effective[i] < effective[j] })
	return effective
}

// IsEmpty reports whether the set grants nothing at all.
func (s CapabilitySet) IsEmpty() bool {
	return len(s.held) == 0
}

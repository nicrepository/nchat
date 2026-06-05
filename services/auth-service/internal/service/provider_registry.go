package service

import (
	"fmt"
	"sync"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// ProviderRegistry maps identity provider slugs to their OIDCProvider implementations.
// At runtime, only providers whose config was loaded at startup are registered.
type ProviderRegistry struct {
	mu        sync.RWMutex
	providers map[domain.IdentityProviderSlug]OIDCProvider
}

// NewProviderRegistry returns an empty registry.
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		providers: make(map[domain.IdentityProviderSlug]OIDCProvider),
	}
}

// Register associates slug with an OIDCProvider. Passing nil marks the slug as known
// but not yet configured (Resolve will return ErrOIDCMisconfigured).
// Returns an error if slug is not an OIDC slug (e.g. samba_ad is LDAP, not OIDC).
func (r *ProviderRegistry) Register(slug domain.IdentityProviderSlug, p OIDCProvider) error {
	if !domain.IsOIDCSlug(slug) {
		return fmt.Errorf("slug %q is not an OIDC provider: %w", slug, domain.ErrOIDCMisconfigured)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[slug] = p
	return nil
}

// Resolve returns the OIDCProvider registered for slug.
// Returns ErrOIDCDisabled if slug was never registered.
// Returns ErrOIDCMisconfigured if slug is registered but has no provider (nil).
func (r *ProviderRegistry) Resolve(slug domain.IdentityProviderSlug) (OIDCProvider, error) {
	r.mu.RLock()
	p, ok := r.providers[slug]
	r.mu.RUnlock()
	if !ok {
		return nil, domain.ErrOIDCDisabled
	}
	if p == nil {
		return nil, domain.ErrOIDCMisconfigured
	}
	return p, nil
}

// DirectoryProvider is the integration boundary for LDAP/AD-based identity providers.
// SambaAD will implement this interface in a future release; it is intentionally
// not an OIDCProvider.
type DirectoryProvider interface {
	ProviderKind() domain.IdentityProviderKind
}

// SambaADProviderConfig is a placeholder for future SambaAD LDAP integration.
// SambaAD uses LDAP/Kerberos — it is not an OIDC provider.
type SambaADProviderConfig struct {
	LDAPHost string
	BaseDN   string
	BindDN   string
}

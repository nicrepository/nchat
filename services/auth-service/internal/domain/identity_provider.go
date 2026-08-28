package domain

// IdentityProviderKind classifies how an identity provider authenticates users.
type IdentityProviderKind string

const (
	IdentityProviderKindOIDC   IdentityProviderKind = "oidc"
	IdentityProviderKindLDAPAD IdentityProviderKind = "ldap_ad"
)

// IdentityProviderSlug is a stable machine identifier for an identity provider.
type IdentityProviderSlug string

const (
	IdentityProviderSlugKeycloak        IdentityProviderSlug = "keycloak"
	IdentityProviderSlugAzureAD         IdentityProviderSlug = "azure_ad"
	IdentityProviderSlugGoogleWorkspace IdentityProviderSlug = "google_workspace"
	IdentityProviderSlugSambaAD         IdentityProviderSlug = "samba_ad"
)

var oidcSlugs = map[IdentityProviderSlug]struct{}{
	IdentityProviderSlugKeycloak:        {},
	IdentityProviderSlugAzureAD:         {},
	IdentityProviderSlugGoogleWorkspace: {},
}

// IsOIDCSlug reports whether slug corresponds to an OIDC identity provider.
func IsOIDCSlug(slug IdentityProviderSlug) bool {
	_, ok := oidcSlugs[slug]
	return ok
}

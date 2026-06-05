package service

import (
	"context"
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

func TestProviderRegistry_KeycloakResolves(t *testing.T) {
	reg := NewProviderRegistry()
	stub := &stubRegistryProvider{}
	if err := reg.Register(domain.IdentityProviderSlugKeycloak, stub); err != nil {
		t.Fatalf("register keycloak: %v", err)
	}
	got, err := reg.Resolve(domain.IdentityProviderSlugKeycloak)
	if err != nil {
		t.Fatalf("resolve keycloak: %v", err)
	}
	if got != stub {
		t.Fatal("resolved provider is not the registered one")
	}
}

func TestProviderRegistry_UnknownSlugIsDisabled(t *testing.T) {
	reg := NewProviderRegistry()
	_, err := reg.Resolve("unknown_provider")
	if !errors.Is(err, domain.ErrOIDCDisabled) {
		t.Fatalf("expected ErrOIDCDisabled, got %v", err)
	}
}

func TestProviderRegistry_RegisteredNilIsMisconfigured(t *testing.T) {
	reg := NewProviderRegistry()
	if err := reg.Register(domain.IdentityProviderSlugAzureAD, nil); err != nil {
		t.Fatalf("register nil azure_ad: %v", err)
	}
	_, err := reg.Resolve(domain.IdentityProviderSlugAzureAD)
	if !errors.Is(err, domain.ErrOIDCMisconfigured) {
		t.Fatalf("expected ErrOIDCMisconfigured for nil provider, got %v", err)
	}
}

func TestProviderRegistry_SambaADCannotBeRegisteredAsOIDC(t *testing.T) {
	reg := NewProviderRegistry()
	stub := &stubRegistryProvider{}
	err := reg.Register(domain.IdentityProviderSlugSambaAD, stub)
	if err == nil {
		t.Fatal("expected error: samba_ad is not an OIDC provider")
	}
}

func TestProviderRegistry_AzureADDefinitionExistsButRequiresConfig(t *testing.T) {
	_, err := NewAzureADProvider(AzureADProviderConfig{})
	if !errors.Is(err, domain.ErrOIDCMisconfigured) {
		t.Fatalf("empty AzureADProviderConfig: expected ErrOIDCMisconfigured, got %v", err)
	}
}

func TestProviderRegistry_AzureADDefinitionReturnsOIDCProviderForConfig(t *testing.T) {
	provider, err := NewAzureADProvider(AzureADProviderConfig{
		TenantID:    "contoso.onmicrosoft.com",
		ClientID:    "client",
		RedirectURL: "https://auth.example.com/auth/oidc/keycloak/callback",
	})
	if err != nil {
		t.Fatalf("AzureADProviderConfig: expected no error, got %v", err)
	}
	if provider == nil {
		t.Fatal("expected Azure AD OIDC provider")
	}
}

func TestProviderRegistry_GoogleWorkspaceDefinitionExistsButRequiresConfig(t *testing.T) {
	_, err := NewGoogleWorkspaceProvider(GoogleWorkspaceProviderConfig{})
	if !errors.Is(err, domain.ErrOIDCMisconfigured) {
		t.Fatalf("empty GoogleWorkspaceProviderConfig: expected ErrOIDCMisconfigured, got %v", err)
	}
}

func TestProviderRegistry_GoogleWorkspaceDefinitionReturnsOIDCProviderForConfig(t *testing.T) {
	provider, err := NewGoogleWorkspaceProvider(GoogleWorkspaceProviderConfig{
		ClientID:    "client",
		RedirectURL: "https://auth.example.com/auth/oidc/keycloak/callback",
	})
	if err != nil {
		t.Fatalf("GoogleWorkspaceProviderConfig: expected no error, got %v", err)
	}
	if provider == nil {
		t.Fatal("expected Google Workspace OIDC provider")
	}
}

func TestProviderRegistry_DisabledProviderRejected(t *testing.T) {
	reg := NewProviderRegistry()
	_, err := reg.Resolve(domain.IdentityProviderSlugGoogleWorkspace)
	if !errors.Is(err, domain.ErrOIDCDisabled) {
		t.Fatalf("expected ErrOIDCDisabled for unregistered google_workspace, got %v", err)
	}
}

type stubRegistryProvider struct{}

func (s *stubRegistryProvider) AuthorizationURL(_, _, _ string) (string, error) {
	return "", nil
}
func (s *stubRegistryProvider) ExchangeCode(_ context.Context, _, _ string) (oidcTokenSet, error) {
	return oidcTokenSet{}, nil
}
func (s *stubRegistryProvider) ValidateIDToken(_ context.Context, _ string) (domain.OIDCClaims, error) {
	return domain.OIDCClaims{}, nil
}

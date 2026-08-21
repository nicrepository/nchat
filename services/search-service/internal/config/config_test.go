package config

import "testing"

func TestLoadAndValidateRequiresSearchDependencies(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("AUTH_JWT_HMAC_SECRET", "")
	if err := Load().Validate(); err == nil {
		t.Fatal("missing database and JWT secret must fail")
	}
	t.Setenv("DATABASE_URL", "postgres://localhost/nchat")
	t.Setenv("AUTH_JWT_HMAC_SECRET", "12345678901234567890123456789012")
	c := Load()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Port != 8086 || c.AuthJWTIssuer != "nchat-auth" || c.AuthJWTAudience != "nchat-api" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

package domain

import "testing"

const (
	envTestUserID = "11111111-1111-4111-8111-111111111111"
	envTestDev    = Environment("development")
	envTestProd   = Environment("production")
)

func TestRoomNameSeparatesEnvironmentsHoldingIdenticalIDs(t *testing.T) {
	for _, kind := range []ResourceKind{ResourceKindCall, ResourceKindChannel, ResourceKindDM} {
		t.Run(string(kind), func(t *testing.T) {
			dev, err := RoomName(envTestDev, kind, roomTestID)
			if err != nil {
				t.Fatalf("dev room: %v", err)
			}
			prod, err := RoomName(envTestProd, kind, roomTestID)
			if err != nil {
				t.Fatalf("prod room: %v", err)
			}
			if dev == prod {
				t.Fatalf("same id in two environments produced one room: %q", dev)
			}
			wantDev := "development:" + string(kind) + ":aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			wantProd := "production:" + string(kind) + ":aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			if dev != wantDev {
				t.Fatalf("dev room %q, want %q", dev, wantDev)
			}
			if prod != wantProd {
				t.Fatalf("prod room %q, want %q", prod, wantProd)
			}
		})
	}
}

func TestRoomNameNeverEmitsTheUnnamespacedFormat(t *testing.T) {
	room, err := RoomName(envTestProd, ResourceKindCall, roomTestID)
	if err != nil {
		t.Fatalf("room: %v", err)
	}
	if room == "call:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatal("room is still in the pre-namespace format")
	}
	if _, _, err := ParseRoomName(envTestProd, "call:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"); err == nil {
		t.Fatal("a pre-namespace room must not parse; accepting both formats would reopen the boundary")
	}
}

func TestParticipantIdentitySeparatesEnvironmentsForOneUser(t *testing.T) {
	dev, err := ParticipantIdentity(envTestDev, envTestUserID)
	if err != nil {
		t.Fatalf("dev identity: %v", err)
	}
	prod, err := ParticipantIdentity(envTestProd, envTestUserID)
	if err != nil {
		t.Fatalf("prod identity: %v", err)
	}
	if dev == prod {
		t.Fatalf("one user UUID produced one identity in two environments: %q", dev)
	}
	if dev != "development:"+envTestUserID {
		t.Fatalf("dev identity %q", dev)
	}
	if prod != "production:"+envTestUserID {
		t.Fatalf("prod identity %q", prod)
	}
}

func TestParticipantIdentityCanonicalisesAndRejectsInvalidUsers(t *testing.T) {
	identity, err := ParticipantIdentity(envTestProd, "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	if identity != "production:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("identity is not canonical: %q", identity)
	}
	for _, userID := range []string{"", "not-a-uuid", "production:" + envTestUserID} {
		if _, err := ParticipantIdentity(envTestProd, userID); err == nil {
			t.Fatalf("expected %q to be rejected as a user id", userID)
		}
	}
}

func TestParseEnvironmentAcceptsDeploymentValuesAndTrimsWhitespace(t *testing.T) {
	for _, tt := range []struct{ raw, want string }{
		{"development", "development"},
		{"production", "production"},
		{"staging", "staging"},
		{"test", "test"},
		{" production ", "production"},
		{"production\n", "production"},
	} {
		parsed, err := ParseEnvironment(tt.raw)
		if err != nil {
			t.Fatalf("expected %q to be accepted: %v", tt.raw, err)
		}
		if parsed.String() != tt.want {
			t.Fatalf("expected %q, got %q", tt.want, parsed)
		}
	}
}

func TestNamespacedBuildersUseTheTrimmedEnvironment(t *testing.T) {
	room, err := RoomName(Environment(" production "), ResourceKindCall, roomTestID)
	if err != nil {
		t.Fatalf("room: %v", err)
	}
	identity, err := ParticipantIdentity(Environment(" production "), envTestUserID)
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	if room != "production:call:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" ||
		identity != "production:"+envTestUserID {
		t.Fatalf("untrimmed namespace: room=%q identity=%q", room, identity)
	}
	if _, _, err := ParseRoomName(Environment(" production "), room); err != nil {
		t.Fatalf("parse room with trimmed environment: %v", err)
	}
	if _, err := ParseParticipantIdentity(Environment(" production "), identity); err != nil {
		t.Fatalf("parse identity with trimmed environment: %v", err)
	}
}

func TestParseEnvironmentRejectsAnythingThatCouldBreakTheNamespace(t *testing.T) {
	for _, raw := range []string{
		"",
		" ",
		":",
		"prod:evil",       // would forge a second segment
		"production:call", // same, with a plausible shape
		"PROD",            // uppercase is not the deployment's form
		"Production",      //
		"1production",     // must start with a letter
		"-production",     //
		"prod_uction",     // underscore is outside the alphabet
		"prod uction",     // internal whitespace
		"produção",        // non-ASCII
		"a-very-long-environment-name-that-goes-past-the-limit", // > 32
	} {
		if _, err := ParseEnvironment(raw); err == nil {
			t.Fatalf("expected environment %q to be rejected", raw)
		}
	}
}

func TestParseRejectsValuesFromAnotherEnvironment(t *testing.T) {
	devRoom, err := RoomName(envTestDev, ResourceKindCall, roomTestID)
	if err != nil {
		t.Fatalf("dev room: %v", err)
	}
	if _, _, err := ParseRoomName(envTestProd, devRoom); err == nil {
		t.Fatalf("production accepted a development room: %q", devRoom)
	}
	if _, _, err := ParseRoomName(envTestDev, devRoom); err != nil {
		t.Fatalf("development rejected its own room: %v", err)
	}

	devIdentity, err := ParticipantIdentity(envTestDev, envTestUserID)
	if err != nil {
		t.Fatalf("dev identity: %v", err)
	}
	if _, err := ParseParticipantIdentity(envTestProd, devIdentity); err == nil {
		t.Fatalf("production accepted a development identity: %q", devIdentity)
	}
	if _, err := ParseParticipantIdentity(envTestDev, devIdentity); err != nil {
		t.Fatalf("development rejected its own identity: %v", err)
	}
}

func TestParseRoomNameRejectsMalformedRooms(t *testing.T) {
	for _, room := range []string{
		"",
		"production",
		"production:",
		"production:call",
		"production:call:not-a-uuid",
		"production:group:" + roomTestID, // unknown kind
		"production:call:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa:x", // extra segment
		"call:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",              // pre-namespace
		"development:call:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",  // other environment
		"production:call:AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA",   // non-canonical UUID
	} {
		if _, _, err := ParseRoomName(envTestProd, room); err == nil {
			t.Fatalf("expected room %q to be rejected", room)
		}
	}
}

func TestParseParticipantIdentityRejectsMalformedIdentities(t *testing.T) {
	for _, identity := range []string{
		"",
		"production",
		"production:",
		"production:not-a-uuid",
		envTestUserID, // pre-namespace
		"development:" + envTestUserID,
		"production:" + envTestUserID + ":extra",
		"production:11111111-1111-4111-8111-111111111111 ",
	} {
		if _, err := ParseParticipantIdentity(envTestProd, identity); err == nil {
			t.Fatalf("expected identity %q to be rejected", identity)
		}
	}
}

func TestNamespacedBuildersRefuseInvalidEnvironments(t *testing.T) {
	for _, environment := range []Environment{"", ":", "prod:evil", "PROD", " "} {
		if _, err := RoomName(environment, ResourceKindCall, roomTestID); err == nil {
			t.Fatalf("RoomName accepted environment %q", environment)
		}
		if _, err := ParticipantIdentity(environment, envTestUserID); err == nil {
			t.Fatalf("ParticipantIdentity accepted environment %q", environment)
		}
	}
}

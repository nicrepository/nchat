package domain

import "testing"

const roomTestID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"

const roomTestProdEnv Environment = "production"

func TestRoomNameDerivesCanonicalNamesWithoutCollision(t *testing.T) {
	channelRoom, err := RoomName(roomTestProdEnv, ResourceKindChannel, roomTestID)
	if err != nil {
		t.Fatalf("channel room: %v", err)
	}
	dmRoom, err := RoomName(roomTestProdEnv, ResourceKindDM, roomTestID)
	if err != nil {
		t.Fatalf("dm room: %v", err)
	}

	if channelRoom != "production:channel:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("unexpected channel room %q", channelRoom)
	}
	if dmRoom != "production:dm:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("unexpected dm room %q", dmRoom)
	}
	if channelRoom == dmRoom {
		t.Fatal("channel and DM rooms must not collide")
	}
}

func TestRoomNameRejectsUnknownKindAndInvalidUUID(t *testing.T) {
	for _, input := range []struct {
		kind ResourceKind
		id   string
	}{
		{kind: "group", id: roomTestID},
		{kind: ResourceKindChannel, id: "not-a-uuid"},
		{kind: ResourceKindDM, id: ""},
	} {
		if _, err := RoomName(roomTestProdEnv, input.kind, input.id); err == nil {
			t.Fatalf("expected invalid room input to fail: %+v", input)
		}
	}
}

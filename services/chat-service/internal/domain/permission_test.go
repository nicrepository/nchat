package domain_test

import (
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

func activeMember() *domain.WorkspaceMember {
	return &domain.WorkspaceMember{WorkspaceID: "ws-1", UserID: "user-1", Status: domain.MemberStatusActive}
}

func suspendedMember() *domain.WorkspaceMember {
	return &domain.WorkspaceMember{WorkspaceID: "ws-1", UserID: "user-1", Status: domain.MemberStatusSuspended}
}

func TestCanReadChannel_NilWorkspaceMember_Denied(t *testing.T) {
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	if domain.CanReadChannel(nil, nil, ch) {
		t.Fatal("nil workspace member should be denied")
	}
}

func TestCanReadChannel_SuspendedMember_Denied(t *testing.T) {
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	if domain.CanReadChannel(suspendedMember(), nil, ch) {
		t.Fatal("suspended member should be denied")
	}
}

func TestCanReadChannel_ActiveMember_PublicChannel_Allowed(t *testing.T) {
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	if !domain.CanReadChannel(activeMember(), nil, ch) {
		t.Fatal("active member + public channel should be allowed")
	}
}

func TestCanReadChannel_ActiveMember_GeneralChannel_Allowed(t *testing.T) {
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive, IsGeneral: true}
	if !domain.CanReadChannel(activeMember(), nil, ch) {
		t.Fatal("#geral channel should be accessible to any active workspace member")
	}
}

func TestCanReadChannel_ActiveMember_PrivateChannel_NoChannelMembership_Denied(t *testing.T) {
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
	if domain.CanReadChannel(activeMember(), nil, ch) {
		t.Fatal("private channel without channel membership should be denied")
	}
}

func TestCanReadChannel_ActiveMember_PrivateChannel_WithChannelMembership_Allowed(t *testing.T) {
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
	cm := &domain.ChannelMember{ChannelID: "ch-1", UserID: "user-1"}
	if !domain.CanReadChannel(activeMember(), cm, ch) {
		t.Fatal("private channel with channel membership should be allowed")
	}
}

func TestCanWriteChannel_MatchesCanRead(t *testing.T) {
	cases := []struct {
		name string
		wm   *domain.WorkspaceMember
		cm   *domain.ChannelMember
		ch   domain.Channel
	}{
		{"nil member", nil, nil, domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}},
		{"public", activeMember(), nil, domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}},
		{"private no cm", activeMember(), nil, domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}},
		{"private with cm", activeMember(), &domain.ChannelMember{ChannelID: "ch-1", UserID: "user-1"}, domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if domain.CanWriteChannel(tc.wm, tc.cm, tc.ch) != domain.CanReadChannel(tc.wm, tc.cm, tc.ch) {
				t.Fatal("CanWriteChannel must match CanReadChannel")
			}
		})
	}
}

func TestCanReadChannel_CrossWorkspace_Denied(t *testing.T) {
	ch := domain.Channel{ID: "ch-2", WorkspaceID: "ws-2", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}
	if domain.CanReadChannel(activeMember(), nil, ch) {
		t.Fatal("workspace membership must match the channel workspace")
	}
}

func TestCanReadChannel_ArchivedChannel_Denied(t *testing.T) {
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusArchived}
	if domain.CanReadChannel(activeMember(), nil, ch) {
		t.Fatal("archived channel must be denied")
	}
}

func TestCanReadChannel_PrivateGeneral_Denied(t *testing.T) {
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive, IsGeneral: true}
	if domain.CanReadChannel(activeMember(), nil, ch) {
		t.Fatal("general channel must be public")
	}
}

func TestCanReadChannel_PrivateMembershipMustMatchChannelAndUser(t *testing.T) {
	ch := domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}
	for _, cm := range []*domain.ChannelMember{
		{ChannelID: "ch-other", UserID: "user-1"},
		{ChannelID: "ch-1", UserID: "user-other"},
	} {
		if domain.CanReadChannel(activeMember(), cm, ch) {
			t.Fatalf("mismatched channel membership must be denied: %+v", cm)
		}
	}
}

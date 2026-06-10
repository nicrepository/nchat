package domain_test

import (
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

func activeMember() *domain.WorkspaceMember {
	return &domain.WorkspaceMember{Status: domain.MemberStatusActive}
}

func suspendedMember() *domain.WorkspaceMember {
	return &domain.WorkspaceMember{Status: domain.MemberStatusSuspended}
}

func TestCanReadChannel_NilWorkspaceMember_Denied(t *testing.T) {
	ch := domain.Channel{Type: domain.ChannelTypePublic}
	if domain.CanReadChannel(nil, nil, ch) {
		t.Fatal("nil workspace member should be denied")
	}
}

func TestCanReadChannel_SuspendedMember_Denied(t *testing.T) {
	ch := domain.Channel{Type: domain.ChannelTypePublic}
	if domain.CanReadChannel(suspendedMember(), nil, ch) {
		t.Fatal("suspended member should be denied")
	}
}

func TestCanReadChannel_ActiveMember_PublicChannel_Allowed(t *testing.T) {
	ch := domain.Channel{Type: domain.ChannelTypePublic}
	if !domain.CanReadChannel(activeMember(), nil, ch) {
		t.Fatal("active member + public channel should be allowed")
	}
}

func TestCanReadChannel_ActiveMember_GeneralChannel_Allowed(t *testing.T) {
	ch := domain.Channel{Type: domain.ChannelTypePrivate, IsGeneral: true}
	if !domain.CanReadChannel(activeMember(), nil, ch) {
		t.Fatal("#geral channel should be accessible to any active workspace member")
	}
}

func TestCanReadChannel_ActiveMember_PrivateChannel_NoChannelMembership_Denied(t *testing.T) {
	ch := domain.Channel{Type: domain.ChannelTypePrivate}
	if domain.CanReadChannel(activeMember(), nil, ch) {
		t.Fatal("private channel without channel membership should be denied")
	}
}

func TestCanReadChannel_ActiveMember_PrivateChannel_WithChannelMembership_Allowed(t *testing.T) {
	ch := domain.Channel{Type: domain.ChannelTypePrivate}
	cm := &domain.ChannelMember{}
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
		{"nil member", nil, nil, domain.Channel{Type: domain.ChannelTypePublic}},
		{"public", activeMember(), nil, domain.Channel{Type: domain.ChannelTypePublic}},
		{"private no cm", activeMember(), nil, domain.Channel{Type: domain.ChannelTypePrivate}},
		{"private with cm", activeMember(), &domain.ChannelMember{}, domain.Channel{Type: domain.ChannelTypePrivate}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if domain.CanWriteChannel(tc.wm, tc.cm, tc.ch) != domain.CanReadChannel(tc.wm, tc.cm, tc.ch) {
				t.Fatal("CanWriteChannel must match CanReadChannel")
			}
		})
	}
}

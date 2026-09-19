// Package channelmembership holds the canonical rule for who may be added to a
// channel.
//
// It exists because two services now write chat.channel_members from two
// different authorization scopes: chat-service, where the actor is a workspace
// owner/admin/moderator, and admin-service's Admin Console (issue #579), where
// the actor is a platform principal holding admin.channels.manage.
//
// The *actor* half of that decision is genuinely different in the two services
// and is not shared. The *target* half — who is eligible to be added at all —
// must be identical, because it is a fact about the channel and the person and
// not about who is asking. Restating it in the second service is exactly the
// divergence a second copy always produces, so it lives here once and both
// modules embed the same string.
package channelmembership

// EligibleTargetsCTE selects, from a candidate list, the users who may be added
// to a channel.
//
// Bind order, fixed for every consumer:
//
//	$1 workspace id (uuid)
//	$2 channel id   (uuid)
//	$3 candidate user ids (uuid[])
//
// It yields one row per eligible candidate, so a caller compares the row count
// against the length of the candidate list to learn whether *every* requested
// target was eligible — which is what lets an add be all-or-nothing rather than
// silently partial.
//
// Every join is part of the rule, not incidental:
//
//   - the target must be an ACTIVE member of the channel's workspace. Adding
//     somebody to a channel of a workspace they do not belong to would grant
//     reach across a tenant boundary;
//   - the workspace must be active;
//   - the channel must be active. An archived channel does not take new members
//     from either service;
//   - the account must be active and not soft-deleted, so a suspended or erased
//     person is never (re)admitted anywhere.
//
// Guests are deliberately NOT excluded. A guest reaching a channel *is* being
// added to it — that is the only way a guest reaches any channel, #geral
// included — so excluding them here would remove the one path RF-74 gives them.
// See docs/security/rbac-matrix.md.
//
// The candidate list is a bound uuid[]; nothing here is concatenated.
const EligibleTargetsCTE = `
		SELECT wm.user_id
		FROM unnest($3::uuid[]) AS candidate(user_id)
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = $1::uuid
		 AND wm.user_id = candidate.user_id
		 AND wm.status = 'active'
		JOIN chat.workspaces w
		  ON w.id = wm.workspace_id AND w.status = 'active'
		JOIN chat.channels c
		  ON c.id = $2::uuid
		 AND c.workspace_id = wm.workspace_id
		 AND c.status = 'active'
		JOIN auth.users u
		  ON u.id = wm.user_id AND u.status = 'active' AND u.deleted_at IS NULL`

// DefaultChannelRole is the role every administratively added member receives.
//
// Always 'member', never 'moderator': neither service offers a way to create a
// channel moderator, and an add endpoint that could would be a privilege grant
// wearing the shape of a membership change.
const DefaultChannelRole = "member"

// LockChannelSQL is the serialization protocol every membership mutation obeys.
//
// It takes an exclusive row lock on the channel, and it must be the FIRST
// statement of the transaction that changes chat.channel_members — in every
// service, without exception.
//
// Why it exists: `member_count` in the Admin API is contractually the total the
// operation produced. Under READ COMMITTED each transaction counts committed
// rows plus its own writes, so two adds starting from ten members both count
// eleven while the committed result is twelve. One of those answers is wrong
// the moment it is returned. A shared lock does not help — it lets both in.
//
// The lock is per channel, deliberately. Two administrators touching different
// channels never wait for each other; only mutations that could disagree about
// one channel's total are serialized.
//
// Lock order, canonical and the same in both services:
//
//  1. the channel row (this statement)
//  2. the actor's / targets' rows, if the service checks them
//  3. the mutation
//  4. the count, still inside the transaction
//
// Step 4 is what makes the answer true: the count runs after the write and
// before the commit, so it sees every earlier commit plus this transaction's
// own change, and the next transaction in line sees this one.
//
// The order matters beyond correctness of the count. chat-service's
// AddWorkspaceMember locks a workspace_members row and then takes FOR SHARE on
// the #geral channel — the opposite order. No cycle is reachable, because the
// membership paths that lock the channel first either refuse #geral outright
// (chat-service) or never lock a workspace_members row at all (admin-service,
// whose authority is a platform capability). Changing either of those two facts
// means re-checking this.
//
// $1 is the channel id. Returns no row for a channel that does not exist, which
// every caller maps to its own not-found.
const LockChannelSQL = `SELECT id FROM chat.channels WHERE id = $1::uuid FOR UPDATE`

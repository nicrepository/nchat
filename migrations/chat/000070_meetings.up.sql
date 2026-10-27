-- Issue #1052: canonical NChat meetings.
--
-- A meeting created in NChat is its own source of truth: the id is ours, never
-- a calendar provider's or LiveKit's, and room_key is a stable, non-secret name
-- for the logical room — it authorizes nothing.
--
-- starts_at / ends_at are absolute instants; timezone is the civil IANA zone the
-- meeting was scheduled in, kept beside them and never derived from an offset.
-- The service validates the name against the IANA database; the database only
-- bounds its length, because a CHECK cannot consult pg_timezone_names.
--
-- A meeting optionally belongs to one conversation, which is a channel or a DM
-- (the same two-column shape as chat.messages). The composite foreign keys make
-- a conversation from another workspace unrepresentable, whatever the writer.
--
-- The organizer is meetings.organizer_user_id and also the meeting's one
-- participant row with role 'organizer'. The deferred foreign key below, which
-- includes the role, makes the two agree at every commit: that row cannot be
-- deleted, demoted, or be missing, and the partial unique index forbids a
-- second organizer row.
--
-- Blue/Green: additive only. Two new tables; nothing existing changes, so a
-- slot running the previous release never reads or writes them.
BEGIN;

CREATE TABLE chat.meetings (
    id                 UUID        PRIMARY KEY,
    workspace_id       UUID        NOT NULL REFERENCES chat.workspaces (id) ON DELETE CASCADE,
    channel_id         UUID,
    dm_conversation_id UUID,
    organizer_user_id  UUID        NOT NULL,
    -- Constant, so the organizer foreign key can name the participant's role.
    organizer_role     TEXT        NOT NULL GENERATED ALWAYS AS ('organizer') STORED,
    title              TEXT        NOT NULL,
    description        TEXT        NOT NULL DEFAULT '',
    starts_at          TIMESTAMPTZ NOT NULL,
    ends_at            TIMESTAMPTZ NOT NULL,
    timezone           TEXT        NOT NULL,
    status             TEXT        NOT NULL DEFAULT 'scheduled',
    join_policy        TEXT        NOT NULL DEFAULT 'invited_only',
    room_key           TEXT        NOT NULL,
    version            BIGINT      NOT NULL DEFAULT 1,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT meetings_room_key_unique UNIQUE (room_key),
    CONSTRAINT meetings_at_most_one_conversation_check CHECK (
        channel_id IS NULL OR dm_conversation_id IS NULL
    ),
    CONSTRAINT meetings_title_length_check CHECK (char_length(title) BETWEEN 1 AND 120),
    CONSTRAINT meetings_description_length_check CHECK (char_length(description) <= 5000),
    CONSTRAINT meetings_time_range_check CHECK (ends_at > starts_at),
    CONSTRAINT meetings_timezone_length_check CHECK (char_length(timezone) BETWEEN 1 AND 64),
    CONSTRAINT meetings_status_check CHECK (
        status IN ('scheduled', 'in_progress', 'ended', 'cancelled')
    ),
    CONSTRAINT meetings_join_policy_check CHECK (
        join_policy IN ('invited_only', 'conversation_members', 'workspace_members_with_link')
    ),
    CONSTRAINT meetings_conversation_policy_check CHECK (
        join_policy <> 'conversation_members'
        OR channel_id IS NOT NULL
        OR dm_conversation_id IS NOT NULL
    ),
    CONSTRAINT meetings_version_check CHECK (version > 0),
    CONSTRAINT meetings_workspace_channel_fk
        FOREIGN KEY (workspace_id, channel_id)
        REFERENCES chat.channels (workspace_id, id)
        ON DELETE CASCADE,
    CONSTRAINT meetings_workspace_dm_fk
        FOREIGN KEY (workspace_id, dm_conversation_id)
        REFERENCES chat.dm_conversations (workspace_id, id)
        ON DELETE CASCADE
);

-- Support the conversation foreign keys' cascades, and a conversation's
-- meetings for the timeline card (#1057).
CREATE INDEX meetings_workspace_channel_idx
    ON chat.meetings (workspace_id, channel_id)
    WHERE channel_id IS NOT NULL;

CREATE INDEX meetings_workspace_dm_idx
    ON chat.meetings (workspace_id, dm_conversation_id)
    WHERE dm_conversation_id IS NOT NULL;

CREATE TABLE chat.meeting_participants (
    meeting_id UUID        NOT NULL REFERENCES chat.meetings (id) ON DELETE CASCADE,
    user_id    UUID        NOT NULL,
    role       TEXT        NOT NULL,
    rsvp       TEXT        NOT NULL DEFAULT 'pending',
    invited_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (meeting_id, user_id),
    -- Target of meetings_organizer_participant_fk; the primary key already
    -- makes it unique.
    CONSTRAINT meeting_participants_meeting_user_role_unique UNIQUE (meeting_id, user_id, role),
    CONSTRAINT meeting_participants_role_check CHECK (role IN ('organizer', 'cohost', 'attendee')),
    CONSTRAINT meeting_participants_rsvp_check CHECK (
        rsvp IN ('pending', 'accepted', 'declined', 'tentative')
    ),
    CONSTRAINT meeting_participants_organizer_accepted_check CHECK (
        role <> 'organizer' OR rsvp = 'accepted'
    )
);

CREATE UNIQUE INDEX meeting_participants_one_organizer_idx
    ON chat.meeting_participants (meeting_id)
    WHERE role = 'organizer';

ALTER TABLE chat.meetings
    ADD CONSTRAINT meetings_organizer_participant_fk
        FOREIGN KEY (id, organizer_user_id, organizer_role)
        REFERENCES chat.meeting_participants (meeting_id, user_id, role)
        DEFERRABLE INITIALLY DEFERRED;

COMMIT;

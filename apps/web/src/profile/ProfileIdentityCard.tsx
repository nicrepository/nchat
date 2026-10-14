import "./ProfileIdentityCard.css";
import { avatarColorFor } from "../chat/messageDisplay";
import { UserAvatar } from "../chat/UserAvatar";
import { presenceLabel, usePresence } from "../chat/presence";
import PresenceDot from "../chat/PresenceDot";
import type { SelfProfile } from "./profileApi";

interface ProfileIdentityCardProps {
  profile: SelfProfile;
  workspaceId?: string;
  onEdit: () => void;
  onChangePhoto: () => void;
}

export default function ProfileIdentityCard({
  profile,
  workspaceId = "",
  onEdit,
  onChangePhoto,
}: ProfileIdentityCardProps) {
  const presence = usePresence(profile.id);
  return (
    <section className="profile-identity" aria-label="Identidade">
      <div
        className="profile-identity__avatar"
        style={{ color: avatarColorFor(profile.id) }}
        aria-hidden="true"
      >
        <UserAvatar
          userId={profile.id}
          workspaceId={workspaceId}
          displayName={profile.displayName}
          avatarUrl={profile.avatarUrl}
          imageClassName="profile-identity__avatar-img"
        />
      </div>
      <div className="profile-identity__info">
        <h3 className="profile-identity__name">{profile.displayName || "Sem nome"}</h3>
        {profile.jobTitle && <p className="profile-identity__job-title">{profile.jobTitle}</p>}
        <div className="profile-identity__meta">
          <span className="profile-identity__presence">
            <PresenceDot state={presence} inline /> {presenceLabel(presence)}
          </span>
          {profile.timezone && (
            <span data-testid="profile-identity-timezone" className="profile-identity__timezone">
              {profile.timezone}
            </span>
          )}
        </div>
        {profile.customStatus && (
          <p className="profile-identity__custom-status">{profile.customStatus}</p>
        )}
        {profile.bio && <p className="profile-identity__bio">{profile.bio}</p>}
        <div className="profile-identity__actions">
          <button
            type="button"
            className="profile-identity__btn profile-identity__btn--primary"
            onClick={onEdit}
          >
            Editar
          </button>
          <button type="button" className="profile-identity__btn" onClick={onChangePhoto}>
            Trocar foto
          </button>
        </div>
      </div>
    </section>
  );
}

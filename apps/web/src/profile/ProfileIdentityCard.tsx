import "./ProfileIdentityCard.css";
import { avatarColorFor, initialsFrom } from "../chat/messageDisplay";
import { PersonAvatarImage } from "../chat/PersonAvatarImage";
import { presenceLabel, usePresence } from "../chat/presence";
import type { SelfProfile } from "./profileApi";

interface ProfileIdentityCardProps {
  profile: SelfProfile;
  onEdit: () => void;
  onChangePhoto: () => void;
}

export default function ProfileIdentityCard({ profile, onEdit, onChangePhoto }: ProfileIdentityCardProps) {
  const presence = usePresence(profile.id);
  const initials = profile.displayName ? initialsFrom(profile.displayName) : "";

  return (
    <section className="profile-identity" aria-label="Identidade">
      <div className="profile-identity__avatar" style={{ color: avatarColorFor(profile.id) }}>
        <PersonAvatarImage src={profile.avatarUrl} initials={initials} imgClassName="profile-identity__avatar-img" />
      </div>
      <div className="profile-identity__info">
        <h1 className="profile-identity__name">{profile.displayName || "Sem nome"}</h1>
        {profile.jobTitle && <p className="profile-identity__job-title">{profile.jobTitle}</p>}
        <div className="profile-identity__meta">
          <span className="profile-identity__presence">
            <PresenceDotInline state={presence} /> {presenceLabel(presence)}
          </span>
          {profile.timezone && (
            <span data-testid="profile-identity-timezone" className="profile-identity__timezone">
              {profile.timezone}
            </span>
          )}
        </div>
        {profile.customStatus && <p className="profile-identity__custom-status">{profile.customStatus}</p>}
        {profile.bio && <p className="profile-identity__bio">{profile.bio}</p>}
        <div className="profile-identity__actions">
          <button type="button" className="profile-identity__btn profile-identity__btn--primary" onClick={onEdit}>
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

function PresenceDotInline({ state }: { state: string }) {
  return <span className={`profile-identity__presence-dot profile-identity__presence-dot--${state}`} aria-hidden="true" />;
}

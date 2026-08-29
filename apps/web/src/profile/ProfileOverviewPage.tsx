import { useState } from "react";

import "./ProfileOverviewPage.css";
import { refreshSelfProfile, useSelfProfile } from "./selfProfile";
import ProfileIdentityCard from "./ProfileIdentityCard";
import ProfileInfoCard from "./ProfileInfoCard";
import ProfileEditDialog from "./ProfileEditDialog";
import AvatarDialog from "./AvatarDialog";

type OpenDialog = "edit" | "avatar" | null;

export default function ProfileOverviewPage() {
  const self = useSelfProfile();
  const [openDialog, setOpenDialog] = useState<OpenDialog>(null);

  if (self.status === "loading") {
    return (
      <div className="profile-overview" role="status" aria-label="Carregando perfil">
        <span className="profile-overview__loading" />
      </div>
    );
  }

  if (self.status === "error") {
    return (
      <div className="profile-overview profile-overview__error">
        <p>Não foi possível carregar seu perfil.</p>
        <button type="button" onClick={() => refreshSelfProfile()}>
          Tentar novamente
        </button>
      </div>
    );
  }

  const { profile } = self;

  return (
    <div className="profile-overview">
      <header className="profile-overview__header">
        <h2 className="profile-overview__title">Perfil</h2>
        <p className="profile-overview__description">
          Suas informações, disponibilidade e preferências pessoais.
        </p>
      </header>
      <ProfileIdentityCard
        profile={profile}
        onEdit={() => setOpenDialog("edit")}
        onChangePhoto={() => setOpenDialog("avatar")}
      />
      <ProfileInfoCard profile={profile} />
      {openDialog === "edit" && (
        <ProfileEditDialog
          profile={profile}
          onClose={() => setOpenDialog(null)}
          onSaved={() => {
            // the source of truth this page reads is useSelfProfile() — force
            // an unconditional refetch so this page shows exactly what the
            // server persisted, not a second, page-local copy of it.
            refreshSelfProfile();
          }}
        />
      )}
      {openDialog === "avatar" && (
        <AvatarDialog currentAvatarUrl={profile.avatarUrl} onClose={() => setOpenDialog(null)} />
      )}
    </div>
  );
}

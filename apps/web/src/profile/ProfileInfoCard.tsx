import "./ProfileInfoCard.css";
import type { SelfProfile } from "./profileApi";

export default function ProfileInfoCard({ profile }: { profile: SelfProfile }) {
  const rows: Array<{ label: string; value: string }> = [];
  if (profile.jobTitle) rows.push({ label: "Cargo", value: profile.jobTitle });
  if (profile.timezone) rows.push({ label: "Fuso horário", value: profile.timezone });
  if (rows.length === 0) return null;

  return (
    <section className="profile-info" aria-label="Informações">
      <h2 className="profile-info__title">Informações</h2>
      <dl className="profile-info__grid">
        {rows.map((row) => (
          <div className="profile-info__row" key={row.label}>
            <dt className="profile-info__label">{row.label}</dt>
            <dd className="profile-info__value">{row.value}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}

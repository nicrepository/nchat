import type { ConfigSetting } from "../api/configApi";
import { formatConfigValue, validateDraft } from "../lib/configFields";
import ConfigBadges from "./ConfigBadges";

interface ConfigFieldProps {
  setting: ConfigSetting;
  draft: string;
  /** A server-side message for this field, which outranks the local one. */
  serverError?: string;
  /** False when the operator lacks the capability to change anything here. */
  editable: boolean;
  disabled: boolean;
  onChange: (value: string) => void;
}

/**
 * One configuration setting, rendered as whatever it actually is.
 *
 * Three shapes and no fourth: a boolean is a checkbox, a number is a numeric
 * input with its range stated, and anything the platform will not let this
 * console change is read-only prose explaining why. A credential is the
 * strongest case of the third: it renders as configured or not configured, with
 * the rotation procedure named, and there is no field to type into because
 * there is no endpoint that would accept one.
 *
 * Hiding a control is not authorization. The API refuses the same request
 * whether or not this component ever rendered, and an operator who edits the
 * DOM gets a 403.
 */
export default function ConfigField({
  setting,
  draft,
  serverError,
  editable,
  disabled,
  onChange,
}: ConfigFieldProps) {
  const inputID = `config-${setting.key}`;
  const localError = setting.editable && editable ? validateDraft(setting, draft) : null;
  const message = serverError ?? localError;

  return (
    <li className="admin-config" data-testid={`config-${setting.key}`}>
      <div className="admin-config__header">
        <label className="admin-config__label" htmlFor={inputID}>
          {setting.label}
        </label>
        <code className="admin-table__muted">{setting.key}</code>
      </div>
      <p className="admin-config__description">{setting.description}</p>
      <ConfigBadges setting={setting} />

      <ConfigControl
        setting={setting}
        draft={draft}
        inputID={inputID}
        editable={editable}
        disabled={disabled}
        invalid={message !== null && message !== undefined}
        onChange={onChange}
      />

      {message !== null && message !== undefined && (
        <p role="alert" className="admin-alert" data-testid={`config-error-${setting.key}`}>
          {message}
        </p>
      )}
    </li>
  );
}

interface ConfigControlProps {
  setting: ConfigSetting;
  draft: string;
  inputID: string;
  editable: boolean;
  disabled: boolean;
  invalid: boolean;
  onChange: (value: string) => void;
}

/**
 * Which of the three controls this setting gets, decided in one place.
 *
 * A credential is a status and never a field, whatever else is true of it — so
 * it is answered first, and no later branch can reach a value it must not show.
 * A field only appears for a setting the platform can write *and* an operator
 * who may write it; everything else reads.
 */
function ConfigControl({
  setting,
  draft,
  inputID,
  editable,
  disabled,
  invalid,
  onChange,
}: ConfigControlProps) {
  if (setting.sensitive) {
    return <CredentialState setting={setting} />;
  }
  if (setting.editable && editable) {
    return (
      <EditableControl
        setting={setting}
        draft={draft}
        inputID={inputID}
        disabled={disabled}
        invalid={invalid}
        onChange={onChange}
      />
    );
  }
  return <ReadOnlyState setting={setting} editable={editable} />;
}

interface EditableControlProps {
  setting: ConfigSetting;
  draft: string;
  inputID: string;
  disabled: boolean;
  invalid: boolean;
  onChange: (value: string) => void;
}

function EditableControl({
  setting,
  draft,
  inputID,
  disabled,
  invalid,
  onChange,
}: EditableControlProps) {
  const helpID = `${inputID}-help`;
  if (setting.type === "bool") {
    return (
      <>
        <div className="admin-field__row">
          <input
            id={inputID}
            type="checkbox"
            checked={draft === "true"}
            disabled={disabled}
            aria-describedby={helpID}
            onChange={(event) => onChange(event.target.checked ? "true" : "false")}
          />
          <span>{draft === "true" ? "Ativado" : "Desativado"}</span>
        </div>
        <p id={helpID} className="admin-field__help">
          Padrão da plataforma: {formatConfigValue(setting.default)}.
        </p>
      </>
    );
  }
  return (
    <>
      <div className="admin-field__row">
        <input
          id={inputID}
          type="text"
          inputMode="numeric"
          value={draft}
          disabled={disabled}
          aria-describedby={helpID}
          aria-invalid={invalid}
          onChange={(event) => onChange(event.target.value)}
        />
        {setting.unit !== "" && <span className="admin-field__unit">{setting.unit}</span>}
      </div>
      <p id={helpID} className="admin-field__help">
        {setting.min !== null && setting.max !== null && (
          <>
            Entre {setting.min} e {setting.max}.{" "}
          </>
        )}
        Padrão da plataforma: {formatConfigValue(setting.default, setting.unit)}.
        {setting.nullable && " Deixe em branco para não aplicar a regra."}
      </p>
    </>
  );
}

/**
 * A credential's whole visible state: whether one is configured.
 *
 * There is no "mostrar", no "substituir" and no "rotacionar" button, because
 * the platform has no endpoint behind any of them. Credentials arrive from
 * Sealed Secrets, and rotating one is a sealed manifest and a rollout; the
 * console names that procedure instead of offering a control that would have to
 * lie about what it does.
 */
function CredentialState({ setting }: { setting: ConfigSetting }) {
  return (
    <div className="admin-config__state">
      {setting.observable ? (
        <p data-testid={`config-status-${setting.key}`}>
          <strong>{setting.configured === true ? "Configurado" : "Não configurado"}</strong>
          {setting.envVar !== "" && <> · lida de {setting.envVar}</>}
        </p>
      ) : (
        <p data-testid={`config-status-${setting.key}`}>
          <strong>Não observável</strong> · o Secret é montado apenas por {setting.ownerService}.
        </p>
      )}
      <p className="admin-notice">{setting.readOnlyReason}</p>
    </div>
  );
}

function ReadOnlyState({ setting, editable }: { setting: ConfigSetting; editable: boolean }) {
  return (
    <div className="admin-config__state">
      <p data-testid={`config-value-${setting.key}`}>
        <strong>{formatConfigValue(setting.value, setting.unit)}</strong>
        {setting.envVar !== "" && setting.observable && <> · {setting.envVar}</>}
      </p>
      <p className="admin-notice">
        {setting.editable && !editable ? (
          <>
            Somente leitura: falta a permissão <code>{setting.manageCapability}</code>.
          </>
        ) : (
          setting.readOnlyReason
        )}
      </p>
    </div>
  );
}

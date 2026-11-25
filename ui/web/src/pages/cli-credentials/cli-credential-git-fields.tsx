/**
 * CliCredentialGitFields — Phase 5 typed-credential form for the git adapter.
 *
 * Rendered conditionally from CLIUserCredentialsDialog when
 * `binary.adapter_name === "git"`. Three credential types: env (legacy),
 * pat (single token field), ssh_key (textarea). Host scope is required
 * for pat/ssh_key — validated client-side AND server-side; this component
 * only blocks the network call when the field is empty.
 *
 * Secret state reset: when the user switches type, all secret state is
 * cleared via `useEffect([type])` so a token entered for "PAT" cannot leak
 * into the SSH key buffer (or vice versa).
 */
import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";

export type GitCredentialType = "env" | "pat" | "ssh_key" | "github_app";

/** Non-secret + secret inputs for a GitHub App installation credential. */
export interface GitHubAppFields {
  appId: string;
  installationId: string;
  privateKey: string;
  /** Optional comma-separated repo names to downscope the minted token. */
  repositories: string;
  /** Optional comma list of `name:read|write`, e.g. `contents:write,pull_requests:write`. */
  permissions: string;
}

export const emptyGitHubAppFields: GitHubAppFields = {
  appId: "",
  installationId: "",
  privateKey: "",
  repositories: "",
  permissions: "",
};

/** Typed credential types each adapter accepts (mirrors backend CLIPreset.CredentialTypes).
 * Returns null for passthrough binaries, which only support legacy env vars. */
export function typedCredentialTypesFor(adapterName?: string | null): GitCredentialType[] | null {
  switch (adapterName) {
    case "git":
      return ["env", "pat", "ssh_key", "github_app"];
    case "gh":
      return ["env", "pat", "github_app"];
    default:
      return null;
  }
}

/** Coerce a stored credential_type into one the adapter's form supports. */
export function coerceCredentialType(raw: string | null | undefined, allowed: GitCredentialType[]): GitCredentialType {
  const v = (raw ?? "env") as GitCredentialType;
  return allowed.includes(v) ? v : "env";
}

export interface CliCredentialGitFieldsProps {
  type: GitCredentialType;
  onTypeChange: (t: GitCredentialType) => void;
  /** Types offered in the picker. Defaults to the git adapter set. */
  allowedTypes?: GitCredentialType[];
  githubApp: GitHubAppFields;
  onGitHubAppChange: (v: GitHubAppFields) => void;
  hostScope: string;
  onHostScopeChange: (v: string) => void;
  token: string;
  onTokenChange: (v: string) => void;
  privateKey: string;
  onPrivateKeyChange: (v: string) => void;
  /** Backend error_key for inline display (e.g. "git.cred_ssh_passphrase_unsupported"). */
  errorKey?: string;
  /** True iff the row already exists and the user is editing — show "secret set" placeholder. */
  hasExistingSecret?: boolean;
}

export function CliCredentialGitFields({
  type,
  onTypeChange,
  allowedTypes = ["env", "pat", "ssh_key", "github_app"],
  githubApp,
  onGitHubAppChange,
  hostScope,
  onHostScopeChange,
  token,
  onTokenChange,
  privateKey,
  onPrivateKeyChange,
  errorKey,
  hasExistingSecret,
}: CliCredentialGitFieldsProps) {
  const { t } = useTranslation("cli-credentials");

  // Reset all secret state on type switch — prevents a token pasted under
  // "PAT" from being submitted when the user later flips to "SSH key".
  useEffect(() => {
    onTokenChange("");
    onPrivateKeyChange("");
    onGitHubAppChange(emptyGitHubAppFields);
    // intentionally only depend on `type` — the setters are stable across renders
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [type]);

  const passphraseError = errorKey === "git.cred_ssh_passphrase_unsupported";
  const sshKeyError = errorKey === "git.cred_ssh_key_invalid";
  const hostScopeError =
    errorKey === "git.cred_host_scope_required" || errorKey === "git.cred_host_scope_invalid";
  const githubAppError =
    errorKey === "git.cred_github_app_invalid" || errorKey === "git.cred_github_app_incomplete";
  const typeLabels: Record<GitCredentialType, string> = {
    env: t("userCredentials.credentialTypeEnv"),
    pat: t("userCredentials.credentialTypePAT"),
    ssh_key: t("userCredentials.credentialTypeSSH"),
    github_app: t("userCredentials.credentialTypeGitHubApp"),
  };
  const setApp = (patch: Partial<GitHubAppFields>) => onGitHubAppChange({ ...githubApp, ...patch });
  const secretPlaceholder = (fallback: string) =>
    hasExistingSecret ? t("userCredentials.secretMasked") : fallback;

  return (
    <div data-testid="git-credential-fields" className="flex flex-col gap-3">
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="git-cred-type">{t("userCredentials.credentialType")}</Label>
        <select
          id="git-cred-type"
          data-testid="git-cred-type"
          value={type}
          onChange={(e) => onTypeChange(e.target.value as GitCredentialType)}
          className="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-base md:text-sm shadow-xs"
        >
          {allowedTypes.map((v) => (
            <option key={v} value={v}>{typeLabels[v]}</option>
          ))}
        </select>
      </div>

      {/* Env legacy: caller owns env-vars table; render nothing here. */}
      {type !== "env" && (
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="git-cred-host-scope">{t("userCredentials.hostScope")}</Label>
          <Input
            id="git-cred-host-scope"
            data-testid="git-cred-host-scope"
            value={hostScope}
            onChange={(e) => onHostScopeChange(e.target.value)}
            placeholder={t("userCredentials.hostScopePlaceholder")}
            autoComplete="off"
            spellCheck={false}
            aria-invalid={hostScopeError}
          />
          {hostScopeError && (
            <p data-testid="git-cred-host-scope-error" className="text-xs text-destructive">
              {errorKey === "git.cred_host_scope_required"
                ? t("userCredentials.hostScopeRequired")
                : t("userCredentials.hostScopeInvalid")}
            </p>
          )}
        </div>
      )}

      {type === "pat" && (
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="git-cred-token">{t("userCredentials.token")}</Label>
          <Input
            id="git-cred-token"
            data-testid="git-cred-token"
            type="password"
            // Non-standard name discourages browser password managers from
            // offering to save the PAT into the OS credential store.
            name="goclaw-cred-token"
            autoComplete="off"
            spellCheck={false}
            value={token}
            onChange={(e) => onTokenChange(e.target.value)}
            placeholder={
              hasExistingSecret
                ? t("userCredentials.secretMasked")
                : t("userCredentials.tokenPlaceholder")
            }
          />
        </div>
      )}

      {type === "ssh_key" && (
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="git-cred-ssh-key">{t("userCredentials.sshKey")}</Label>
          <Textarea
            id="git-cred-ssh-key"
            data-testid="git-cred-ssh-key"
            rows={10}
            spellCheck={false}
            autoComplete="off"
            value={privateKey}
            onChange={(e) => onPrivateKeyChange(e.target.value)}
            placeholder={
              hasExistingSecret
                ? t("userCredentials.secretMasked")
                : t("userCredentials.sshKeyPlaceholder")
            }
            aria-invalid={passphraseError || sshKeyError}
            className="font-mono text-base md:text-xs"
          />
          {passphraseError && (
            <p data-testid="git-cred-passphrase-error" className="text-xs text-destructive">
              {t("userCredentials.passphraseUnsupported")}
            </p>
          )}
          {sshKeyError && !passphraseError && (
            <p data-testid="git-cred-ssh-key-error" className="text-xs text-destructive">
              {t("userCredentials.sshKeyInvalid")}
            </p>
          )}
        </div>
      )}

      {type === "github_app" && (
        <div data-testid="github-app-fields" className="flex flex-col gap-3">
          <p className="rounded-md border bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
            {t("userCredentials.githubAppHint")}
          </p>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="gh-app-id">{t("userCredentials.githubAppId")}</Label>
              <Input
                id="gh-app-id"
                data-testid="gh-app-id"
                inputMode="numeric"
                autoComplete="off"
                value={githubApp.appId}
                onChange={(e) => setApp({ appId: e.target.value })}
                placeholder={secretPlaceholder("123456")}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="gh-app-installation">{t("userCredentials.githubAppInstallationId")}</Label>
              <Input
                id="gh-app-installation"
                data-testid="gh-app-installation"
                inputMode="numeric"
                autoComplete="off"
                value={githubApp.installationId}
                onChange={(e) => setApp({ installationId: e.target.value })}
                placeholder={secretPlaceholder("78901234")}
              />
            </div>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="gh-app-key">{t("userCredentials.githubAppPrivateKey")}</Label>
            <Textarea
              id="gh-app-key"
              data-testid="gh-app-key"
              rows={8}
              spellCheck={false}
              autoComplete="off"
              value={githubApp.privateKey}
              onChange={(e) => setApp({ privateKey: e.target.value })}
              placeholder={secretPlaceholder("-----BEGIN RSA PRIVATE KEY-----")}
              aria-invalid={githubAppError}
              className="font-mono text-base md:text-xs"
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="gh-app-repos">{t("userCredentials.githubAppRepositories")}</Label>
            <Input
              id="gh-app-repos"
              data-testid="gh-app-repos"
              autoComplete="off"
              spellCheck={false}
              value={githubApp.repositories}
              onChange={(e) => setApp({ repositories: e.target.value })}
              placeholder={t("userCredentials.githubAppRepositoriesPlaceholder")}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="gh-app-perms">{t("userCredentials.githubAppPermissions")}</Label>
            <Input
              id="gh-app-perms"
              data-testid="gh-app-perms"
              autoComplete="off"
              spellCheck={false}
              value={githubApp.permissions}
              onChange={(e) => setApp({ permissions: e.target.value })}
              placeholder="contents:write,pull_requests:write,issues:write"
            />
            <p className="text-xs text-muted-foreground">{t("userCredentials.githubAppScopeHint")}</p>
          </div>
          {githubAppError && (
            <p data-testid="gh-app-error" className="text-xs text-destructive">
              {errorKey === "git.cred_github_app_incomplete"
                ? t("userCredentials.githubAppIncomplete")
                : t("userCredentials.githubAppInvalid")}
            </p>
          )}
        </div>
      )}
    </div>
  );
}

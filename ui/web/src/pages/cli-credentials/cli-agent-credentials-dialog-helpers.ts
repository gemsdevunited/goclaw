import type { CLIEnvEntryResponse, CLIEnvPayload } from "@/types/cli-credential";
import type { ManualEnvEntry } from "./cli-credential-env-vars-section";
import type { GitCredentialType, GitHubAppFields } from "./cli-credential-git-fields";

export function entriesFromEnv(env: Record<string, CLIEnvEntryResponse> | null | undefined): ManualEnvEntry[] {
  if (!env || Object.keys(env).length === 0) return [];
  return Object.entries(env).map(([key, entry]) => ({
    key,
    value: entry.value ?? "",
    kind: entry.kind ?? "sensitive",
  }));
}

export function envPayloadFromEntries(entries: ManualEnvEntry[]): CLIEnvPayload {
  const env: CLIEnvPayload = {};
  for (const entry of entries) {
    const key = entry.key.trim();
    if (key) env[key] = { kind: entry.kind, value: entry.value };
  }
  return env;
}

export type TypedCredentialPayload = {
  credential_type: Exclude<GitCredentialType, "env">;
  host_scope: string;
  blob: Record<string, string>;
};

export type AgentCredentialPayloadResult =
  | { kind: "typed"; payload: TypedCredentialPayload }
  | { kind: "env"; payload: { env: CLIEnvPayload } }
  | { kind: "no_change" }
  | { kind: "error"; errorKey: string };

export type TypedCredentialPayloadResult = Exclude<AgentCredentialPayloadResult, { kind: "env" }>;

/** Build the typed (non-env) PUT payload shared by agent + user credential forms.
 * A typed PUT replaces the whole blob, so on edit an all-empty form means
 * "keep existing secret" and a partially-filled GitHub App form is rejected. */
export function buildTypedCredentialPayload(input: {
  type: Exclude<GitCredentialType, "env">;
  hostScope: string;
  token: string;
  privateKey: string;
  githubApp?: GitHubAppFields;
  hasExistingSecret: boolean;
}): TypedCredentialPayloadResult {
  const { type, hostScope, token, privateKey, githubApp, hasExistingSecret } = input;
  const scope = hostScope.trim();
  if (!scope) return { kind: "error", errorKey: "git.cred_host_scope_required" };
  if (type === "pat") {
    if (!token) return hasExistingSecret ? { kind: "no_change" } : { kind: "error", errorKey: "git.cred_blob_missing_token" };
    return { kind: "typed", payload: { credential_type: "pat", host_scope: scope, blob: { token } } };
  }
  if (type === "github_app") {
    const app: Record<string, string> = {
      client_id: githubApp?.clientId.trim() ?? "",
      app_id: githubApp?.appId.trim() ?? "",
      private_key: githubApp?.privateKey.trim() ?? "",
      owner: githubApp?.owner.trim() ?? "",
      installation_id: githubApp?.installationId.trim() ?? "",
      repositories: githubApp?.repositories.trim() ?? "",
      permissions: githubApp?.permissions.trim() ?? "",
    };
    const anyFilled = Object.values(app).some((v) => v !== "");
    if (!anyFilled && hasExistingSecret) return { kind: "no_change" };
    // Private key + an issuer (client ID preferred, App ID legacy) are the only
    // hard requirements; the installation is auto-resolved server-side.
    if (!app.private_key || (!app.client_id && !app.app_id)) {
      return { kind: "error", errorKey: "git.cred_github_app_incomplete" };
    }
    const blob: Record<string, string> = {};
    for (const [k, v] of Object.entries(app)) {
      if (v) blob[k] = v;
    }
    return { kind: "typed", payload: { credential_type: "github_app", host_scope: scope, blob } };
  }
  if (!privateKey.trim()) return hasExistingSecret ? { kind: "no_change" } : { kind: "error", errorKey: "git.cred_blob_missing_key" };
  return { kind: "typed", payload: { credential_type: "ssh_key", host_scope: scope, blob: { key: privateKey } } };
}

export function buildAgentCredentialPayload(input: {
  /** True when the binary's adapter accepts typed credentials (git, gh). */
  isTyped: boolean;
  type: GitCredentialType;
  hostScope: string;
  token: string;
  privateKey: string;
  githubApp?: GitHubAppFields;
  hasExistingSecret: boolean;
  envEntries: ManualEnvEntry[];
  isNewEntry: boolean;
}): AgentCredentialPayloadResult {
  const { isTyped, type, envEntries, isNewEntry } = input;
  if (isTyped && type !== "env") {
    return buildTypedCredentialPayload({ ...input, type });
  }
  const env = envPayloadFromEntries(envEntries);
  if (isNewEntry && Object.keys(env).length === 0) return { kind: "error", errorKey: "env_required" };
  return { kind: "env", payload: { env } };
}

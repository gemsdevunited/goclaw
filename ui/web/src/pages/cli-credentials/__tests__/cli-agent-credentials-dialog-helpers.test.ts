import { describe, expect, it } from "vitest";
import { buildAgentCredentialPayload, buildTypedCredentialPayload } from "../cli-agent-credentials-dialog-helpers";
import { coerceCredentialType, emptyGitHubAppFields, typedCredentialTypesFor } from "../cli-credential-git-fields";

describe("agent credential payload builder", () => {
  it("builds a PAT payload for git agent credentials", () => {
    const result = buildAgentCredentialPayload({
      isTyped: true,
      type: "pat",
      hostScope: " github.com ",
      token: "ghp_test",
      privateKey: "",
      hasExistingSecret: false,
      envEntries: [],
      isNewEntry: true,
    });

    expect(result).toEqual({
      kind: "typed",
      payload: {
        credential_type: "pat",
        host_scope: "github.com",
        blob: { token: "ghp_test" },
      },
    });
  });

  it("keeps an existing typed secret when edit form has no replacement secret", () => {
    const result = buildAgentCredentialPayload({
      isTyped: true,
      type: "ssh_key",
      hostScope: "git.example.com:2222",
      token: "",
      privateKey: "",
      hasExistingSecret: true,
      envEntries: [],
      isNewEntry: false,
    });

    expect(result).toEqual({ kind: "no_change" });
  });

  it("requires env vars for a new env credential", () => {
    const result = buildAgentCredentialPayload({
      isTyped: false,
      type: "env",
      hostScope: "",
      token: "",
      privateKey: "",
      hasExistingSecret: false,
      envEntries: [],
      isNewEntry: true,
    });

    expect(result).toEqual({ kind: "error", errorKey: "env_required" });
  });

  it("builds a downscoped GitHub App payload", () => {
    const result = buildTypedCredentialPayload({
      type: "github_app",
      hostScope: "github.com",
      token: "",
      privateKey: "",
      githubApp: {
        ...emptyGitHubAppFields,
        clientId: " Iv23liAbC123 ",
        owner: "gemsdevunited",
        privateKey: "-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----",
        repositories: "GemSFastify",
      },
      hasExistingSecret: false,
    });

    expect(result).toEqual({
      kind: "typed",
      payload: {
        credential_type: "github_app",
        host_scope: "github.com",
        blob: {
          client_id: "Iv23liAbC123",
          private_key: "-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----",
          owner: "gemsdevunited",
          repositories: "GemSFastify",
        },
      },
    });
  });

  it("keeps the stored GitHub App when all fields are blank on edit", () => {
    const result = buildTypedCredentialPayload({
      type: "github_app",
      hostScope: "github.com",
      token: "",
      privateKey: "",
      githubApp: emptyGitHubAppFields,
      hasExistingSecret: true,
    });
    expect(result).toEqual({ kind: "no_change" });
  });

  it("rejects a partially filled GitHub App form", () => {
    const result = buildTypedCredentialPayload({
      type: "github_app",
      hostScope: "github.com",
      token: "",
      privateKey: "",
      githubApp: { ...emptyGitHubAppFields, clientId: "Iv23liAbC123" },
      hasExistingSecret: true,
    });
    expect(result).toEqual({ kind: "error", errorKey: "git.cred_github_app_incomplete" });
  });

  it("offers typed credential types per adapter", () => {
    expect(typedCredentialTypesFor("git")).toEqual(["env", "pat", "ssh_key", "github_app"]);
    expect(typedCredentialTypesFor("gh")).toEqual(["env", "pat", "github_app"]);
    expect(typedCredentialTypesFor(undefined)).toBeNull();
    expect(coerceCredentialType("ssh_key", ["env", "pat", "github_app"])).toBe("env");
    expect(coerceCredentialType("github_app", ["env", "pat", "github_app"])).toBe("github_app");
  });
});

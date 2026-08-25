import { describe, it, expect } from "vitest";
import { formatUserLabel } from "./format-user-label";

describe("formatUserLabel", () => {
  it("returns empty string for empty input", () => {
    expect(formatUserLabel("")).toBe("");
  });

  it("returns display_name from contact resolver when present", () => {
    const resolve = (_id: string) => ({ display_name: "Alice", username: "ali" });
    expect(formatUserLabel("u1", resolve)).toBe("Alice");
  });

  it("returns @username from contact resolver when no display_name", () => {
    const resolve = (_id: string) => ({ username: "ali" });
    expect(formatUserLabel("u1", resolve)).toBe("@ali");
  });

  it("returns 'System' for the system user", () => {
    expect(formatUserLabel("system")).toBe("System");
  });

  it("formats group identifiers as 'Channel topic'", () => {
    expect(formatUserLabel("group:telegram:-100123:topic:42")).toBe("Telegram -100123:topic:42");
  });

  it("prefixes numeric IDs with #", () => {
    expect(formatUserLabel("12345")).toBe("#12345");
  });

  describe("emails", () => {
    it("shows full email when short", () => {
      expect(formatUserLabel("a@b.co")).toBe("a@b.co");
    });

    it("shows full email even when long — never truncate human-readable emails in the middle", () => {
      const email = "thaongocrr.thanhnhien@gmail.com";
      expect(formatUserLabel(email)).toBe(email);
    });

    it("shows full email with subdomain", () => {
      const email = "long.name.very.long@subdomain.example.co.uk";
      expect(formatUserLabel(email)).toBe(email);
    });
  });

  describe("long opaque IDs", () => {
    it("keeps short opaque IDs intact", () => {
      expect(formatUserLabel("oc_abc123")).toBe("oc_abc123");
    });

    it("truncates only opaque IDs that exceed the threshold", () => {
      const id = "oc_295eb80d325c976cbeb4a779e2010518";
      // length 36 > 28 → first 12 + … + last 6
      expect(formatUserLabel(id)).toBe("oc_295eb80d3…010518");
    });

    it("does not truncate IDs at exactly 28 chars", () => {
      const id = "a".repeat(28);
      expect(formatUserLabel(id)).toBe(id);
    });
  });
});

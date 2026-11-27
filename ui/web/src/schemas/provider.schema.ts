import { z } from "zod";
import { isValidSlug } from "@/lib/slug";

export function hasClefAccountIdInBase(apiBase?: string): boolean {
  if (!apiBase) return false;
  const trimmed = apiBase.trim();
  if (!trimmed) return false;
  if (!trimmed.includes("/")) return true;
  const idx = trimmed.indexOf("/accounts/");
  if (idx !== -1) {
    const after = trimmed.slice(idx + "/accounts/".length);
    const firstPart = after.split("/")[0]?.trim();
    return !!firstPart;
  }
  return false;
}

export const providerCreateSchema = z
  .object({
    name: z
      .string()
      .min(1, "Required")
      .refine(isValidSlug, "Only lowercase letters, numbers, and hyphens"),
    displayName: z.string().optional(),
    providerType: z.string().min(1, "Required"),
    apiBase: z.string().optional(),
    apiKey: z.string().optional(),
    enabled: z.boolean(),
    // ACP-specific fields
    acpBinary: z.string().optional(),
    acpArgs: z.string().optional(),
    acpIdleTTL: z.string().optional(),
    acpPermMode: z.string().optional(),
    acpWorkDir: z.string().optional(),
    // Clef-specific fields
    clefAccountId: z.string().optional(),
  })
  .refine(
    (data) => {
      if (data.providerType === "cloudflare_clef") {
        return !!(data.clefAccountId?.trim() || hasClefAccountIdInBase(data.apiBase));
      }
      return true;
    },
    {
      message: "Cloudflare Account ID is required",
      path: ["clefAccountId"],
    }
  );

export type ProviderCreateFormData = z.infer<typeof providerCreateSchema>;

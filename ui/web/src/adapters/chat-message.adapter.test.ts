import { describe, expect, it } from "vitest";
import { transformHistoryMessages } from "./chat-message.adapter";
import type { Message } from "@/types/session";

describe("transformHistoryMessages", () => {
  it("moves legacy tagged thinking from assistant content into thinking", () => {
    const messages: Message[] = [
      { role: "assistant", content: "Visible <thinking>hidden reasoning</thinking> answer" },
    ];

    const [msg] = transformHistoryMessages(messages);

    expect(msg?.content).toBe("Visible  answer");
    expect(msg?.thinking).toBe("hidden reasoning");
  });

  it("preserves existing thinking and appends tagged thinking", () => {
    const messages: Message[] = [
      { role: "assistant", content: "Visible <think>tagged</think> answer", thinking: "native" },
    ];

    const [msg] = transformHistoryMessages(messages);

    expect(msg?.content).toBe("Visible  answer");
    expect(msg?.thinking).toBe("native\ntagged");
  });

  describe("media_refs conversion (regression: user-attached images)", () => {
    it("converts assistant media_refs to mediaItems", () => {
      const messages: Message[] = [
        {
          role: "assistant",
          content: "Here is the result",
          media_refs: [
            { id: "m1", mime_type: "image/png", kind: "image", path: "/app/workspace/out.png", prompt: "leopard pattern" },
          ],
        },
      ];

      const [msg] = transformHistoryMessages(messages);

      expect(msg?.mediaItems).toEqual([
        {
          path: "/v1/files/out.png",
          mimeType: "image/png",
          fileName: "out.png",
          kind: "image",
          prompt: "leopard pattern",
        },
      ]);
    });

    it("converts USER media_refs to mediaItems — attached uploads must show in gallery", () => {
      // Before this fix, the conversion was gated on `m.role === "assistant"`,
      // so the user-attached image only showed the "Image attached" badge
      // (from <media:image> tag parser) but never rendered the actual file.
      const messages: Message[] = [
        {
          role: "user",
          content: '<media:image>\nredesign to leopard pattern',
          media_refs: [
            { id: "m1", mime_type: "image/jpeg", kind: "image", path: "/app/workspace/uploads/image-19b4e0d5.jpg" },
          ],
        },
      ];

      const [msg] = transformHistoryMessages(messages);

      expect(msg?.mediaItems).toEqual([
        {
          path: "/v1/files/image-19b4e0d5.jpg",
          mimeType: "image/jpeg",
          fileName: "image-19b4e0d5.jpg",
          kind: "image",
          prompt: undefined,
        },
      ]);
    });

    it("falls back to ref.id when path is missing", () => {
      const messages: Message[] = [
        {
          role: "user",
          content: "",
          media_refs: [{ id: "m-fallback", mime_type: "image/jpeg", kind: "image" }],
        },
      ];

      const [msg] = transformHistoryMessages(messages);

      expect(msg?.mediaItems?.[0]?.path).toBe("/v1/files/m-fallback");
    });

    it("does not set mediaItems when media_refs is empty or missing", () => {
      const messages: Message[] = [{ role: "user", content: "no attachment" }];
      const [msg] = transformHistoryMessages(messages);
      expect(msg?.mediaItems).toBeUndefined();
    });
  });
});


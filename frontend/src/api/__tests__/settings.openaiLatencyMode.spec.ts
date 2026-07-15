import { describe, expect, it } from "vitest";

import { normalizeOpenAILatencyMode } from "@/api/admin/settings";

describe("admin settings OpenAI latency mode", () => {
  it("keeps the low-latency mode", () => {
    expect(normalizeOpenAILatencyMode("low_latency")).toBe("low_latency");
  });

  it.each(["compatible", "", "fast", null, undefined])(
    "falls back to compatible for %s",
    (value) => {
      expect(normalizeOpenAILatencyMode(value)).toBe("compatible");
    },
  );
});

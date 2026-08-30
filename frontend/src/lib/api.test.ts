import { describe, expect, it } from "vitest";
import { ApiError } from "../lib/api";

describe("ApiError", () => {
  it("preserves the server error envelope", () => {
    const e = new ApiError(401, "AUTH_INVALID_CREDENTIALS", "Invalid credentials");
    expect(e.status).toBe(401);
    expect(e.code).toBe("AUTH_INVALID_CREDENTIALS");
    expect(e.message).toBe("Invalid credentials");
    expect(e instanceof Error).toBe(true);
  });

  it("can carry a request id for support triage", () => {
    const e = new ApiError(500, "INTERNAL_ERROR", "boom", "req-123");
    expect(e.requestId).toBe("req-123");
  });
});

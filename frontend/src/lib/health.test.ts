import { describe, expect, it } from "vitest";
import { alertTone, hasHealthData, healthLabel, healthTone, serviceTone } from "./health";

describe("healthTone", () => {
  it("maps normalized health states to badge tones", () => {
    expect(healthTone("healthy")).toBe("success");
    expect(healthTone("warning")).toBe("warning");
    expect(healthTone("critical")).toBe("danger");
    expect(healthTone("offline")).toBe("danger");
  });
  it("never fabricates a state for unknown values", () => {
    expect(healthTone("unknown")).toBe("neutral");
    expect(healthTone(undefined)).toBe("neutral");
    expect(healthTone(null)).toBe("neutral");
  });
});

describe("healthLabel", () => {
  it("capitalizes for display", () => {
    expect(healthLabel("healthy")).toBe("Healthy");
    expect(healthLabel(undefined)).toBe("Unknown");
  });
});

describe("serviceTone", () => {
  it("maps service health", () => {
    expect(serviceTone("Running")).toBe("success");
    expect(serviceTone("Failed")).toBe("danger");
    expect(serviceTone("Stopped")).toBe("warning");
    expect(serviceTone("NotInstalled")).toBe("neutral");
  });
});

describe("alertTone", () => {
  it("maps alert lifecycle states", () => {
    expect(alertTone("triggered")).toBe("danger");
    expect(alertTone("acknowledged")).toBe("warning");
    expect(alertTone("resolved")).toBe("success");
  });
});

describe("hasHealthData", () => {
  it("requires evaluated points", () => {
    expect(hasHealthData(undefined)).toBe(false);
    expect(hasHealthData({ state: "unknown", points: [], basis: 0 })).toBe(false);
    expect(
      hasHealthData({
        state: "healthy",
        basis: 4,
        points: [{ component: "cpu", value: 12, state: "healthy" }],
      }),
    ).toBe(true);
  });
});

import { describe, expect, it } from "vitest";
import { passwordStrength } from "../../lib/password";

describe("passwordStrength", () => {
  it("returns -1 for empty input", () => {
    expect(passwordStrength("")).toBe(-1);
  });

  it("scores weak passwords low", () => {
    expect(passwordStrength("password")).toBeLessThanOrEqual(1);
  });

  it("scores a long mixed-class password high", () => {
    expect(passwordStrength("Str0ng-Passw0rd!With-Length")).toBeGreaterThanOrEqual(3);
  });

  it("rewards length on top of class variety", () => {
    const short = passwordStrength("Ab1!");
    const longer = passwordStrength("A better, longer Vault#2024 pass");
    expect(longer).toBeGreaterThan(short);
  });
});

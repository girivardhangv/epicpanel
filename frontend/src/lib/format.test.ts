import { describe, expect, it } from "vitest";
import { formatBytes, formatMbps, formatPercent, formatUptime, rangeToSeconds, timeLabel } from "./format";

describe("formatPercent", () => {
  it("uses friendly precision (spec §23)", () => {
    expect(formatPercent(32.437829)).toBe("32.4%");
    expect(formatPercent(100)).toBe("100.0%");
    expect(formatPercent(0)).toBe("0.0%");
  });
  it("renders a dash for unavailable values", () => {
    expect(formatPercent(null)).toBe("—");
    expect(formatPercent(undefined)).toBe("—");
    expect(formatPercent(NaN)).toBe("—");
  });
});

describe("formatBytes", () => {
  it("scales units", () => {
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(17179869184)).toBe("16.0 GB");
    expect(formatBytes(8589934592)).toBe("8.0 GB");
  });
  it("renders a dash for unavailable values", () => {
    expect(formatBytes(null)).toBe("—");
  });
});

describe("formatMbps", () => {
  it("formats rates with sane precision", () => {
    expect(formatMbps(14.231)).toBe("14.2 Mbps");
    expect(formatMbps(240)).toBe("240 Mbps");
    expect(formatMbps(1500)).toBe("1.50 Gbps");
    expect(formatMbps(null)).toBe("—");
  });
});

describe("formatUptime", () => {
  it("humanizes durations", () => {
    expect(formatUptime(90061)).toBe("1d 1h");
    expect(formatUptime(7320)).toBe("2h 2m");
    expect(formatUptime(120)).toBe("2m");
    expect(formatUptime(null)).toBe("—");
  });
});

describe("rangeToSeconds", () => {
  it("accepts exactly the documented ranges", () => {
    expect(rangeToSeconds("1h")).toBe(3600);
    expect(rangeToSeconds("6h")).toBe(21600);
    expect(rangeToSeconds("24h")).toBe(86400);
    expect(rangeToSeconds("7d")).toBe(604800);
    expect(rangeToSeconds("30d")).toBe(2592000);
  });
  it("rejects unbounded/unknown ranges client-side", () => {
    expect(rangeToSeconds("999d")).toBeNull();
    expect(rangeToSeconds("")).toBeNull();
  });
});

describe("timeLabel", () => {
  it("labels intraday points as clock time", () => {
    const iso = new Date("2026-08-29T14:35:00Z").toISOString();
    expect(timeLabel(iso, 3600)).toMatch(/\d{2}:\d{2}/);
  });
  it("returns empty for invalid timestamps", () => {
    expect(timeLabel("not-a-time", 3600)).toBe("");
  });
});

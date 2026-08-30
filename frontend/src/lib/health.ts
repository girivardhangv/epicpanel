import type { HealthStateView } from "../types/api";

export type HealthTone = "success" | "warning" | "danger" | "neutral" | "info";

/** Maps a normalized health state to the design system badge tone. */
export function healthTone(state: string | undefined | null): HealthTone {
  switch (state) {
    case "healthy":
      return "success";
    case "warning":
      return "warning";
    case "critical":
      return "danger";
    case "offline":
      return "danger";
    default:
      return "neutral"; // unknown — never fabricate
  }
}

export function healthLabel(state: string | undefined | null): string {
  if (!state) return "Unknown";
  return state.charAt(0).toUpperCase() + state.slice(1);
}

/** Service status → badge tone (Running/Stopped/Failed/Unknown/NotInstalled). */
export function serviceTone(status: string): HealthTone {
  switch (status) {
    case "Running":
      return "success";
    case "Failed":
      return "danger";
    case "Stopped":
      return "warning";
    default:
      return "neutral";
  }
}

/** Alert status → badge tone. */
export function alertTone(status: string): HealthTone {
  switch (status) {
    case "triggered":
      return "danger";
    case "acknowledged":
      return "warning";
    case "resolved":
      return "success";
    default:
      return "neutral";
  }
}

/** True when the health view is usable for display (has evaluated points). */
export function hasHealthData(health: HealthStateView | undefined): boolean {
  return !!health && health.state !== "unknown" && health.points.length > 0;
}

// Chart/monitoring formatting helpers. Precision is deliberately friendly
// (spec §23): 32.4%, never 32.437829%.

export function formatPercent(value: number | null | undefined, digits = 1): string {
  if (value === null || value === undefined || Number.isNaN(value)) return "—";
  return `${value.toFixed(digits)}%`;
}

export function formatBytes(bytes: number | null | undefined, digits = 1): string {
  if (bytes === null || bytes === undefined || Number.isNaN(bytes)) return "—";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let v = bytes;
  let unit = 0;
  while (Math.abs(v) >= 1024 && unit < units.length - 1) {
    v /= 1024;
    unit++;
  }
  return `${v.toFixed(unit === 0 ? 0 : digits)} ${units[unit]}`;
}

export function formatMbps(mbps: number | null | undefined): string {
  if (mbps === null || mbps === undefined || Number.isNaN(mbps)) return "—";
  if (mbps >= 1000) return `${(mbps / 1000).toFixed(2)} Gbps`;
  if (mbps >= 100) return `${mbps.toFixed(0)} Mbps`;
  return `${mbps.toFixed(1)} Mbps`;
}

export function formatUptime(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || Number.isNaN(seconds) || seconds < 0) {
    return "—";
  }
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

/** Parses an API range token into seconds; returns null when invalid. */
export function rangeToSeconds(range: string): number | null {
  switch (range) {
    case "1h":
      return 3600;
    case "6h":
      return 6 * 3600;
    case "24h":
      return 24 * 3600;
    case "7d":
      return 7 * 86400;
    case "30d":
      return 30 * 86400;
    default:
      return null;
  }
}

/** Short time label for chart axes: 14:35 for intraday, Aug 29 for longer. */
export function timeLabel(iso: string, rangeSeconds: number): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  if (rangeSeconds <= 6 * 3600) {
    return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  }
  if (rangeSeconds <= 7 * 86400) {
    return d.toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit" });
  }
  return d.toLocaleDateString([], { month: "short", day: "numeric" });
}

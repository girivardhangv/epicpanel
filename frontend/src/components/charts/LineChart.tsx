import { useMemo, useState } from "react";
import { Spinner } from "../ui/States";
import { timeLabel } from "../../lib/format";

export interface ChartPoint {
  t: string;
  value: number | null;
}

interface LineChartProps {
  title?: string;
  unit?: string;
  points: ChartPoint[];
  rangeSeconds: number;
  loading?: boolean;
  error?: unknown;
  onRetry?: () => void;
  /** Optional horizontal reference lines, e.g. warning/critical thresholds. */
  thresholds?: Array<{ value: number; label: string; className: string }>;
  /** Fixed max for the y axis (e.g. 100 for percent). */
  yMax?: number;
  height?: number;
}

const W = 640; // viewBox width; the SVG scales responsively
const PAD = { top: 12, right: 12, bottom: 24, left: 44 };

/**
 * Lightweight dependency-free SVG line chart with tooltip, threshold guides
 * and honest empty/error states. Values are downsampled by the API; this
 * component never renders thousands of DOM nodes (spec §35).
 */
export function LineChart({
  title,
  unit = "%",
  points,
  rangeSeconds,
  loading,
  error,
  onRetry,
  thresholds = [],
  yMax,
  height = 180,
}: LineChartProps) {
  const [hover, setHover] = useState<number | null>(null);

  const geometry = useMemo(() => {
    const clean = points.filter((p) => p.value !== null && !Number.isNaN(p.value as number));
    if (clean.length === 0) return null;
    const values = clean.map((p) => p.value as number);
    let min = Math.min(...values, 0);
    let max = yMax ?? Math.max(...values, ...thresholds.map((t) => t.value));
    if (max - min < 1) max = min + 1;
    const innerW = W - PAD.left - PAD.right;
    const innerH = height - PAD.top - PAD.bottom;
    const x = (i: number) =>
      PAD.left + (clean.length === 1 ? innerW / 2 : (i / (clean.length - 1)) * innerW);
    const y = (v: number) => PAD.top + innerH - ((v - min) / (max - min)) * innerH;
    const path = clean
      .map((p, i) => `${i === 0 ? "M" : "L"}${x(i).toFixed(1)},${y(p.value as number).toFixed(1)}`)
      .join(" ");
    const area = `${path} L${x(clean.length - 1).toFixed(1)},${(height - PAD.bottom).toFixed(1)} L${x(0).toFixed(1)},${(height - PAD.bottom).toFixed(1)} Z`;
    return { clean, min, max, x, y, path, area, innerW, innerH };
  }, [points, yMax, thresholds, height]);

  if (loading) {
    return (
      <div className="rounded-xl border border-slate-200 p-4">
        <ChartHeader title={title} />
        <Spinner label="Loading chart…" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="rounded-xl border border-slate-200 p-4" role="alert">
        <ChartHeader title={title} />
        <p className="py-4 text-center text-sm text-slate-500">
          Historical data is not available right now.
        </p>
        {onRetry && (
          <div className="flex justify-center pb-2">
            <button
              onClick={onRetry}
              className="focus-ring rounded-md border border-slate-300 px-3 py-1.5 text-xs hover:bg-slate-50"
            >
              Try again
            </button>
          </div>
        )}
      </div>
    );
  }

  if (!geometry) {
    return (
      <div className="rounded-xl border border-dashed border-slate-300 bg-white/60 p-4">
        <ChartHeader title={title} />
        <p className="py-6 text-center text-sm text-slate-500">
          No data yet — charts appear once the agent reports telemetry.
        </p>
      </div>
    );
  }

  const { clean, min, max, x, y, path, area, innerH } = geometry;
  const hoverPoint = hover !== null ? clean[hover] : null;

  return (
    <figure className="rounded-xl border border-slate-200 bg-white p-4">
      <ChartHeader title={title} />
      <div className="relative">
        <svg
          viewBox={`0 0 ${W} ${height}`}
          className="w-full"
          style={{ height }}
          role="img"
          aria-label={title ?? "metric chart"}
          onMouseLeave={() => setHover(null)}
          onMouseMove={(e) => {
            const rect = (e.target as SVGElement).ownerSVGElement?.getBoundingClientRect();
            if (!rect) return;
            const relX = ((e.clientX - rect.left) / rect.width) * W;
            let best = 0;
            let bestDist = Infinity;
            for (let i = 0; i < clean.length; i++) {
              const d = Math.abs(x(i) - relX);
              if (d < bestDist) {
                bestDist = d;
                best = i;
              }
            }
            setHover(best);
          }}
        >
          {/* y grid lines with axis labels */}
          {[0, 0.25, 0.5, 0.75, 1].map((f) => {
            const v = min + f * (max - min);
            const yy = PAD.top + innerH - f * innerH;
            return (
              <g key={f}>
                <line x1={PAD.left} x2={W - PAD.right} y1={yy} y2={yy} className="stroke-slate-100" strokeWidth="1" />
                <text x={PAD.left - 6} y={yy + 3} textAnchor="end" className="fill-slate-400 text-[9px]">
                  {v.toFixed(0)}
                </text>
              </g>
            );
          })}

          {/* threshold guides */}
          {thresholds.map((t) =>
            t.value >= min && t.value <= max ? (
              <g key={t.label}>
                <line
                  x1={PAD.left}
                  x2={W - PAD.right}
                  y1={y(t.value)}
                  y2={y(t.value)}
                  className={t.className}
                  strokeDasharray="4 3"
                  strokeWidth="1"
                />
              </g>
            ) : null,
          )}

          <path d={area} className="fill-indigo-50" />
          <path d={path} fill="none" className="stroke-indigo-500" strokeWidth="2" />

          {/* hover marker */}
          {hover !== null && clean[hover] && (
            <g>
              <line
                x1={x(hover)} x2={x(hover)} y1={PAD.top} y2={height - PAD.bottom}
                className="stroke-slate-300" strokeWidth="1"
              />
              <circle cx={x(hover)} cy={y(clean[hover].value as number)} r="3.5" className="fill-indigo-600" />
            </g>
          )}

          {/* time axis: first / middle / last label */}
          {[0, Math.floor((clean.length - 1) / 2), clean.length - 1].map((i, k) =>
            clean[i] ? (
              <text
                key={k}
                x={x(i)}
                y={height - 6}
                textAnchor={k === 0 ? "start" : k === 2 ? "end" : "middle"}
                className="fill-slate-400 text-[9px]"
              >
                {timeLabel(clean[i].t, rangeSeconds)}
              </text>
            ) : null,
          )}
        </svg>

        {hoverPoint && (
          <div
            className="pointer-events-none absolute -top-1 rounded-lg border border-slate-200 bg-white px-2.5 py-1.5 text-xs shadow-md"
            style={{
              left: `${(x(hover ?? 0) / W) * 100}%`,
              transform: "translateX(-50%)",
            }}
          >
            <span className="font-medium text-slate-800">
              {(hoverPoint.value as number).toFixed(1)}
              {unit}
            </span>
            <span className="ml-2 text-slate-500">{timeLabel(hoverPoint.t, rangeSeconds)}</span>
          </div>
        )}
      </div>
    </figure>
  );
}

function ChartHeader({ title }: { title?: string }) {
  if (!title) return null;
  return <figcaption className="mb-2 text-sm font-semibold text-slate-900">{title}</figcaption>;
}

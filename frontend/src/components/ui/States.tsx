import { Loader2, RefreshCw } from "lucide-react";
import { cn } from "../../lib/utils";
import { Button } from "./Button";
import { Badge } from "./Card";

export function Spinner({ label }: { label?: string }) {
  return (
    <div role="status" aria-live="polite" className="flex items-center justify-center gap-2 py-10 text-slate-500">
      <Loader2 className="h-5 w-5 animate-spin" aria-hidden />
      <span className="text-sm">{label ?? "Loading…"}</span>
    </div>
  );
}

export function Skeleton({ className }: { className?: string }) {
  return <div aria-hidden className={cn("animate-pulse rounded-md bg-slate-200/80", className)} />;
}

export function ErrorState({
  error,
  onRetry,
}: {
  error: unknown;
  onRetry?: () => void;
}) {
  const message =
    error instanceof Error ? error.message : "Something went wrong while loading this view.";
  return (
    <div
      role="alert"
      className="mx-auto flex max-w-sm flex-col items-center gap-3 rounded-xl border border-slate-200 bg-white px-6 py-10 text-center shadow-sm"
    >
      <RefreshCw className="h-6 w-6 text-red-400" aria-hidden />
      <p className="text-sm font-medium text-slate-900">Could not load data</p>
      <p className="text-xs leading-relaxed text-slate-500">{message}</p>
      {onRetry && (
        <Button size="sm" variant="outline" onClick={onRetry}>
          Try again
        </Button>
      )}
    </div>
  );
}

export function EmptyState({
  icon,
  title,
  description,
  action,
}: {
  icon?: React.ReactNode;
  title: string;
  description?: string;
  action?: React.ReactNode;
}) {
  return (
    <div className="flex flex-col items-center justify-center gap-3 rounded-xl border border-dashed border-slate-300 bg-white/60 px-6 py-14 text-center">
      {icon && <div className="text-slate-300 [&>svg]:h-10 [&>svg]:w-10">{icon}</div>}
      <div>
        <p className="text-sm font-semibold text-slate-800">{title}</p>
        {description && (
          <p className="mx-auto mt-1 max-w-sm text-xs leading-relaxed text-slate-500">{description}</p>
        )}
      </div>
      {action}
    </div>
  );
}

export function NotConfigured({ what, how }: { what: string; how?: string }) {
  return (
    <div className="rounded-lg border border-dashed border-slate-300 bg-slate-50 p-4 text-center">
      <Badge tone="neutral">Not configured</Badge>
      <p className="mt-2 text-xs text-slate-600">
        {what} is not available yet.
      </p>
      {how && <p className="mt-0.5 text-xs text-slate-400">{how}</p>}
    </div>
  );
}

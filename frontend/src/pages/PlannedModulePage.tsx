import { EmptyState } from "../components/ui/States";
import { Badge } from "../components/ui/Card";

/** Honest placeholder for modules that ship in a later phase — no fake data. */
export function PlannedModulePage({ name }: { name: string }) {
  return (
    <div className="mx-auto max-w-3xl space-y-6">
      <div>
        <h1 className="text-xl font-semibold text-slate-900">{name}</h1>
        <p className="mt-0.5 text-sm text-slate-500">Planned module</p>
      </div>
      <EmptyState
        title={`${name} is not part of the foundation release`}
        description={
          "This feature will be built as a dedicated module after the core platform is stable. It is shown in navigation because your role already carries its future permissions."
        }
        action={<Badge tone="info">Coming in a later development phase</Badge>}
      />
    </div>
  );
}

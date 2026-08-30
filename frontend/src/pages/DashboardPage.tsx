import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { alertsApi, dashboardApi, monitoringApi } from "../services";
import type { LicenseInfo } from "../types/api";
import { useAuth } from "../features/auth/AuthContext";
import { Card, CardBody, CardHeader, Badge } from "../components/ui/Card";
import { Button } from "../components/ui/Button";
import { Spinner, ErrorState, EmptyState, Skeleton } from "../components/ui/States";
import { alertTone, healthLabel, healthTone } from "../lib/health";
import { formatPercent, formatUptime } from "../lib/format";
import { useToast } from "../components/ui/Toast";
import { errMessage } from "./ServersPage";
import { useState } from "react";

function licenseTone(status: LicenseInfo["status"]) {
  switch (status) {
    case "active":
      return "success" as const;
    case "grace":
      return "warning" as const;
    default:
      return "danger" as const;
  }
}

export function DashboardPage() {
  const { hasPermission } = useAuth();
  const canMonitor = hasPermission("monitoring.view");

  const summaryQuery = useQuery({
    queryKey: ["dashboard", "summary"],
    queryFn: dashboardApi.summary,
    refetchInterval: 30_000,
  });

  const fleetQuery = useQuery({
    queryKey: ["monitoring", "fleet"],
    queryFn: monitoringApi.fleet,
    enabled: canMonitor,
    refetchInterval: 15_000,
  });

  const alertsQuery = useQuery({
    queryKey: ["alerts", "dashboard"],
    queryFn: () => alertsApi.list("triggered"),
    enabled: canMonitor,
    refetchInterval: 30_000,
  });

  if (summaryQuery.isLoading) return <Spinner label="Loading dashboard…" />;
  if (summaryQuery.isError)
    return <ErrorState error={summaryQuery.error} onRetry={() => void summaryQuery.refetch()} />;

  const data = summaryQuery.data!;

  return (
    <div className="mx-auto max-w-6xl space-y-6">
      <div>
        <h1 className="text-xl font-semibold text-slate-900">Dashboard</h1>
        <p className="mt-0.5 text-sm text-slate-500">
          Fleet overview — real agent telemetry, never estimates.
        </p>
      </div>

      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <StatCard
          label="Servers"
          value={`${data.servers_online}/${data.servers_total}`}
          sub="online / registered"
        />
        <StatCard
          label="Websites"
          value={`${data.websites_active}/${data.websites_total}`}
          sub="active / provisioned"
        />
        <StatCard label="Panel users" value={String(data.users_count)} sub="active accounts" />
        <div className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm">
          <p className="text-xs font-medium uppercase tracking-wide text-slate-500">License</p>
          <p className="mt-1 text-2xl font-semibold text-slate-900">{data.license.plan || "—"}</p>
          <div className="mt-2">
            <Badge tone={licenseTone(data.license.status)}>{data.license.status}</Badge>
          </div>
        </div>
      </div>

      {canMonitor && (
        <div className="grid gap-4 lg:grid-cols-3">
          <div className="lg:col-span-2">
            <FleetTable
              query={fleetQuery}
            />
          </div>
          <AlertsPanel query={alertsQuery} />
        </div>
      )}

      <div className="grid gap-4 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <CardBody>
            <h2 className="text-sm font-semibold text-slate-900">Recent security events</h2>
            {data.recent_events.length === 0 ? (
              <p className="mt-4 text-xs text-slate-500">No events recorded yet.</p>
            ) : (
              <ul className="mt-3 divide-y divide-slate-100">
                {data.recent_events.slice(0, 8).map((ev) => (
                  <li key={ev.id} className="flex items-start justify-between gap-3 py-2">
                    <div className="min-w-0">
                      <p className="truncate text-xs font-medium text-slate-800">
                        {humanizeAction(ev.action)}
                      </p>
                      <p className="truncate text-[11px] text-slate-500">
                        {ev.actor_label || ev.actor_type}
                        {ev.ip ? ` · ${ev.ip}` : ""}
                      </p>
                    </div>
                    <time className="shrink-0 text-[11px] text-slate-400" dateTime={ev.created_at}>
                      {new Date(ev.created_at).toLocaleTimeString([], {
                        hour: "2-digit",
                        minute: "2-digit",
                      })}
                    </time>
                  </li>
                ))}
              </ul>
            )}
          </CardBody>
        </Card>

        {!canMonitor && (
          <Card>
            <CardBody>
              <h2 className="text-sm font-semibold text-slate-900">Monitoring</h2>
              <p className="mt-3 text-xs leading-relaxed text-slate-500">
                Server health, charts and alerts require the monitoring permission. Ask an
                administrator to grant <code className="font-mono">monitoring.view</code>.
              </p>
            </CardBody>
          </Card>
        )}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Fleet table (spec §25)
// ---------------------------------------------------------------------------

function FleetTable({ query }: { query: { isLoading: boolean; isError: boolean; error: unknown; data?: { servers: import("../types/api").FleetServer[] }; refetch: () => void } }) {
  if (query.isLoading) {
    return (
      <Card>
        <CardHeader title="Servers" subtitle="Live health from agent telemetry" />
        <CardBody className="space-y-3">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-8 w-full" />
          ))}
        </CardBody>
      </Card>
    );
  }
  if (query.isError)
    return <ErrorState error={query.error} onRetry={() => query.refetch()} />;

  const servers = query.data?.servers ?? [];
  return (
    <Card>
      <CardHeader
        title="Servers"
        subtitle={`${servers.filter((s) => s.online).length} of ${servers.length} online · health from smoothed telemetry`}
      />
      {servers.length === 0 ? (
        <CardBody>
          <EmptyState
            title="No servers enrolled"
            description="Connect a server to see its live health here."
          />
        </CardBody>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full min-w-[640px] text-left text-sm">
            <thead>
              <tr className="border-b border-slate-100 text-xs uppercase tracking-wide text-slate-500">
                <th className="px-6 py-3 font-medium">Server</th>
                <th className="px-4 py-3 font-medium">Health</th>
                <th className="px-4 py-3 font-medium">CPU</th>
                <th className="px-4 py-3 font-medium">Memory</th>
                <th className="px-4 py-3 font-medium">Disk</th>
                <th className="px-6 py-3 font-medium">Uptime</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-50">
              {servers.map((s) => {
                const tone = healthTone(s.health);
                return (
                  <tr key={s.server_id} className="transition-colors hover:bg-slate-50">
                    <td className="px-6 py-3">
                      <Link
                        to={`/app/servers/${s.server_id}`}
                        className="font-medium text-indigo-600 hover:text-indigo-800 focus-ring rounded"
                      >
                        {s.name || s.hostname}
                      </Link>
                      <p className="text-xs text-slate-500">{s.hostname}</p>
                    </td>
                    <td className="px-4 py-3">
                      <Badge tone={s.online ? tone : "danger"}>
                        {s.online ? healthLabel(s.health) : "Offline"}
                      </Badge>
                    </td>
                    <td className="px-4 py-3 text-slate-700">{formatPercent(s.cpu_usage)}</td>
                    <td className="px-4 py-3 text-slate-700">{formatPercent(s.memory_usage)}</td>
                    <td className="px-4 py-3 text-slate-700">{formatPercent(s.max_disk_usage)}</td>
                    <td className="px-6 py-3 text-xs text-slate-600">
                      {s.online ? formatUptime(s.uptime_hours != null ? s.uptime_hours * 3600 : null) : "—"}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Alerts panel (spec §29): severity / server / rule / triggered + acknowledge
// ---------------------------------------------------------------------------

function AlertsPanel({ query }: { query: { isLoading: boolean; isError: boolean; error: unknown; data?: { alerts: import("../types/api").AlertView[] }; refetch: () => void } }) {
  const toast = useToast();
  const [acking, setAcking] = useState<string | null>(null);
  const acknowledge = (id: string) => {
    setAcking(id);
    alertsApi
      .acknowledge(id)
      .then(() => {
        void query.refetch();
        toast.success("Alert acknowledged");
      })
      .catch((e) => toast.error(errMessage(e, "Acknowledge failed")))
      .finally(() => setAcking(null));
  };

  return (
    <Card>
      <CardHeader
        title="Recent alerts"
        subtitle="One active alert per rule — no storms"
        actions={
          <Button size="sm" variant="ghost" onClick={() => query.refetch()}>
            Refresh
          </Button>
        }
      />
      <CardBody>
        {query.isLoading && <Spinner label="Loading alerts…" />}
        {query.isError && (
          <p role="alert" className="text-xs text-red-600">
            Alerts are not available right now.
          </p>
        )}
        {query.data &&
          (query.data.alerts.length === 0 ? (
            <EmptyState
              title="No active alerts"
              description="Rules fire only when a condition persists for its configured duration."
            />
          ) : (
            <ul className="divide-y divide-slate-100">
              {query.data.alerts.slice(0, 8).map((a) => (
                <li key={a.id} className="py-2.5">
                  <div className="flex items-center gap-2">
                    <Badge tone={a.severity === "critical" ? "danger" : "warning"}>
                      {a.severity}
                    </Badge>
                    <Badge tone={alertTone(a.status)}>{a.status}</Badge>
                  </div>
                  <p className="mt-1.5 text-xs font-medium text-slate-800">
                    {a.server_name || "panel"} — {a.message}
                  </p>
                  <div className="mt-1 flex items-center justify-between">
                    <time className="text-[11px] text-slate-400" dateTime={a.triggered_at}>
                      {new Date(a.triggered_at).toLocaleString()}
                    </time>
                    {a.status === "triggered" && a.server_id && (
                      <div className="flex gap-2">
                        <Link
                          to={`/app/servers/${a.server_id}`}
                          className="text-[11px] text-indigo-600 hover:underline"
                        >
                          View server
                        </Link>
                        <button
                          onClick={() => acknowledge(a.id)}
                          disabled={acking === a.id}
                          className="text-[11px] text-slate-600 hover:underline disabled:opacity-50"
                        >
                          Acknowledge
                        </button>
                      </div>
                    )}
                  </div>
                </li>
              ))}
            </ul>
          ))}
      </CardBody>
    </Card>
  );
}

function humanizeAction(action: string): string {
  return action
    .replace(/\./g, " ")
    .split(" ")
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(" ");
}

function StatCard({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm">
      <p className="text-xs font-medium uppercase tracking-wide text-slate-500">{label}</p>
      <p className="mt-1 text-2xl font-semibold text-slate-900">{value}</p>
      {sub && <p className="mt-1 text-xs text-slate-400">{sub}</p>}
    </div>
  );
}

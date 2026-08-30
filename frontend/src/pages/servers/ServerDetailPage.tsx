import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { alertsApi, monitoringApi, serversApi, websitesApi, type MetricsRange } from "../../services";
import { useAuth } from "../../features/auth/AuthContext";
import { useToast } from "../../components/ui/Toast";
import { errMessage } from "../ServersPage";
import { LineChart } from "../../components/charts/LineChart";
import { Card, CardBody, CardHeader, Badge } from "../../components/ui/Card";
import { Button } from "../../components/ui/Button";
import { Spinner, ErrorState, EmptyState, NotConfigured } from "../../components/ui/States";
import {
  alertTone,
  healthLabel,
  healthTone,
  serviceTone,
} from "../../lib/health";
import {
  formatBytes,
  formatPercent,
  formatUptime,
} from "../../lib/format";

const TABS = ["Overview", "Monitoring", "Services", "Processes", "Websites", "Activity"] as const;
type Tab = (typeof TABS)[number];

const RANGES: MetricsRange[] = ["1h", "6h", "24h", "7d", "30d"];

export function ServerDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const { hasPermission } = useAuth();
  const [tab, setTab] = useState<Tab>("Overview");

  const serverQuery = useQuery({
    queryKey: ["servers", id],
    queryFn: () => serversApi.get(id),
    enabled: !!id,
  });

  const canMonitor = hasPermission("monitoring.server.view");

  if (serverQuery.isLoading) return <Spinner label="Loading server…" />;
  if (serverQuery.isError)
    return <ErrorState error={serverQuery.error} onRetry={() => void serverQuery.refetch()} />;

  const srv = serverQuery.data!;

  return (
    <div className="mx-auto max-w-6xl space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <div className="flex items-center gap-3">
            <h1 className="text-xl font-semibold text-slate-900">{srv.label || srv.hostname}</h1>
            <Badge tone={srv.online ? "success" : "danger"}>{srv.online ? "Online" : "Offline"}</Badge>
          </div>
          <p className="mt-1 text-sm text-slate-500">
            {srv.hostname} · {srv.os} {srv.os_version} · {srv.arch} · agent {srv.agent_version || "—"}
          </p>
        </div>
        <Button variant="outline" size="sm" onClick={() => navigate("/app/servers")}>
          All servers
        </Button>
      </div>

      <nav className="flex flex-wrap gap-1 rounded-xl border border-slate-200 bg-white p-1" aria-label="Server sections">
        {TABS.map((t) => {
          const hidden =
            (t === "Monitoring" && !canMonitor) ||
            (t === "Services" && !hasPermission("monitoring.services.view")) ||
            (t === "Processes" && !hasPermission("monitoring.processes.view"));
          if (hidden) return null;
          return (
            <button
              key={t}
              onClick={() => setTab(t)}
              aria-current={tab === t}
              className={[
                "rounded-lg px-3.5 py-2 text-sm font-medium transition-colors focus-ring",
                tab === t ? "bg-slate-900 text-white" : "text-slate-600 hover:bg-slate-100",
              ].join(" ")}
            >
              {t}
            </button>
          );
        })}
      </nav>

      {tab === "Overview" && <OverviewTab serverId={id} canMonitor={canMonitor} />}
      {tab === "Monitoring" && canMonitor && <MonitoringTab serverId={id} />}
      {tab === "Services" && hasPermission("monitoring.services.view") && <ServicesTab serverId={id} />}
      {tab === "Processes" && hasPermission("monitoring.processes.view") && <ProcessesTab serverId={id} />}
      {tab === "Websites" && <WebsitesTab serverId={id} />}
      {tab === "Activity" && <ActivityTab serverId={id} name={srv.label || srv.hostname} />}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Overview: current snapshot + health
// ---------------------------------------------------------------------------

function OverviewTab({ serverId, canMonitor }: { serverId: string; canMonitor: boolean }) {
  const current = useQuery({
    queryKey: ["servers", serverId, "metrics", "current"],
    queryFn: () => monitoringApi.current(serverId),
    enabled: canMonitor,
    refetchInterval: 10000, // spec §36: current metrics poll 10–15s
  });

  if (!canMonitor) {
    return <NotConfigured what="Monitoring" how="Your role does not include monitoring permissions" />;
  }
  if (current.isLoading) return <Spinner label="Reading telemetry…" />;
  if (current.isError)
    return <ErrorState error={current.error} onRetry={() => void current.refetch()} />;

  const data = current.data!;
  const latest = data.latest;
  const tone = healthTone(data.health.state);

  return (
    <div className="space-y-5">
      <Card>
        <CardBody className="flex flex-wrap items-center justify-between gap-4">
          <div>
            <p className="text-xs uppercase tracking-wide text-slate-500">Server health</p>
            <p className={`mt-1 text-lg font-semibold ${
              tone === "success" ? "text-emerald-600" : tone === "warning" ? "text-amber-600" : tone === "danger" ? "text-red-600" : "text-slate-500"
            }`}>
              {healthLabel(data.health.state)}
            </p>
          </div>
          <div className="flex flex-wrap gap-2">
            {(data.health.points ?? []).map((p) => (
              <span
                key={p.component}
                className="rounded-lg bg-slate-50 px-3 py-2 text-xs text-slate-600"
              >
                {p.component}: <span className="font-semibold">{formatPercent(p.value)}</span>
                <span className="ml-1.5 text-slate-400">({p.state})</span>
              </span>
            ))}
            {(data.health.points ?? []).length === 0 && (
              <span className="text-xs text-slate-500">
                No telemetry yet — the agent has not reported metrics.
              </span>
            )}
          </div>
        </CardBody>
      </Card>

      {!data.has_data ? (
        <NotConfigured
          what="Live metrics"
          how="Waiting for the first telemetry batch from this agent"
        />
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <MetricCard label="CPU" value={formatPercent(latest?.cpu_usage_percent)} sub={latest?.cpu_usage_percent != null ? `idle ${formatPercent(latest.cpu_idle_percent, 0)}` : undefined} />
          <MetricCard label="Memory" value={formatPercent(latest?.memory_usage_percent)} sub={latest ? `${formatBytes(latest.memory_used_bytes ?? null)} / ${formatBytes(latest.memory_total_bytes ?? null)}` : undefined} />
          <MetricCard label="Max disk" value={formatPercent(maxDisk(latest?.disk ?? []))} sub={maxDiskMount(latest?.disk ?? [])} />
          <MetricCard label="Uptime" value={formatUptime(latest?.uptime_seconds ?? null)} sub={`load ${latest?.load_1m ?? "—"} / ${latest?.load_5m ?? "—"}`} />
        </div>
      )}

      {latest && (latest.disk.length > 0 || latest.network.length > 0) && (
        <div className="grid gap-4 lg:grid-cols-2">
          {latest.disk.length > 0 && (
            <Card>
              <CardHeader title="Filesystems" />
              <CardBody className="space-y-2">
                {latest.disk.map((d) => (
                  <div key={d.mount} className="flex items-center gap-3 text-xs">
                    <span className="w-28 truncate font-mono text-slate-700">{d.mount}</span>
                    <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-slate-100">
                      <div
                        className={[
                          "h-full rounded-full",
                          d.usage_percent > 90 ? "bg-red-500" : d.usage_percent > 80 ? "bg-amber-500" : "bg-indigo-500",
                        ].join(" ")}
                        style={{ width: `${Math.min(d.usage_percent, 100)}%` }}
                      />
                    </div>
                    <span className="w-32 text-right text-slate-500">
                      {formatBytes(d.used_bytes)} / {formatBytes(d.total_bytes)}
                    </span>
                  </div>
                ))}
              </CardBody>
            </Card>
          )}
          {latest.network.length > 0 && (
            <Card>
              <CardHeader title="Network interfaces" subtitle="Cumulative counters since boot" />
              <CardBody className="space-y-2">
                {latest.network.slice(0, 6).map((n) => (
                  <div key={n.interface} className="flex items-center justify-between text-xs">
                    <span className="truncate font-mono text-slate-700">{n.interface}</span>
                    <span className="text-slate-500">
                      ↓ {formatBytes(n.rx_bytes)} · ↑ {formatBytes(n.tx_bytes)}
                    </span>
                  </div>
                ))}
              </CardBody>
            </Card>
          )}
        </div>
      )}
    </div>
  );
}

function maxDisk(disks: { usage_percent: number; mount: string }[]): number | null {
  if (disks.length === 0) return null;
  return Math.max(...disks.map((d) => d.usage_percent));
}

function maxDiskMount(disks: { usage_percent: number; mount: string }[]): string | undefined {
  if (disks.length === 0) return undefined;
  return disks.reduce((a, b) => (a.usage_percent >= b.usage_percent ? a : b)).mount;
}

function MetricCard({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <Card>
      <CardBody className="py-4">
        <p className="text-xs uppercase tracking-wide text-slate-500">{label}</p>
        <p className="mt-1 text-2xl font-semibold text-slate-900">{value}</p>
        {sub && <p className="mt-0.5 truncate text-xs text-slate-500">{sub}</p>}
      </CardBody>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Monitoring: charts with range selector
// ---------------------------------------------------------------------------

function MonitoringTab({ serverId }: { serverId: string }) {
  const [range, setRange] = useState<MetricsRange>("24h");

  const history = useQuery({
    queryKey: ["servers", serverId, "metrics", "history", range],
    queryFn: () => monitoringApi.history(serverId, range),
    staleTime: 60_000, // historical charts are cached, not re-downloaded (§36)
  });

  const network = useQuery({
    queryKey: ["servers", serverId, "metrics", "network", range],
    queryFn: () => monitoringApi.network(serverId, range),
    staleTime: 60_000,
    enabled: range !== "30d", // network series are raw-sample based (≤24h... 7d ok, capped server-side)
  });

  const disk = useQuery({
    queryKey: ["servers", serverId, "metrics", "disk", range],
    queryFn: () => monitoringApi.disk(serverId, range),
    staleTime: 60_000,
  });

  const current = useQuery({
    queryKey: ["servers", serverId, "metrics", "current"],
    queryFn: () => monitoringApi.current(serverId),
    refetchInterval: 10000,
  });

  const rangeSeconds = { "1h": 3600, "6h": 21600, "24h": 86400, "7d": 604800, "30d": 2592000 }[range];
  const t = current.data?.thresholds;
  const toPoints = (get: (p: import("../../types/api").HistoryPoint) => number | null) =>
    (history.data?.points ?? []).map((p) => ({ t: p.t, value: get(p) }));

  return (
    <div className="space-y-5">
      <div className="flex items-center justify-between">
        <p className="text-sm text-slate-500">
          {history.data?.source === "hourly" && "Hourly aggregates"}
          {history.data?.source === "daily" && "Daily aggregates"}
          {history.data?.source === "raw" && "Raw samples"}
        </p>
        <div className="flex gap-1 rounded-lg border border-slate-300 bg-white p-0.5">
          {RANGES.map((r) => (
            <button
              key={r}
              onClick={() => setRange(r)}
              className={[
                "rounded-md px-2.5 py-1 text-xs font-medium focus-ring",
                range === r ? "bg-slate-900 text-white" : "text-slate-600 hover:bg-slate-100",
              ].join(" ")}
            >
              {r}
            </button>
          ))}
        </div>
      </div>

      <LineChart
        title="CPU usage"
        points={toPoints((p) => p.cpu_usage)}
        rangeSeconds={rangeSeconds}
        loading={history.isLoading}
        error={history.isError ? history.error : undefined}
        onRetry={() => void history.refetch()}
        yMax={100}
        thresholds={t ? [
          { value: t.cpu_warn, label: "warn", className: "stroke-amber-300" },
          { value: t.cpu_crit, label: "crit", className: "stroke-red-300" },
        ] : []}
      />

      <LineChart
        title="Memory usage"
        points={toPoints((p) => p.memory_usage)}
        rangeSeconds={rangeSeconds}
        loading={history.isLoading}
        error={history.isError ? history.error : undefined}
        onRetry={() => void history.refetch()}
        yMax={100}
        thresholds={t ? [
          { value: t.mem_warn, label: "warn", className: "stroke-amber-300" },
          { value: t.mem_crit, label: "crit", className: "stroke-red-300" },
        ] : []}
      />

      <LineChart
        title="Load average (5m)"
        points={toPoints((p) => p.load_5m)}
        rangeSeconds={rangeSeconds}
        loading={history.isLoading}
        error={history.isError ? history.error : undefined}
        onRetry={() => void history.refetch()}
      />

      <LineChart
        title="Highest disk usage"
        points={toPoints((p) => p.max_disk_usage)}
        rangeSeconds={rangeSeconds}
        loading={disk.isLoading}
        error={disk.isError ? disk.error : undefined}
        onRetry={() => void disk.refetch()}
        yMax={100}
        thresholds={t ? [
          { value: t.disk_warn, label: "warn", className: "stroke-amber-300" },
          { value: t.disk_crit, label: "crit", className: "stroke-red-300" },
        ] : []}
      />

      {range !== "30d" && (
        <NetworkCharts
          interfaces={network.data?.interfaces ?? null}
          loading={network.isLoading}
          error={network.error}
          rangeSeconds={rangeSeconds}
          onRetry={() => void network.refetch()}
        />
      )}
      {range === "30d" && (
        <NotConfigured
          what="Network rate history beyond 7 days"
          how="Rate series are computed from raw counters; long ranges show CPU/memory/disk aggregates"
        />
      )}
    </div>
  );
}

function NetworkCharts({
  interfaces,
  loading,
  error,
  rangeSeconds,
  onRetry,
}: {
  interfaces: Record<string, { points: { t: string; rx_mbps: number | null; tx_mbps: number | null }[] }> | null;
  loading: boolean;
  error: unknown;
  rangeSeconds: number;
  onRetry: () => void;
}) {
  if (loading) return <Spinner label="Computing rates…" />;
  if (error)
    return (
      <div role="alert" className="rounded-xl border border-slate-200 p-4 text-center text-sm text-slate-500">
        Network history is not available.{" "}
        <button onClick={onRetry} className="text-indigo-600 hover:underline">Retry</button>
      </div>
    );
  const names = Object.keys(interfaces ?? {});
  if (names.length === 0) {
    return (
      <div className="rounded-xl border border-dashed border-slate-300 p-4 text-center text-sm text-slate-500">
        No network series yet.
      </div>
    );
  }
  return (
    <div className="space-y-5">
      {names.slice(0, 4).map((name) => {
        const pts = interfaces![name].points;
        return (
          <div key={name} className="grid gap-4 lg:grid-cols-2">
            <LineChart
              title={`RX · ${name}`}
              points={pts.map((p) => ({ t: p.t, value: p.rx_mbps }))}
              rangeSeconds={rangeSeconds}
              unit=" Mbps"
            />
            <LineChart
              title={`TX · ${name}`}
              points={pts.map((p) => ({ t: p.t, value: p.tx_mbps }))}
              rangeSeconds={rangeSeconds}
              unit=" Mbps"
            />
          </div>
        );
      })}
      <p className="text-xs text-slate-400">
        Rates are derived centrally from cumulative counters (spec §5). Current totals:{" "}
        {names.map((n) => n).join(", ")}
      </p>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Services / Processes
// ---------------------------------------------------------------------------

function ServicesTab({ serverId }: { serverId: string }) {
  const toast = useToast();
  const queryClient = useQueryClient();
  const q = useQuery({
    queryKey: ["servers", serverId, "metrics", "services"],
    queryFn: () => monitoringApi.services(serverId),
    refetchInterval: 15000,
  });
  const [installing, setInstalling] = useState<string | null>(null);
  const install = (runtime: "nginx" | "php", version?: string) => {
    setInstalling(runtime);
    const call = runtime === "nginx"
      ? serversApi.installNginx(serverId)
      : serversApi.installPHP(serverId, version);
    call
      .then(() => {
        toast.success(`${runtime === "nginx" ? "Nginx" : "PHP"} installed — probing...`);
        void q.refetch();
        void queryClient.invalidateQueries({ queryKey: ["servers", serverId, "capabilities"] });
      })
      .catch((e) => toast.error(errMessage(e, `${runtime} install failed`)))
      .finally(() => setInstalling(null));
  };
  if (q.isLoading) return <Spinner label="Reading service health…" />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  const data = q.data!;
  if (!data.has_data)
    return <EmptyState title="No service data yet" description="The agent has not reported service health." />;
  return (
    <Card>
      <CardHeader title="Services" subtitle={`Observed ${new Date(data.observed_at ?? "").toLocaleString()}`} />
      <CardBody className="space-y-2">
        {data.services.map((s) => (
          <div key={s.name} className="flex items-center justify-between rounded-lg bg-slate-50 px-4 py-2.5 text-sm">
            <div>
              <p className="font-medium text-slate-800">{s.display_name}</p>
              <p className="text-xs text-slate-500">{s.name}{s.enabled != null ? (s.enabled ? " · enabled" : " · disabled") : ""}</p>
            </div>
            <div className="flex items-center gap-2">
              <Badge tone={serviceTone(s.status)}>{s.status}</Badge>
              {(s.status === "Stopped" || s.status === "NotInstalled") && (
                <Button
                  size="sm"
                  variant="outline"
                  loading={installing === s.name}
                  onClick={() => install(s.name === "nginx" ? "nginx" : "php")}
                >
                  Install
                </Button>
              )}
            </div>
          </div>
        ))}
      </CardBody>
    </Card>
  );
}

function ProcessesTab({ serverId }: { serverId: string }) {
  const q = useQuery({
    queryKey: ["servers", serverId, "metrics", "processes"],
    queryFn: () => monitoringApi.processes(serverId),
    refetchInterval: 15000,
  });
  if (q.isLoading) return <Spinner label="Reading processes…" />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  const data = q.data!;
  if (!data.has_data)
    return <EmptyState title="No process data yet" description="The agent has not reported its bounded process list." />;
  return (
    <Card>
      <CardHeader
        title="Top processes"
        subtitle={`Top CPU + top memory (bounded list) · observed ${new Date(data.observed_at ?? "").toLocaleString()}`}
      />
      <div className="overflow-x-auto">
        <table className="w-full min-w-[520px] text-left text-sm">
          <thead>
            <tr className="border-b border-slate-100 text-xs uppercase tracking-wide text-slate-500">
              <th className="px-6 py-3 font-medium">Process</th>
              <th className="px-4 py-3 font-medium">PID</th>
              <th className="px-4 py-3 font-medium">CPU</th>
              <th className="px-4 py-3 font-medium">Memory</th>
              <th className="px-6 py-3 font-medium">Status</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-50">
            {data.processes.map((p) => (
              <tr key={`${p.pid}-${p.name}`}>
                <td className="px-6 py-2.5 font-mono text-xs text-slate-800">{p.name}</td>
                <td className="px-4 py-2.5 text-slate-600">{p.pid}</td>
                <td className="px-4 py-2.5 text-slate-700">{formatPercent(p.cpu_percent)}</td>
                <td className="px-4 py-2.5 text-slate-700">{formatBytes(p.memory_bytes)}</td>
                <td className="px-6 py-2.5 text-slate-600">{p.status}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Websites hosted on this server
// ---------------------------------------------------------------------------

function WebsitesTab({ serverId }: { serverId: string }) {
  const q = useQuery({
    queryKey: ["websites", "server", serverId],
    queryFn: () => websitesApi.list({ server_id: serverId }),
  });
  if (q.isLoading) return <Spinner label="Loading websites…" />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  const sites = q.data?.websites ?? [];
  if (sites.length === 0)
    return <EmptyState title="No websites on this server" description="Provision one from the Websites page." />;
  return (
    <Card>
      <CardHeader title="Websites" subtitle={`${sites.length} hosted`} />
      <CardBody className="space-y-2">
        {sites.map((w) => (
          <Link
            key={w.id}
            to={`/app/websites/${w.id}`}
            className="flex items-center justify-between rounded-lg px-3 py-2.5 text-sm transition-colors hover:bg-slate-50"
          >
            <span className="font-medium text-indigo-700">{w.domain}</span>
            <span className="flex items-center gap-3 text-xs text-slate-500">
              {w.php_version ? `PHP ${w.php_version}` : "static"}
              <Badge tone={w.status === "active" ? "success" : w.status === "error" ? "danger" : "neutral"}>
                {w.status}
              </Badge>
            </span>
          </Link>
        ))}
      </CardBody>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Activity: alerts for this server
// ---------------------------------------------------------------------------

function ActivityTab({ serverId, name }: { serverId: string; name: string }) {
  const toast = useToast();
  const queryClient = useQueryClient();
  const q = useQuery({
    queryKey: ["alerts", "server", serverId],
    queryFn: () => alertsApi.list(undefined, serverId),
  });
  const ack = useMutationToast(() => {
    void queryClient.invalidateQueries({ queryKey: ["alerts"] });
    toast.success("Alert acknowledged");
  });

  if (q.isLoading) return <Spinner label="Loading alerts…" />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  const alerts = q.data?.alerts ?? [];
  if (alerts.length === 0)
    return <EmptyState title={`No alerts for ${name}`} description="Alerts appear when a rule stays breached for its configured duration." />;

  return (
    <Card>
      <CardHeader title="Alerts" subtitle="Deduplicated per rule — no alert storms" />
      <CardBody className="space-y-2">
        {alerts.map((a) => (
          <div key={a.id} className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-slate-100 px-4 py-3">
            <div>
              <div className="flex items-center gap-2">
                <Badge tone={a.severity === "critical" ? "danger" : "warning"}>{a.severity}</Badge>
                <Badge tone={alertTone(a.status)}>{a.status}</Badge>
              </div>
              <p className="mt-1.5 text-sm text-slate-800">{a.message}</p>
              <p className="text-xs text-slate-500">triggered {new Date(a.triggered_at).toLocaleString()}</p>
            </div>
            {a.status === "triggered" && <Button size="sm" variant="outline" loading={ack.pendingId === a.id} onClick={() => ack.acknowledge(a.id)}>Acknowledge</Button>}
          </div>
        ))}
      </CardBody>
    </Card>
  );
}

function useMutationToast(onDone: () => void) {
  const [pendingId, setPendingId] = useState<string | null>(null);
  const toast = useToast();
  const acknowledge = (id: string) => {
    setPendingId(id);
    alertsApi
      .acknowledge(id)
      .then(() => onDone())
      .catch((e) => toast.error(errMessage(e, "Acknowledge failed")))
      .finally(() => setPendingId(null));
  };
  return { acknowledge, pendingId };
}

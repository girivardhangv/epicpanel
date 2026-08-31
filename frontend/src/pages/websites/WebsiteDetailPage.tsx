import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { databasesApi, domainsApi, serversApi, websitesApi } from "../../services";
import { ApiError } from "../../lib/api";
import { useAuth } from "../../features/auth/AuthContext";
import { useToast } from "../../components/ui/Toast";
import { errMessage } from "../ServersPage";
import { WebsiteStatusBadge } from "../WebsitesPage";
import { serviceTone } from "../../lib/health";
import type { LogPage, WebsiteView } from "../../types/api";
import { Card, CardBody, CardHeader, Badge } from "../../components/ui/Card";
import { Button } from "../../components/ui/Button";
import { Modal } from "../../components/ui/Modal";
import { Spinner, ErrorState, NotConfigured } from "../../components/ui/States";
import { Alert } from "../../components/ui/Alert";

export function WebsiteDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { hasPermission } = useAuth();
  const toast = useToast();

  const websiteQuery = useQuery({
    queryKey: ["websites", id],
    queryFn: () => websitesApi.get(id),
    enabled: !!id,
  });

  const healthQuery = useQuery({
    queryKey: ["websites", id, "health"],
    queryFn: () => websitesApi.health(id),
    enabled: !!id,
    refetchInterval: 15000,
  });

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ["websites", id] });
    void queryClient.invalidateQueries({ queryKey: ["websites"] });
  };

  const stateMutation = useMutation({
    mutationFn: async ({ action }: { action: "enable" | "disable" | "reload" }) => {
      if (action === "enable") return websitesApi.enable(id);
      if (action === "disable") return websitesApi.disable(id);
      await websitesApi.reload(id);
      return { status: "reloaded" };
    },
    onSuccess: (_data, vars) => {
      invalidate();
      toast.success(
        vars.action === "enable"
          ? "Website enabled"
          : vars.action === "disable"
            ? "Website disabled"
            : "Nginx reloaded",
      );
    },
    onError: (err) => toast.error(errMessage(err, "Operation failed")),
  });

  const retryMutation = useMutation({
    mutationFn: () => websitesApi.retry(id),
    onSuccess: () => {
      invalidate();
      toast.success("Provisioning re-queued");
    },
    onError: (err) => toast.error(errMessage(err, "Retry failed")),
  });

  if (websiteQuery.isLoading) return <Spinner label="Loading website…" />;
  if (websiteQuery.isError)
    return <ErrorState error={websiteQuery.error} onRetry={() => void websiteQuery.refetch()} />;

  const w = websiteQuery.data!;

  return (
    <div className="mx-auto max-w-6xl space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <div className="flex items-center gap-3">
            <h1 className="text-xl font-semibold text-slate-900">{w.domain}</h1>
            <WebsiteStatusBadge status={w.status} />
          </div>
          <p className="mt-1 text-sm text-slate-500">
            {w.server_name} · {w.web_server}
            {w.php_version ? ` · PHP ${w.php_version}` : " · static"}
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <a
            href={`http://${w.domain}`}
            target="_blank"
            rel="noreferrer"
            className="focus-ring inline-flex h-10 items-center rounded-lg border border-slate-300 bg-white px-4 text-sm font-medium text-slate-700 shadow-sm hover:bg-slate-50"
          >
            Visit website
          </a>
          {hasPermission("websites.edit") && w.status === "active" && (
            <>
              <Button
                variant="outline"
                size="md"
                loading={stateMutation.isPending && stateMutation.variables?.action === "reload"}
                onClick={() => stateMutation.mutate({ action: "reload" })}
              >
                Reload
              </Button>
              <Button
                variant="outline"
                size="md"
                loading={stateMutation.isPending && stateMutation.variables?.action === "disable"}
                onClick={() => stateMutation.mutate({ action: "disable" })}
              >
                Disable
              </Button>
            </>
          )}
          {hasPermission("websites.edit") && w.status === "disabled" && (
            <Button
              loading={stateMutation.isPending && stateMutation.variables?.action === "enable"}
              onClick={() => stateMutation.mutate({ action: "enable" })}
            >
              Enable
            </Button>
          )}
        </div>
      </div>

      {w.status === "error" && (
        <Alert tone="danger" title="Provisioning failed for this website">
          Fix the reported problem, then retry. Completed steps are preserved.{" "}
          {hasPermission("websites.edit") && (
            <Button
              size="sm"
              className="ml-2"
              loading={retryMutation.isPending}
              onClick={() => retryMutation.mutate()}
            >
              Retry provisioning
            </Button>
          )}
        </Alert>
      )}
      {w.status === "provisioning" && (
        <Alert tone="info" title="Provisioning in progress">
          This website is being set up. The page updates when the job finishes.
        </Alert>
      )}

      <div className="grid gap-4 lg:grid-cols-3">
        <Card>
          <CardHeader title="Configuration" />
          <CardBody>
            <dl className="space-y-3 text-sm">
              <InfoRow label="Server" value={`${w.server_name} (${w.server_os})`} />
              <InfoRow label="Web server" value={w.web_server} />
              <InfoRow label="PHP" value={w.php_version || "static site"} />
              {w.os_user && <InfoRow label="Isolation user" value={w.os_user} mono />}
              <InfoRow label="Document root" value={w.document_root} mono />
              <InfoRow
                label="Aliases"
                value={
                  w.aliases.length > 0 ? (
                    <span className="flex flex-wrap gap-1">
                      {w.aliases.map((a) => (
                        <Badge key={a} tone="neutral">
                          {a}
                        </Badge>
                      ))}
                    </span>
                  ) : (
                    "none"
                  )
                }
              />
            </dl>
            <div className="mt-4 flex flex-wrap gap-2 border-t border-slate-100 pt-4">
              {hasPermission("websites.php.manage") && (
                <ChangePHPButton
                  websiteId={w.id}
                  current={w.php_version}
                  serverId={w.server_id}
                  onChanged={() => {
                    invalidate();
                    toast.success("PHP change queued");
                  }}
                  onError={(e) => toast.error(errMessage(e, "PHP change failed"))}
                />
              )}
              {hasPermission("websites.config.manage") && (
                <ManageAliasesButton
                  websiteId={w.id}
                  serverId={w.server_id}
                  currentIds={w.aliases}
                  onChanged={() => {
                    invalidate();
                    toast.success("Alias update queued");
                  }}
                  onError={(e) => toast.error(errMessage(e, "Alias update failed"))}
                />
              )}
              {hasPermission("websites.config.manage") && (
                <Link
                  to={`/app/websites/${w.id}/files`}
                  className="inline-flex items-center gap-1 rounded-lg border border-slate-300 px-3 py-1.5 text-sm font-medium text-slate-700 transition-colors hover:bg-slate-50 focus-ring"
                >
                  File Manager
                </Link>
              )}
            </div>
          </CardBody>
        </Card>

        <Card>
          <CardHeader title="Website health" subtitle="Derived from provisioning state and server telemetry" />
          <CardBody>
            {healthQuery.isLoading && <Spinner label="Checking health…" />}
            {healthQuery.isError && (
              <p role="alert" className="text-xs text-slate-500">
                Health is unavailable right now.
              </p>
            )}
            {healthQuery.data && (
              <div className="space-y-2.5 text-sm">
                <HealthRow label="Nginx" status={healthQuery.data.nginx.status} />
                <HealthRow
                  label={healthQuery.data.php_version ? `PHP ${healthQuery.data.php_version}` : "PHP"}
                  status={
                    healthQuery.data.php_version
                      ? healthQuery.data.php.status
                      : "NotInstalled"
                  }
                  muted={!healthQuery.data.php_version}
                />
                <HealthRow label="Configuration" status={healthQuery.data.configuration.status} />
                <HealthRow label="Server" status={healthQuery.data.server.status === "online" ? "Running" : "Stopped"} />
                {!healthQuery.data.has_data && (
                  <p className="pt-1 text-xs text-slate-400">
                    Service status appears once the agent reports telemetry.
                  </p>
                )}
              </div>
            )}
          </CardBody>
        </Card>

        <Card>
          <CardHeader title="SSL / DNS" subtitle="Certificates issued per website (Phase 4)" />
          <CardBody className="space-y-3">
            <SSLCard websiteId={w.id} />
            <NotConfigured what="DNS management" how="Ships in a later phase" />
          </CardBody>
        </Card>

        <Card>
          <CardHeader title="Resource limits" subtitle="Per-site CPU & memory ceilings (Phase 9)" />
          <CardBody>
            <ResourceLimitsCard websiteId={w.id} website={w} />
          </CardBody>
        </Card>
      </div>

      <Card>
        <CardHeader title="Disk usage" />
        <CardBody>
          <NotConfigured what="Per-site disk accounting" how="Ships in a later phase" />
        </CardBody>
      </Card>

      <WebsiteDatabasesSection websiteId={w.id} />

      {hasPermission("websites.logs.view") && w.status !== "provisioning" && (
        <LogsPanel websiteId={w.id} serverReachable={w.server_online} />
      )}

      {hasPermission("websites.delete") && (
        <DeleteWebsiteCard
          websiteId={w.id}
          domain={w.domain}
          onDeleted={() => {
            void queryClient.invalidateQueries({ queryKey: ["websites"] });
            toast.success("Website deletion started");
            navigate("/app/websites");
          }}
          onError={(e) => toast.error(errMessage(e, "Delete failed"))}
        />
      )}
    </div>
  );
}

function InfoRow({ label, value, mono }: { label: string; value: React.ReactNode; mono?: boolean }) {
  return (
    <div className="flex items-start justify-between gap-4">
      <dt className="text-slate-500">{label}</dt>
      <dd className={mono ? "break-all text-right font-mono text-xs text-slate-800" : "text-right font-medium text-slate-800"}>
        {value}
      </dd>
    </div>
  );
}

function HealthRow({ label, status, muted }: { label: string; status: string; muted?: boolean }) {
  return (
    <div className="flex items-center justify-between">
      <span className={muted ? "text-slate-400" : "text-slate-500"}>{label}</span>
      <Badge tone={muted ? "neutral" : serviceTone(status)}>{muted ? "—" : status}</Badge>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Change PHP
// ---------------------------------------------------------------------------

function ChangePHPButton({
  websiteId,
  current,
  serverId,
  onChanged,
  onError,
}: {
  websiteId: string;
  current: string;
  serverId: string;
  onChanged: () => void;
  onError: (e: unknown) => void;
}) {
  const [open, setOpen] = useState(false);
  const versionsQuery = useQuery({
    queryKey: ["servers", serverId, "php-versions"],
    queryFn: () => serversApi.phpVersions(serverId),
    enabled: open,
  });
  const [selected, setSelected] = useState(current);
  const update = useMutation({
    mutationFn: () => websitesApi.update(websiteId, { php_version: selected }),
    onSuccess: () => {
      setOpen(false);
      onChanged();
    },
    onError,
  });

  return (
    <>
      <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
        Change PHP
      </Button>
      {open && (
        <Modal title="Change PHP runtime" onClose={() => setOpen(false)}>
          {versionsQuery.isLoading ? (
            <Spinner label="Discovering runtimes…" />
          ) : (versionsQuery.data?.versions ?? []).length === 0 ? (
            <Alert tone="warning" title="PHP is not installed on this server.">
              Install PHP on the server, then refresh.
            </Alert>
          ) : (
            <div className="grid gap-2 sm:grid-cols-3">
              {(versionsQuery.data?.versions ?? []).map((p) => (
                <button
                  key={p.version}
                  type="button"
                  onClick={() => setSelected(p.version)}
                  className={[
                    "rounded-lg border px-3 py-2.5 text-left text-sm transition-colors focus-ring",
                    selected === p.version
                      ? "border-indigo-600 bg-indigo-50 font-medium text-indigo-700"
                      : "border-slate-300 bg-white text-slate-700 hover:bg-slate-50",
                  ].join(" ")}
                >
                  PHP {p.version}
                </button>
              ))}
              <button
                type="button"
                onClick={() => setSelected("")}
                className={[
                  "rounded-lg border px-3 py-2.5 text-left text-sm transition-colors focus-ring",
                  selected === ""
                    ? "border-indigo-600 bg-indigo-50 font-medium text-indigo-700"
                    : "border-slate-300 bg-white text-slate-700 hover:bg-slate-50",
                ].join(" ")}
              >
                No PHP
              </button>
            </div>
          )}
          <div className="mt-4 flex justify-end gap-2 border-t border-slate-100 pt-4">
            <Button variant="ghost" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button loading={update.isPending} disabled={selected === current} onClick={() => update.mutate()}>
              Apply change
            </Button>
          </div>
        </Modal>
      )}
    </>
  );
}

// ---------------------------------------------------------------------------
// Aliases
// ---------------------------------------------------------------------------

function ManageAliasesButton({
  websiteId,
  serverId,
  currentIds,
  onChanged,
  onError,
}: {
  websiteId: string;
  serverId: string;
  currentIds: string[];
  onChanged: () => void;
  onError: (e: unknown) => void;
}) {
  const [open, setOpen] = useState(false);
  const domainsQuery = useQuery({
    queryKey: ["domains", serverId],
    queryFn: () => domainsApi.list(serverId),
    enabled: open,
  });
  const [selected, setSelected] = useState<string[]>(currentIds);
  const update = useMutation({
    mutationFn: () => websitesApi.update(websiteId, { alias_domain_ids: selected }),
    onSuccess: () => {
      setOpen(false);
      onChanged();
    },
    onError,
  });

  const candidates = (domainsQuery.data?.domains ?? []).filter(
    (d) => d.type === "alias" && (!d.website_id || d.website_id === websiteId),
  );

  return (
    <>
      <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
        Manage aliases
      </Button>
      {open && (
        <Modal title="Manage aliases" onClose={() => setOpen(false)} wide>
          {candidates.length === 0 ? (
            <Alert tone="info" title="No alias domains available">
              Add alias-type domains for this server first.
            </Alert>
          ) : (
            <div className="flex flex-wrap gap-2">
              {candidates.map((d) => {
                const checked = selected.includes(d.id);
                return (
                  <button
                    key={d.id}
                    type="button"
                    aria-pressed={checked}
                    onClick={() =>
                      setSelected(checked ? selected.filter((x) => x !== d.id) : [...selected, d.id])
                    }
                    className={[
                      "rounded-full border px-3 py-1 text-xs transition-colors focus-ring",
                      checked
                        ? "border-indigo-600 bg-indigo-50 font-medium text-indigo-700"
                        : "border-slate-300 bg-white text-slate-600 hover:bg-slate-50",
                    ].join(" ")}
                  >
                    {d.domain}
                  </button>
                );
              })}
            </div>
          )}
          <div className="mt-4 flex justify-end gap-2 border-t border-slate-100 pt-4">
            <Button variant="ghost" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button loading={update.isPending} onClick={() => update.mutate()}>
              Save aliases
            </Button>
          </div>
        </Modal>
      )}
    </>
  );
}

// ---------------------------------------------------------------------------
// Logs
// ---------------------------------------------------------------------------

const LOG_MAX = 128 * 1024;

function LogsPanel({ websiteId, serverReachable }: { websiteId: string; serverReachable: boolean }) {
  const [type, setType] = useState<"access" | "error">("access");
  const [filter, setFilter] = useState("");
  const logsQuery = useQuery({
    queryKey: ["websites", websiteId, "logs", type],
    queryFn: () => websitesApi.logs(websiteId, type, LOG_MAX),
    refetchInterval: 10000,
    retry: false,
  });

  const data: LogPage | undefined = logsQuery.data;
  const lines = (data?.content ?? "").split("\n");
  const shown = filter
    ? lines.filter((l) => l.toLowerCase().includes(filter.toLowerCase()))
    : lines;

  return (
    <Card>
      <CardHeader
        title="Logs"
        subtitle={
          data
            ? `${data.path} · ${data.truncated ? "last " : ""}${Math.round((data.content.length || 0) / 1024)} KB shown of ${Math.round(data.size_bytes / 1024)} KB`
            : undefined
        }
        actions={
          <div className="flex items-center gap-2">
            <div className="flex rounded-lg border border-slate-300 p-0.5">
              {(["access", "error"] as const).map((t) => (
                <button
                  key={t}
                  type="button"
                  onClick={() => setType(t)}
                  className={[
                    "rounded-md px-2.5 py-1 text-xs font-medium capitalize focus-ring",
                    type === t ? "bg-slate-900 text-white" : "text-slate-600 hover:bg-slate-100",
                  ].join(" ")}
                >
                  {t}
                </button>
              ))}
            </div>
            <input
              type="search"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="Filter lines…"
              aria-label="Filter log lines"
              className="w-44 rounded-lg border border-slate-300 px-2.5 py-1.5 text-xs shadow-sm focus-ring"
            />
            <Button size="sm" variant="ghost" onClick={() => void logsQuery.refetch()}>
              Refresh
            </Button>
          </div>
        }
      />
      <CardBody className="pt-3">
        {logsQuery.isLoading && <Spinner label="Reading log…" />}
        {logsQuery.isError && (
          <p role="alert" className="text-xs text-red-600">
            {logsQuery.error instanceof ApiError
              ? logsQuery.error.status === 0
                ? "The agent could not be reached for log retrieval."
                : logsQuery.error.message
              : "Logs are not available yet."}
          </p>
        )}
        {data && (
          <>
            {!serverReachable && (
              <p className="mb-2 text-xs text-amber-600">
                The server appears offline; the log below is the last successful read.
              </p>
            )}
            {shown.length === 0 ? (
              <p className="text-xs text-slate-500">
                {filter ? "No lines match the filter." : "The log is empty."}
              </p>
            ) : (
              <pre className="max-h-96 overflow-auto rounded-lg bg-slate-950 p-4 text-[11px] leading-relaxed text-slate-200">
                {shown.slice(-500).join("\n")}
              </pre>
            )}
            {data.truncated && (
              <p className="mt-2 text-xs text-slate-400">
                Showing the most recent lines only — the log file is larger than the retrieval
                window.
              </p>
            )}
          </>
        )}
      </CardBody>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

function DeleteWebsiteCard({
  websiteId,
  domain,
  onDeleted,
  onError,
}: {
  websiteId: string;
  domain: string;
  onDeleted: () => void;
  onError: (e: unknown) => void;
}) {
  const [open, setOpen] = useState(false);
  const [deleteFiles, setDeleteFiles] = useState(false);
  const del = useMutation({
    mutationFn: () => websitesApi.delete(websiteId, deleteFiles),
    onSuccess: () => {
      setOpen(false);
      onDeleted();
    },
    onError,
  });

  return (
    <Card className="border-red-100">
      <CardHeader title="Danger zone" subtitle="Deletion removes the Nginx configuration immediately" />
      <CardBody className="flex items-center justify-between gap-4">
        <p className="text-sm text-slate-600">
          Deleting <span className="font-medium text-slate-800">{domain}</span> removes its Nginx
          configuration and reloads the web server. Files can optionally be removed too.
        </p>
        <Button variant="danger" onClick={() => setOpen(true)} className="shrink-0">
          Delete website
        </Button>
      </CardBody>
      {open && (
        <Modal title={`Delete ${domain}?`} onClose={() => setOpen(false)}>
          <div className="space-y-4">
            <Alert tone="danger" title="This action is destructive">
              The website configuration is removed and Nginx is reloaded. Choose whether files
              should be deleted as well.
            </Alert>
            <label className="flex items-start gap-2.5 text-sm text-slate-700">
              <input
                type="checkbox"
                checked={deleteFiles}
                onChange={(e) => setDeleteFiles(e.target.checked)}
                className="mt-0.5 h-4 w-4 rounded border-slate-300 text-red-600 focus-ring"
              />
              <span>
                Also delete the website files
                <span className="block text-xs text-slate-500">
                  Unchecked: delete configuration only — files stay on disk and the site can be
                  re-created later.
                </span>
              </span>
            </label>
            <div className="flex justify-end gap-2 border-t border-slate-100 pt-4">
              <Button variant="ghost" onClick={() => setOpen(false)}>
                Cancel
              </Button>
              <Button variant="danger" loading={del.isPending} onClick={() => del.mutate()}>
                Delete {deleteFiles ? "configuration and files" : "configuration only"}
              </Button>
            </div>
          </div>
        </Modal>
      )}
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Resource limits (Phase 9)
// ---------------------------------------------------------------------------

function ResourceLimitsCard({ websiteId, website }: { websiteId: string; website: WebsiteView }) {
  const toast = useToast();
  const queryClient = useQueryClient();
  const { hasPermission } = useAuth();
  const canManage = hasPermission("websites.config.manage");

  const [cpu, setCpu] = useState(website.cpu_limit_pct ?? 0);
  const [mem, setMem] = useState(website.memory_limit_mb ?? 0);

  const save = useMutation({
    mutationFn: () => websitesApi.updateLimits(websiteId, cpu, mem),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["websites", websiteId] });
      toast.success("Resource limits applied");
    },
    onError: (e) => toast.error(errMessage(e, "Failed to apply limits")),
  });

  const cpuOk = cpu >= 0 && cpu <= 100;
  const memOk = mem >= 0;
  const changed = cpu !== (website.cpu_limit_pct ?? 0) || mem !== (website.memory_limit_mb ?? 0);

  return (
    <div className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-2">
        <label className="block">
          <span className="text-xs font-medium text-slate-600">CPU limit (%)</span>
          <input
            type="number"
            min={0}
            max={100}
            value={cpu}
            disabled={!canManage}
            onChange={(e) => setCpu(Number(e.target.value) || 0)}
            className="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 text-sm focus-ring disabled:bg-slate-50"
          />
          <span className="mt-1 block text-xs text-slate-400">0 = unlimited</span>
        </label>
        <label className="block">
          <span className="text-xs font-medium text-slate-600">Memory limit (MB)</span>
          <input
            type="number"
            min={0}
            value={mem}
            disabled={!canManage}
            onChange={(e) => setMem(Number(e.target.value) || 0)}
            className="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 text-sm focus-ring disabled:bg-slate-50"
          />
          <span className="mt-1 block text-xs text-slate-400">0 = unlimited</span>
        </label>
      </div>

      {website.server_os !== "windows" ? (
        <p className="text-xs text-slate-400">
          Enforced on the server via cgroups (per-site slice). PHP-FPM pool runs as the
          isolated account <span className="font-mono">{website.os_user || "—"}</span>.
        </p>
      ) : (
        <p className="text-xs text-amber-600">
          Resource limits are not yet enforceable on Windows servers; values are saved but not applied.
        </p>
      )}

      {canManage && (
        <div className="flex justify-end gap-2 border-t border-slate-100 pt-3">
          <Button
            size="sm"
            loading={save.isPending}
            disabled={!changed || !cpuOk || !memOk}
            onClick={() => save.mutate()}
          >
            Apply limits
          </Button>
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// SSL certificate card (Phase 4)
// ---------------------------------------------------------------------------

function SSLCard({ websiteId }: { websiteId: string }) {
  const queryClient = useQueryClient();
  const toast = useToast();
  const { hasPermission } = useAuth();

  const certQuery = useQuery({
    queryKey: ["websites", websiteId, "certificate"],
    queryFn: () => websitesApi.certificate(websiteId),
    enabled: !!websiteId,
  });

  const [autoRenew, setAutoRenew] = useState(true);

  const issueMutation = useMutation({
    mutationFn: () => websitesApi.requestCertificate(websiteId, autoRenew),
    onSuccess: () => {
      toast.success("Certificate request queued");
      void queryClient.invalidateQueries({ queryKey: ["websites", websiteId, "certificate"] });
    },
    onError: (e) => toast.error(errMessage(e, "Certificate request failed")),
  });

  const removeMutation = useMutation({
    mutationFn: () => websitesApi.removeCertificate(websiteId),
    onSuccess: () => {
      toast.success("Certificate removed");
      void queryClient.invalidateQueries({ queryKey: ["websites", websiteId, "certificate"] });
    },
    onError: (e) => toast.error(errMessage(e, "Certificate removal failed")),
  });

  const cert = certQuery.data?.certificate ?? null;
  const canManage = hasPermission("websites.config.manage");

  if (certQuery.isLoading) return <Spinner label="Loading certificate…" />;

  return (
    <div className="space-y-3">
      {cert ? (
        <>
          <div className="flex items-center justify-between">
            <span className="text-xs text-slate-500">Provider</span>
            <Badge tone="info">{cert.provider}</Badge>
          </div>
          <div className="flex items-center justify-between text-xs">
            <span className="text-slate-500">Expires</span>
            <span className="font-medium text-slate-800">
              {cert.expires_at ? new Date(cert.expires_at).toLocaleDateString() : "—"}
            </span>
          </div>
          <div className="flex items-center justify-between text-xs">
            <span className="text-slate-500">Auto-renew</span>
            <span className="font-medium text-slate-800">{cert.auto_renew ? "On" : "Off"}</span>
          </div>
          {cert.domains.length > 0 && (
            <p className="truncate text-xs text-slate-400" title={cert.domains.join(", ")}>
              {cert.domains.join(", ")}
            </p>
          )}
          {canManage && (
            <Button
              size="sm"
              variant="outline"
              loading={removeMutation.isPending}
              onClick={() => {
                if (window.confirm("Remove this certificate? The site will revert to HTTP.")) {
                  removeMutation.mutate();
                }
              }}
            >
              Remove certificate
            </Button>
          )}
        </>
      ) : (
        <>
          <p className="text-xs leading-relaxed text-slate-500">
            No certificate yet. Issue one to serve this site over HTTPS — the certificate covers
            the primary domain and its aliases.
          </p>
          {canManage && (
            <div className="space-y-2">
              <label className="flex items-center gap-2 text-xs text-slate-600">
                <input
                  type="checkbox"
                  checked={autoRenew}
                  onChange={(e) => setAutoRenew(e.target.checked)}
                  className="h-3.5 w-3.5 rounded border-slate-300 text-indigo-600 focus-ring"
                />
                Auto-renew before expiry
              </label>
              <Button
                size="sm"
                loading={issueMutation.isPending}
                onClick={() => issueMutation.mutate()}
              >
                Issue certificate
              </Button>
            </div>
          )}
        </>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Databases attached to this website (Phase 6)
// ---------------------------------------------------------------------------

function WebsiteDatabasesSection({ websiteId }: { websiteId: string }) {
  const { hasPermission } = useAuth();
  const q = useQuery({
    queryKey: ["databases", "website", websiteId],
    queryFn: () => databasesApi.list({ website_id: websiteId }),
  });

  if (!hasPermission("databases.view")) return null;

  return (
    <Card>
      <CardHeader
        title="Databases"
        subtitle="Managed databases attached to this website"
        actions={
          hasPermission("databases.create") ? (
            <Link to="/app/databases" className="focus-ring rounded-md px-2 py-1.5 text-xs text-indigo-600 hover:bg-slate-100">
              Create database
            </Link>
          ) : undefined
        }
      />
      <CardBody>
        {q.isLoading && <Spinner label="Loading databases…" />}
        {q.data && q.data.databases.length === 0 && (
          <p className="text-sm text-slate-500">No databases attached to this website yet.</p>
        )}
        {q.data && q.data.databases.length > 0 && (
          <ul className="divide-y divide-slate-100">
            {q.data.databases.map((d) => (
              <li key={d.id} className="flex items-center justify-between py-2.5">
                <Link to={`/app/databases/${d.id}`} className="text-sm font-medium text-indigo-700 hover:underline">
                  {d.name}
                </Link>
                <span className="flex items-center gap-2 text-xs text-slate-500">
                  <Badge tone="neutral">{d.engine}</Badge>
                  <span>{d.users.length} user{d.users.length === 1 ? "" : "s"}</span>
                </span>
              </li>
            ))}
          </ul>
        )}
      </CardBody>
    </Card>
  );
}

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { serversApi } from "../services";
import { ApiError } from "../lib/api";
import { useAuth } from "../features/auth/AuthContext";
import { useToast } from "../components/ui/Toast";
import type { RegistrationTokenView, ServerCapabilities, ServerView } from "../types/api";
import { Card, CardBody, Badge } from "../components/ui/Card";
import { Button } from "../components/ui/Button";
import { Input } from "../components/ui/Input";
import { Modal } from "../components/ui/Modal";
import { Spinner, ErrorState, EmptyState, NotConfigured } from "../components/ui/States";
import { Alert } from "../components/ui/Alert";

type ServerConnectionState = "online" | "offline" | "connecting" | "error" | "unknown";

function connectionState(srv: ServerView): ServerConnectionState {
  if (srv.online) return "online";
  if (!srv.last_seen_at) return "connecting"; // enrolled, awaiting first heartbeat
  if (srv.status === "online") return "offline";
  return "unknown";
}

const CONNECTION_META: Record<ServerConnectionState, { label: string; tone: "success" | "warning" | "danger" | "neutral" | "info" }> = {
  online: { label: "Online", tone: "success" },
  connecting: { label: "Connecting", tone: "info" },
  offline: { label: "Offline", tone: "danger" },
  error: { label: "Error", tone: "danger" },
  unknown: { label: "Unknown", tone: "neutral" },
};

export function ServersPage() {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const { hasPermission } = useAuth();
  const toast = useToast();
  const [detail, setDetail] = useState<ServerView | null>(null);
  const [showRegistration, setShowRegistration] = useState(false);

  const listQuery = useQuery({ queryKey: ["servers"], queryFn: () => serversApi.list() });

  const revokeMutation = useMutation({
    mutationFn: (id: string) => serversApi.revoke(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["servers"] });
      void queryClient.invalidateQueries({ queryKey: ["dashboard", "summary"] });
      setDetail(null);
      toast.success("Server access revoked");
    },
    onError: (err) => toast.error(errMessage(err, "Revocation failed")),
  });

  if (listQuery.isLoading) return <Spinner label="Loading servers…" />;
  if (listQuery.isError)
    return <ErrorState error={listQuery.error} onRetry={() => void listQuery.refetch()} />;

  const servers = listQuery.data?.servers ?? [];

  return (
    <div className="mx-auto max-w-6xl space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold text-slate-900">Servers</h1>
          <p className="mt-0.5 text-sm text-slate-500">
            Machines enrolled through the EpicPanel agent.
          </p>
        </div>
        <div className="flex gap-2">
          {hasPermission("servers.create") && (
            <Button variant="outline" size="sm" onClick={() => setShowRegistration((v) => !v)}>
              Connect a server
            </Button>
          )}
        </div>
      </div>

      {showRegistration && <RegistrationTokenPanel onClose={() => setShowRegistration(false)} />}

      {servers.length === 0 ? (
        <EmptyState
          title="No servers enrolled"
          description="Create a registration token above and run the EpicPanel agent on your first server. Tokens expire and can be used exactly once."
        />
      ) : (
        <Card>
          <div className="overflow-x-auto">
            <table className="w-full min-w-[720px] text-left text-sm">
              <thead>
                <tr className="border-b border-slate-100 text-xs uppercase tracking-wide text-slate-500">
                  <th className="px-6 py-3 font-medium">Host</th>
                  <th className="px-4 py-3 font-medium">OS</th>
                  <th className="px-4 py-3 font-medium">Arch</th>
                  <th className="px-4 py-3 font-medium">Agent</th>
                  <th className="px-4 py-3 font-medium">Management</th>
                  <th className="px-4 py-3 font-medium">Last seen</th>
                  <th className="px-6 py-3 font-medium">Status</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-50">
                {servers.map((srv) => {
                  const state = connectionState(srv);
                  return (
                    <tr
                      key={srv.id}
                      onClick={() => navigate(`/app/servers/${srv.id}`)}
                      className="cursor-pointer transition-colors hover:bg-slate-50"
                    >
                      <td className="px-6 py-3">
                        <p className="font-medium text-slate-900">{srv.label || srv.hostname}</p>
                        <p className="text-xs text-slate-500">{srv.hostname}</p>
                      </td>
                      <td className="px-4 py-3 capitalize text-slate-700">{srv.os}</td>
                      <td className="px-4 py-3 text-slate-700">{srv.arch}</td>
                      <td className="px-4 py-3 text-slate-700">{srv.agent_version || "—"}</td>
                      <td className="px-4 py-3">
                        {srv.manageable ? (
                          <Badge tone="info">agent channel</Badge>
                        ) : (
                          <span
                            title="Re-enroll the agent to enable remote management"
                            className="text-xs text-slate-400"
                          >
                            heartbeat only
                          </span>
                        )}
                      </td>
                      <td className="px-4 py-3 text-xs text-slate-600">
                        {srv.last_seen_at ? new Date(srv.last_seen_at).toLocaleString() : "never"}
                      </td>
                      <td className="px-6 py-3">
                        <div className="flex items-center justify-end gap-2">
                          <Badge tone={CONNECTION_META[state].tone}>
                            {CONNECTION_META[state].label}
                          </Badge>
                          <button
                            onClick={(e) => {
                              e.stopPropagation();
                              setDetail(srv);
                            }}
                            title="Probe capabilities & manage access"
                            className="focus-ring rounded-md px-2 py-1 text-xs text-slate-500 hover:bg-slate-100 hover:text-slate-700"
                          >
                            Probe
                          </button>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      {detail && (
        <ServerDetailModal
          server={detail}
          onClose={() => setDetail(null)}
          onRevoked={() => setDetail(null)}
          canRevoke={hasPermission("servers.delete")}
          revokePending={revokeMutation.isPending}
          onRevoke={() => {
            if (
              window.confirm(
                `Revoke ${detail.hostname}? The agent will no longer be able to connect until it re-enrolls.`,
              )
            ) {
              revokeMutation.mutate(detail.id);
            }
          }}
        />
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------

function ServerDetailModal({
  server,
  onClose,
  canRevoke,
  revokePending,
  onRevoke,
}: {
  server: ServerView;
  onClose: () => void;
  onRevoked: () => void;
  canRevoke: boolean;
  revokePending: boolean;
  onRevoke: () => void;
}) {
  const capsQuery = useQuery({
    queryKey: ["servers", server.id, "capabilities"],
    queryFn: () => serversApi.capabilities(server.id),
  });
  const caps: ServerCapabilities | undefined = capsQuery.data;

  return (
    <Modal title={server.label || server.hostname} onClose={onClose} wide>
      <dl className="grid grid-cols-2 gap-x-4 gap-y-3 text-sm">
        <Row label="Hostname" value={server.hostname} />
        <Row label="Operating system" value={`${server.os} ${server.os_version}`} />
        <Row label="Architecture" value={server.arch} />
        <Row label="Registered IP" value={server.registered_ip || "—"} />
        <Row label="Agent version" value={server.agent_version || "—"} />
        <Row
          label="Registered"
          value={new Date(server.registered_at).toLocaleString()}
        />
      </dl>

      <div className="mt-5 space-y-2 text-sm">
        {server.specs?.cpu ? (
          <p className="rounded-lg bg-slate-50 px-3 py-2 text-xs text-slate-600">
            CPU: {server.specs.cpu.cores_logical ?? "?"} logical cores
            {server.specs.cpu.model ? ` — ${server.specs.cpu.model}` : ""}
          </p>
        ) : (
          <NotConfigured what="CPU inventory" how="Awaiting agent report" />
        )}
        {server.specs?.memory && server.specs.memory.total_mb ? (
          <p className="rounded-lg bg-slate-50 px-3 py-2 text-xs text-slate-600">
            Memory: {(server.specs.memory.total_mb / 1024).toFixed(1)} GB total
          </p>
        ) : null}
        {(server.specs?.disks ?? []).map((d) => (
          <p key={d.mountpoint} className="rounded-lg bg-slate-50 px-3 py-2 text-xs text-slate-600">
            Disk {d.mountpoint}: {((d.free_mb ?? 0) / 1024).toFixed(1)} GB free of{" "}
            {((d.total_mb ?? 0) / 1024).toFixed(1)} GB
          </p>
        ))}
      </div>

      <div className="mt-5 rounded-xl border border-slate-200">
        <div className="flex items-center justify-between border-b border-slate-100 px-4 py-3">
          <h3 className="text-sm font-semibold text-slate-900">Server capabilities</h3>
          <Button
            size="sm"
            variant="ghost"
            loading={capsQuery.isFetching}
            onClick={() => void capsQuery.refetch()}
          >
            Refresh probe
          </Button>
        </div>
        <div className="px-4 py-3">
          {capsQuery.isLoading && <Spinner label="Probing…" />}
          {capsQuery.isError && (
            <p role="alert" className="text-xs text-red-600">
              The agent could not be probed. Check that it is running and reachable.
            </p>
          )}
          {caps && (
            <ul className="space-y-1.5 text-xs">
              <CapRow
                ok={caps.reachable}
                label="Agent management channel"
                detail={caps.reachable ? "reachable" : caps.error ?? "unreachable"}
              />
              <CapRow
                ok={!!caps.nginx?.installed}
                label="Nginx"
                detail={
                  caps.nginx?.installed
                    ? `${caps.nginx.version || "installed"} · ${caps.nginx.running ? "running" : "stopped"}`
                    : "not detected"
                }
              />
              {(caps.php ?? []).map((p) => (
                <CapRow
                  key={p.version}
                  ok={p.status !== "stopped"}
                  label={`PHP ${p.version}`}
                  detail={`${p.handler_type} · ${p.status}`}
                />
              ))}
              {(caps.php ?? []).length === 0 && caps.reachable && (
                <CapRow ok={false} label="PHP" detail="PHP is not installed on this server" />
              )}
              <CapRow
                ok={caps.provisioning}
                label="Website provisioning"
                detail={caps.provisioning ? "ready" : "requires nginx"}
              />
              <CapRow ok={caps.log_access} label="Log access" detail={caps.log_access ? "available" : "unavailable"} />
              <CapRow ok={false} label="SSL" detail="ships in a later phase" />
              <CapRow ok={false} label="DNS" detail="ships in a later phase" />
            </ul>
          )}
        </div>
      </div>

      {canRevoke && (
        <div className="mt-6 flex justify-end border-t border-slate-100 pt-4">
          <Button variant="danger" size="sm" loading={revokePending} onClick={onRevoke}>
            Revoke access
          </Button>
        </div>
      )}
    </Modal>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <>
      <dt className="text-slate-500">{label}</dt>
      <dd className="text-right font-medium text-slate-800">{value}</dd>
    </>
  );
}

function CapRow({ ok, label, detail }: { ok: boolean; label: string; detail: string }) {
  return (
    <li className="flex items-center justify-between gap-3">
      <span className="flex items-center gap-2 text-slate-700">
        <span aria-hidden className={ok ? "text-emerald-500" : "text-slate-300"}>
          {ok ? "✓" : "✗"}
        </span>
        {label}
      </span>
      <span className="text-slate-500">{detail}</span>
    </li>
  );
}

// ---------------------------------------------------------------------------
// Registration tokens
// ---------------------------------------------------------------------------

function RegistrationTokenPanel({ onClose }: { onClose: () => void }) {
  const toast = useToast();
  const tokensQuery = useQuery({
    queryKey: ["servers", "registration-tokens"],
    queryFn: serversApi.listTokens,
  });
  const [label, setLabel] = useState("");
  const [issued, setIssued] = useState<string | null>(null);

  const createMutation = useMutation({
    mutationFn: () => serversApi.createToken(label.trim(), 24),
    onSuccess: (data) => {
      setIssued(data.registration_token);
      setLabel("");
      void tokensQuery.refetch();
    },
    onError: (err) => toast.error(errMessage(err, "Token creation failed")),
  });

  const revokeMutation = useMutation({
    mutationFn: (id: string) => serversApi.revokeToken(id),
    onSuccess: () => {
      void tokensQuery.refetch();
      toast.success("Registration token revoked");
    },
    onError: (err) => toast.error(errMessage(err, "Revocation failed")),
  });

  const active: RegistrationTokenView[] = (tokensQuery.data?.tokens ?? []).filter(
    (t) => !t.used_at && !t.revoked_at && new Date(t.expires_at) > new Date(),
  );

  return (
    <Card>
      <CardBody>
        <div className="flex items-start justify-between">
          <div>
            <h2 className="text-sm font-semibold text-slate-900">Enroll a new server</h2>
            <p className="mt-1 max-w-2xl text-xs leading-relaxed text-slate-500">
              Registration tokens are single-use and expire after 24 hours. The full token is
              displayed exactly once — store it securely. Agents enrolled this way also receive the
              management channel needed for website provisioning.
            </p>
          </div>
          <button
            onClick={onClose}
            aria-label="Close instructions"
            className="focus-ring rounded-md p-1.5 text-slate-400 hover:bg-slate-100 hover:text-slate-600"
          >
            ✕
          </button>
        </div>

        <div className="mt-4 flex flex-wrap items-end gap-3">
          <div className="w-64">
            <Input
              label="Token label (optional)"
              placeholder="web-prod-01"
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              maxLength={128}
            />
          </div>
          <Button
            loading={createMutation.isPending}
            onClick={() => createMutation.mutate()}
            className="mb-0.5"
          >
            Create token
          </Button>
        </div>

        {createMutation.isError && (
          <p role="alert" className="mt-2 text-xs text-red-600">
            {errMessage(createMutation.error, "Token creation failed")}
          </p>
        )}

        {issued && (
          <div className="mt-4 space-y-2">
            <Alert tone="success" title="Registration token created">
              Copy it now — it will not be shown again.
            </Alert>
            <code className="block overflow-x-auto rounded-lg bg-slate-900 px-3 py-2.5 text-xs text-emerald-300">
              {issued}
            </code>
            <pre className="overflow-x-auto rounded-lg bg-slate-900 px-3 py-2.5 text-[11px] leading-relaxed text-slate-200"><code>{`# Linux
sudo epicpanel-agentd -url <panel-url> \\
  -key "${issued}" -label "web-01"

# Windows (as Administrator)
epicpanel-agentd.exe -url <panel-url> \`
  -key "${issued}" -label "win-01"`}</code></pre>
          </div>
        )}

        <div className="mt-5">
          <h3 className="text-xs font-semibold uppercase tracking-wide text-slate-500">
            Active tokens
          </h3>
          {tokensQuery.isLoading && <Spinner label="Loading tokens…" />}
          {active.length === 0 && !tokensQuery.isLoading && (
            <p className="mt-2 text-xs text-slate-500">No active registration tokens.</p>
          )}
          <ul className="mt-2 divide-y divide-slate-100">
            {active.map((t) => (
              <li key={t.id} className="flex items-center justify-between gap-3 py-2 text-sm">
                <div>
                  <p className="font-medium text-slate-800">{t.label || "unnamed token"}</p>
                  <p className="text-xs text-slate-500">
                    expires {new Date(t.expires_at).toLocaleString()}
                  </p>
                </div>
                <Button
                  size="sm"
                  variant="outline"
                  loading={revokeMutation.isPending && revokeMutation.variables === t.id}
                  onClick={() => revokeMutation.mutate(t.id)}
                >
                  Revoke
                </Button>
              </li>
            ))}
          </ul>
        </div>
      </CardBody>
    </Card>
  );
}

export function errMessage(err: unknown, fallback: string): string {
  if (err instanceof ApiError) return err.message;
  return fallback;
}

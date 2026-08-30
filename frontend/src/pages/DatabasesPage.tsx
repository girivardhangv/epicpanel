import { useMemo, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { databasesApi, serversApi, websitesApi } from "../services";
import { useAuth } from "../features/auth/AuthContext";
import { useToast } from "../components/ui/Toast";
import { errMessage } from "./ServersPage";
import type { DatabaseEngine, DatabaseView } from "../types/api";
import { Card, Badge } from "../components/ui/Card";
import { Button } from "../components/ui/Button";
import { Input } from "../components/ui/Input";
import { Modal } from "../components/ui/Modal";
import { ErrorState, EmptyState, Skeleton } from "../components/ui/States";
import { Database as DatabaseIcon } from "lucide-react";

const DB_NAME_RE = /^[a-z][a-z0-9_]{0,62}$/;

export function DatabaseStatusBadge({ status }: { status: DatabaseView["status"] }) {
  const tone =
    status === "active" ? "success" : status === "error" ? "danger" : status === "deleting" ? "danger" : "info";
  return (
    <span
      className={[
        "inline-flex items-center gap-1 rounded-md px-2 py-0.5 text-xs font-medium ring-1 ring-inset",
        tone === "success" && "bg-emerald-50 text-emerald-700 ring-emerald-600/20",
        tone === "danger" && "bg-red-50 text-red-700 ring-red-600/20",
        tone === "info" && "bg-indigo-50 text-indigo-700 ring-indigo-600/20",
      ]
        .filter(Boolean)
        .join(" ")}
    >
      {status === "provisioning" && (
        <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-indigo-500" aria-hidden />
      )}
      {status}
    </span>
  );
}

export function DatabasesPage() {
  const navigate = useNavigate();
  const { hasPermission } = useAuth();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [search, setSearch] = useState("");
  const [showCreate, setShowCreate] = useState(false);

  const listQuery = useQuery({ queryKey: ["databases"], queryFn: () => databasesApi.list() });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => databasesApi.delete(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["databases"] });
      toast.success("Database deletion started");
    },
    onError: (e) => toast.error(errMessage(e, "Delete failed")),
  });

  const databases = useMemo(() => {
    const all = listQuery.data?.databases ?? [];
    const q = search.trim().toLowerCase();
    return q ? all.filter((d) => d.name.toLowerCase().includes(q) || d.server_name.toLowerCase().includes(q)) : all;
  }, [listQuery.data, search]);

  return (
    <div className="mx-auto max-w-6xl space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold text-slate-900">Databases</h1>
          <p className="mt-0.5 text-sm text-slate-500">Managed MySQL and PostgreSQL databases.</p>
        </div>
        {hasPermission("databases.create") && (
          <Button size="sm" onClick={() => setShowCreate(true)}>
            Create database
          </Button>
        )}
      </div>

      <div className="flex items-center gap-3">
        <input
          type="search"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="Search by name or server…"
          aria-label="Search databases"
          className="w-64 rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm shadow-sm focus-ring"
        />
        <span className="text-xs text-slate-500">{databases.length} database{databases.length === 1 ? "" : "s"}</span>
      </div>

      {listQuery.isLoading ? (
        <Card>
          <div className="space-y-3 px-6 py-5">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-8 w-full" />
            ))}
          </div>
        </Card>
      ) : listQuery.isError ? (
        <ErrorState error={listQuery.error} onRetry={() => void listQuery.refetch()} />
      ) : databases.length === 0 ? (
        <EmptyState
          icon={<DatabaseIcon />}
          title={search ? "No databases match your search" : "No databases yet"}
          description="Create a MySQL or PostgreSQL database on one of your servers."
        />
      ) : (
        <Card>
          <div className="overflow-x-auto">
            <table className="w-full min-w-[680px] text-left text-sm">
              <thead>
                <tr className="border-b border-slate-100 text-xs uppercase tracking-wide text-slate-500">
                  <th className="px-6 py-3 font-medium">Name</th>
                  <th className="px-4 py-3 font-medium">Engine</th>
                  <th className="px-4 py-3 font-medium">Server</th>
                  <th className="px-4 py-3 font-medium">Status</th>
                  <th className="px-4 py-3 font-medium">Users</th>
                  <th className="px-6 py-3 text-right font-medium">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-50">
                {databases.map((d) => (
                  <tr key={d.id} className="transition-colors hover:bg-slate-50">
                    <td className="px-6 py-3">
                      <Link to={`/app/databases/${d.id}`} className="font-medium text-indigo-600 hover:text-indigo-800 focus-ring rounded">
                        {d.name}
                      </Link>
                    </td>
                    <td className="px-4 py-3">
                      <Badge tone="neutral">{d.engine}</Badge>
                    </td>
                    <td className="px-4 py-3 text-slate-700">{d.server_name}</td>
                    <td className="px-4 py-3">
                      <DatabaseStatusBadge status={d.status} />
                    </td>
                    <td className="px-4 py-3 text-slate-600">{d.users.length}</td>
                    <td className="px-6 py-3 text-right">
                      <div className="flex justify-end gap-1">
                        <Link to={`/app/databases/${d.id}`} className="focus-ring rounded-md px-2 py-1.5 text-xs text-slate-600 hover:bg-slate-100">
                          Manage
                        </Link>
                        {hasPermission("databases.delete") && (
                          <Button
                            size="sm"
                            variant="ghost"
                            loading={deleteMutation.isPending && deleteMutation.variables === d.id}
                            onClick={() => {
                              if (window.confirm(`Delete database ${d.name}? This drops it on the server.`)) {
                                deleteMutation.mutate(d.id);
                              }
                            }}
                          >
                            Delete
                          </Button>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      {showCreate && (
        <CreateDatabaseModal
          onClose={() => setShowCreate(false)}
          onCreated={(d) => {
            setShowCreate(false);
            void queryClient.invalidateQueries({ queryKey: ["databases"] });
            toast.success(`Database ${d.name} queued for provisioning`);
            navigate(`/app/databases/${d.id}`);
          }}
        />
      )}
    </div>
  );
}

function CreateDatabaseModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (d: DatabaseView) => void;
}) {
  const toast = useToast();
  const serversQuery = useQuery({ queryKey: ["servers"], queryFn: () => serversApi.list() });
  const [serverId, setServerId] = useState("");
  const [engine, setEngine] = useState<DatabaseEngine>("mysql");
  const [name, setName] = useState("");
  const [websiteId, setWebsiteId] = useState("");
  const [error, setError] = useState<string | null>(null);

  const servers = (serversQuery.data?.servers ?? []).filter((s) => s.manageable);
  const activeServer = serverId || servers[0]?.id || "";

  const enginesQuery = useQuery({
    queryKey: ["servers", activeServer, "db-engines"],
    queryFn: () => serversApi.dbEngines(activeServer),
    enabled: !!activeServer,
  });
  const websitesQuery = useQuery({
    queryKey: ["websites", "server", activeServer],
    queryFn: () => websitesApi.list({ server_id: activeServer }),
    enabled: !!activeServer,
  });

  const engines = enginesQuery.data;
  const mysqlAvailable = !!engines?.mysql?.available;
  const pgAvailable = !!engines?.postgres?.available;

  const create = useMutation({
    mutationFn: () =>
      databasesApi.create({
        server_id: activeServer,
        engine,
        name: name.trim(),
        website_id: websiteId || null,
      }),
    onSuccess: (res) => onCreated(res.database),
    onError: (e) => toast.error(errMessage(e, "Create failed")),
  });

  const submit = () => {
    if (!activeServer) return setError("Select a server.");
    if (!DB_NAME_RE.test(name.trim())) {
      return setError("Name must be lowercase letters, digits or underscore, start with a letter (max 63).");
    }
    setError(null);
    create.mutate();
  };

  return (
    <Modal title="Create database" onClose={onClose} wide>
      <div className="space-y-4">
        <div>
          <label htmlFor="db-server" className="mb-1.5 block text-sm font-medium text-slate-700">Server</label>
          <select
            id="db-server"
            value={activeServer}
            onChange={(e) => { setServerId(e.target.value); setWebsiteId(""); }}
            className="block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm shadow-sm focus-ring"
          >
            {servers.map((s) => (
              <option key={s.id} value={s.id}>{s.label || s.hostname} ({s.os})</option>
            ))}
          </select>
          {servers.length === 0 && <p className="mt-1.5 text-xs text-slate-500">No manageable servers.</p>}
        </div>

        <div>
          <p className="mb-1.5 text-sm font-medium text-slate-700">Engine</p>
          {enginesQuery.isLoading ? (
            <p className="text-xs text-slate-500">Detecting engines…</p>
          ) : !mysqlAvailable && !pgAvailable ? (
            <p className="text-xs text-amber-600">
              No database engine is available on this server. Configure the agent's MySQL/PostgreSQL admin credentials.
            </p>
          ) : (
            <div className="flex gap-2">
              {mysqlAvailable && (
                <EngineButton active={engine === "mysql"} onClick={() => setEngine("mysql")} label="MySQL / MariaDB" version={engines?.mysql?.version} />
              )}
              {pgAvailable && (
                <EngineButton active={engine === "postgres"} onClick={() => setEngine("postgres")} label="PostgreSQL" version={engines?.postgres?.version} />
              )}
            </div>
          )}
        </div>

        <Input
          label="Database name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="myapp_db"
          error={error ?? undefined}
          hint="Lowercase letters, digits and underscore; must start with a letter."
          autoComplete="off"
        />

        <div>
          <label htmlFor="db-website" className="mb-1.5 block text-sm font-medium text-slate-700">
            Attach to website <span className="font-normal text-slate-500">(optional)</span>
          </label>
          <select
            id="db-website"
            value={websiteId}
            onChange={(e) => setWebsiteId(e.target.value)}
            className="block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm shadow-sm focus-ring"
          >
            <option value="">None</option>
            {(websitesQuery.data?.websites ?? []).map((w) => (
              <option key={w.id} value={w.id}>{w.domain}</option>
            ))}
          </select>
        </div>

        <div className="flex justify-end gap-2 border-t border-slate-100 pt-4">
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button loading={create.isPending} disabled={!activeServer} onClick={submit}>
            Create database
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function EngineButton({ active, onClick, label, version }: { active: boolean; onClick: () => void; label: string; version?: string }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={[
        "rounded-lg border px-3 py-2.5 text-left text-sm transition-colors focus-ring",
        active ? "border-indigo-600 bg-indigo-50 font-medium text-indigo-700" : "border-slate-300 bg-white text-slate-700 hover:bg-slate-50",
      ].join(" ")}
    >
      <span className="block">{label}</span>
      {version && <span className="text-xs text-slate-500">{version}</span>}
    </button>
  );
}

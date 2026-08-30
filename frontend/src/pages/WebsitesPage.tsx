import { useMemo, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { websitesApi } from "../services";
import { useAuth } from "../features/auth/AuthContext";
import { useToast } from "../components/ui/Toast";
import { errMessage } from "./ServersPage";
import type { WebsiteStatus } from "../types/api";
import { Card } from "../components/ui/Card";
import { Button } from "../components/ui/Button";
import { ErrorState, EmptyState, Skeleton } from "../components/ui/States";
import { Globe } from "lucide-react";

const PAGE_SIZE = 10;

export function WebsiteStatusBadge({ status }: { status: WebsiteStatus }) {
  const tone =
    status === "active"
      ? "success"
      : status === "disabled"
        ? "neutral"
        : status === "error"
          ? "danger"
          : status === "deleting"
            ? "danger"
            : "info";
  return (
    <span
      className={[
        "inline-flex items-center gap-1 rounded-md px-2 py-0.5 text-xs font-medium ring-1 ring-inset",
        tone === "success" && "bg-emerald-50 text-emerald-700 ring-emerald-600/20",
        tone === "neutral" && "bg-slate-100 text-slate-600 ring-slate-500/20",
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

export function WebsitesPage() {
  const navigate = useNavigate();
  const { hasPermission } = useAuth();
  const toast = useToast();
  const queryClient = useQueryClient();

  const [search, setSearch] = useState("");
  const [status, setStatus] = useState<string>("");
  const [sortKey, setSortKey] = useState<"domain" | "created" | "status">("created");
  const [sortDir, setSortDir] = useState<"asc" | "desc">("desc");
  const [page, setPage] = useState(0);

  const listQuery = useQuery({
    queryKey: ["websites", search, status],
    queryFn: () => websitesApi.list({ search: search || undefined, status: status || undefined }),
  });

  const disableMutation = useMutation({
    mutationFn: (id: string) => websitesApi.disable(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["websites"] });
      toast.success("Website disabled");
    },
    onError: (err) => toast.error(errMessage(err, "Disable failed")),
  });

  const websites = useMemo(() => listQuery.data?.websites ?? [], [listQuery.data]);

  const sorted = useMemo(() => {
    const arr = [...websites];
    arr.sort((a, b) => {
      const dir = sortDir === "asc" ? 1 : -1;
      if (sortKey === "domain") return a.domain.localeCompare(b.domain) * dir;
      if (sortKey === "status") return a.status.localeCompare(b.status) * dir;
      return (new Date(a.created_at).getTime() - new Date(b.created_at).getTime()) * dir;
    });
    return arr;
  }, [websites, sortKey, sortDir]);

  const pageCount = Math.max(1, Math.ceil(sorted.length / PAGE_SIZE));
  const pageRows = sorted.slice(page * PAGE_SIZE, page * PAGE_SIZE + PAGE_SIZE);

  const toggleSort = (key: "domain" | "created" | "status") => {
    if (sortKey === key) {
      setSortDir((d) => (d === "asc" ? "desc" : "asc"));
    } else {
      setSortKey(key);
      setSortDir("asc");
    }
  };

  return (
    <div className="mx-auto max-w-6xl space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold text-slate-900">Websites</h1>
          <p className="mt-0.5 text-sm text-slate-500">
            Hosted sites provisioned through Nginx and PHP on your servers.
          </p>
        </div>
        {hasPermission("websites.create") && (
          <Button size="sm" onClick={() => navigate("/app/websites/new")}>
            Create website
          </Button>
        )}
      </div>

      <div className="flex flex-wrap items-center gap-3">
        <input
          type="search"
          value={search}
          onChange={(e) => {
            setSearch(e.target.value);
            setPage(0);
          }}
          placeholder="Search by domain…"
          aria-label="Search websites"
          className="w-64 rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm shadow-sm focus-ring"
        />
        <select
          value={status}
          onChange={(e) => {
            setStatus(e.target.value);
            setPage(0);
          }}
          aria-label="Filter by status"
          className="rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm shadow-sm focus-ring"
        >
          <option value="">All statuses</option>
          <option value="active">Active</option>
          <option value="disabled">Disabled</option>
          <option value="provisioning">Provisioning</option>
          <option value="error">Error</option>
        </select>
        <span className="text-xs text-slate-500">
          {sorted.length} website{sorted.length === 1 ? "" : "s"}
        </span>
      </div>

      {listQuery.isLoading ? (
        <Card>
          <div className="space-y-3 px-6 py-5">
            {Array.from({ length: 4 }).map((_, i) => (
              <Skeleton key={i} className="h-8 w-full" />
            ))}
          </div>
        </Card>
      ) : listQuery.isError ? (
        <ErrorState error={listQuery.error} onRetry={() => void listQuery.refetch()} />
      ) : sorted.length === 0 ? (
        <EmptyState
          icon={<Globe />}
          title={search || status ? "No websites match your filters" : "No websites yet"}
          description={
            search || status
              ? "Adjust the search or status filter."
              : "Create your first website to provision Nginx and PHP automatically."
          }
        />
      ) : (
        <Card>
          <div className="overflow-x-auto">
            <table className="w-full min-w-[760px] text-left text-sm">
              <thead>
                <tr className="border-b border-slate-100 text-xs uppercase tracking-wide text-slate-500">
                  <SortableTh label="Domain" active={sortKey === "domain"} dir={sortDir} onClick={() => toggleSort("domain")} />
                  <th className="px-4 py-3 font-medium">Server</th>
                  <SortableTh label="Status" active={sortKey === "status"} dir={sortDir} onClick={() => toggleSort("status")} />
                  <th className="px-4 py-3 font-medium">PHP</th>
                  <th className="px-4 py-3 font-medium">Web server</th>
                  <th className="px-4 py-3 font-medium">Disk</th>
                  <SortableTh label="Created" active={sortKey === "created"} dir={sortDir} onClick={() => toggleSort("created")} />
                  <th className="px-6 py-3 text-right font-medium">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-50">
                {pageRows.map((w) => (
                  <tr key={w.id} className="transition-colors hover:bg-slate-50">
                    <td className="px-6 py-3">
                      <Link
                        to={`/app/websites/${w.id}`}
                        className="font-medium text-indigo-600 hover:text-indigo-800 focus-ring rounded"
                      >
                        {w.domain}
                      </Link>
                      {w.aliases.length > 0 && (
                        <p className="text-xs text-slate-400">+{w.aliases.length} alias{w.aliases.length === 1 ? "" : "es"}</p>
                      )}
                    </td>
                    <td className="px-4 py-3 text-slate-700">{w.server_name || w.server_id.slice(0, 8)}</td>
                    <td className="px-4 py-3">
                      <WebsiteStatusBadge status={w.status} />
                    </td>
                    <td className="px-4 py-3 text-slate-700">{w.php_version || "—"}</td>
                    <td className="px-4 py-3 capitalize text-slate-700">{w.web_server}</td>
                    <td className="px-4 py-3 text-xs text-slate-400" title="Per-site disk usage ships later">
                      not tracked
                    </td>
                    <td className="px-4 py-3 text-xs text-slate-600">
                      {new Date(w.created_at).toLocaleDateString()}
                    </td>
                    <td className="px-6 py-3 text-right">
                      <div className="flex justify-end gap-1">
                        <a
                          href={`http://${w.domain}`}
                          target="_blank"
                          rel="noreferrer"
                          className="focus-ring rounded-md px-2 py-1.5 text-xs text-slate-600 hover:bg-slate-100"
                        >
                          Open
                        </a>
                        <Link
                          to={`/app/websites/${w.id}`}
                          className="focus-ring rounded-md px-2 py-1.5 text-xs text-slate-600 hover:bg-slate-100"
                        >
                          Manage
                        </Link>
                        {hasPermission("websites.edit") && w.status === "active" && (
                          <Button
                            size="sm"
                            variant="ghost"
                            loading={disableMutation.isPending && disableMutation.variables === w.id}
                            onClick={() => disableMutation.mutate(w.id)}
                          >
                            Disable
                          </Button>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {pageCount > 1 && (
            <div className="flex items-center justify-between border-t border-slate-100 px-6 py-3 text-xs text-slate-500">
              <span>
                Page {page + 1} of {pageCount}
              </span>
              <div className="flex gap-2">
                <Button size="sm" variant="outline" disabled={page === 0} onClick={() => setPage((p) => p - 1)}>
                  Previous
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={page >= pageCount - 1}
                  onClick={() => setPage((p) => p + 1)}
                >
                  Next
                </Button>
              </div>
            </div>
          )}
        </Card>
      )}
    </div>
  );
}

function SortableTh({
  label,
  active,
  dir,
  onClick,
}: {
  label: string;
  active: boolean;
  dir: "asc" | "desc";
  onClick: () => void;
}) {
  return (
    <th className="px-4 py-3 font-medium">
      <button
        onClick={onClick}
        className={[
          "focus-ring rounded font-medium",
          active ? "text-slate-800" : "text-slate-500 hover:text-slate-700",
        ].join(" ")}
      >
        {label}
        <span aria-hidden className="ml-1 text-[10px]">
          {active ? (dir === "asc" ? "▲" : "▼") : "↕"}
        </span>
      </button>
    </th>
  );
}

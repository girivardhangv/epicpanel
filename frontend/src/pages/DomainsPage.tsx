import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { domainsApi, serversApi } from "../services";
import { useAuth } from "../features/auth/AuthContext";
import { useToast } from "../components/ui/Toast";
import { errMessage } from "./ServersPage";
import type { DomainView } from "../types/api";
import { Card, Badge } from "../components/ui/Card";
import { Button } from "../components/ui/Button";
import { Input } from "../components/ui/Input";
import { Modal } from "../components/ui/Modal";
import { Spinner, ErrorState, EmptyState } from "../components/ui/States";

const DOMAIN_RE = /^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/;

/** Client-side pre-validation mirroring the server rules (UX only). */
export function validateDomainInput(raw: string, allowWildcard: boolean): string | null {
  const d = raw.trim().toLowerCase().replace(/\.$/, "");
  if (!d) return "A domain is required.";
  if (d.length > 253) return "Domain exceeds 253 characters.";
  if (!/^[a-z0-9.*-]+$/.test(d)) return "Only letters, digits, dots and hyphens are allowed.";
  if (d.includes("*")) {
    if (!allowWildcard) return "Wildcard domains are only allowed as aliases.";
    if (!d.startsWith("*.") || d.slice(2).includes("*")) return "Wildcard is only allowed as the leftmost label.";
  }
  if (!DOMAIN_RE.test(d)) return "Enter a valid hostname such as example.com.";
  return null;
}

export function DomainsPage() {
  const queryClient = useQueryClient();
  const { hasPermission } = useAuth();
  const toast = useToast();
  const [showCreate, setShowCreate] = useState(false);

  const domainsQuery = useQuery({ queryKey: ["domains"], queryFn: () => domainsApi.list() });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => domainsApi.delete(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["domains"] });
      toast.success("Domain deleted");
    },
    onError: (err) => toast.error(errMessage(err, "Delete failed")),
  });

  if (domainsQuery.isLoading) return <Spinner label="Loading domains…" />;
  if (domainsQuery.isError)
    return <ErrorState error={domainsQuery.error} onRetry={() => void domainsQuery.refetch()} />;

  const domains = domainsQuery.data?.domains ?? [];

  return (
    <div className="mx-auto max-w-6xl space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold text-slate-900">Domains</h1>
          <p className="mt-0.5 text-sm text-slate-500">
            Domain names available for websites on each server.
          </p>
        </div>
        {hasPermission("domains.create") && (
          <Button size="sm" onClick={() => setShowCreate(true)}>
            Add domain
          </Button>
        )}
      </div>

      {domains.length === 0 ? (
        <EmptyState
          title="No domains yet"
          description="Add the first domain to make it available for website creation."
        />
      ) : (
        <Card>
          <div className="overflow-x-auto">
            <table className="w-full min-w-[640px] text-left text-sm">
              <thead>
                <tr className="border-b border-slate-100 text-xs uppercase tracking-wide text-slate-500">
                  <th className="px-6 py-3 font-medium">Domain</th>
                  <th className="px-4 py-3 font-medium">Type</th>
                  <th className="px-4 py-3 font-medium">Website</th>
                  <th className="px-4 py-3 font-medium">Added</th>
                  <th className="px-6 py-3 text-right font-medium">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-50">
                {domains.map((d) => (
                  <DomainRow
                    key={d.id}
                    domain={d}
                    canDelete={hasPermission("domains.delete")}
                    deletePending={deleteMutation.isPending && deleteMutation.variables === d.id}
                    onDelete={() => {
                      if (window.confirm(`Delete domain ${d.domain}? This cannot be undone.`)) {
                        deleteMutation.mutate(d.id);
                      }
                    }}
                  />
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      {showCreate && (
        <CreateDomainModal
          onClose={() => setShowCreate(false)}
          onCreated={(d) => {
            setShowCreate(false);
            void queryClient.invalidateQueries({ queryKey: ["domains"] });
            toast.success(`Domain ${d.domain} added`);
          }}
        />
      )}
    </div>
  );
}

function DomainRow({
  domain,
  canDelete,
  deletePending,
  onDelete,
}: {
  domain: DomainView;
  canDelete: boolean;
  deletePending: boolean;
  onDelete: () => void;
}) {
  return (
    <tr className="transition-colors hover:bg-slate-50">
      <td className="px-6 py-3 font-medium text-slate-900">{domain.domain}</td>
      <td className="px-4 py-3">
        <Badge tone={domain.type === "primary" ? "info" : "neutral"}>{domain.type}</Badge>
      </td>
      <td className="px-4 py-3 text-slate-700">
        {domain.website_id ? (
          domain.website_name || "attached"
        ) : (
          <span className="text-slate-400">—</span>
        )}
      </td>
      <td className="px-4 py-3 text-xs text-slate-600">
        {new Date(domain.created_at).toLocaleDateString()}
      </td>
      <td className="px-6 py-3 text-right">
        {canDelete && (
          <Button
            size="sm"
            variant="ghost"
            disabled={!!domain.website_id}
            title={domain.website_id ? "Detach the domain from its website first" : "Delete domain"}
            loading={deletePending}
            onClick={onDelete}
          >
            Delete
          </Button>
        )}
      </td>
    </tr>
  );
}

function CreateDomainModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (d: DomainView) => void;
}) {
  const toast = useToast();
  const serversQuery = useQuery({ queryKey: ["servers"], queryFn: () => serversApi.list() });
  const [serverId, setServerId] = useState("");
  const [domain, setDomain] = useState("");
  const [type, setType] = useState<"primary" | "alias" | "subdomain">("primary");
  const [clientError, setClientError] = useState<string | null>(null);

  const servers = serversQuery.data?.servers ?? [];
  const activeServer = useMemo(() => serverId || servers[0]?.id || "", [serverId, servers]);

  const createMutation = useMutation({
    mutationFn: () => domainsApi.create({ server_id: activeServer, domain, type }),
    onSuccess: onCreated,
    onError: (err) => toast.error(errMessage(err, "Domain creation failed")),
  });

  const submit = () => {
    const err = validateDomainInput(domain, type === "alias");
    if (err) {
      setClientError(err);
      return;
    }
    if (!activeServer) {
      setClientError("Enroll a server first.");
      return;
    }
    setClientError(null);
    createMutation.mutate();
  };

  return (
    <Modal title="Add domain" onClose={onClose}>
      <div className="space-y-4">
        <div>
          <label htmlFor="domain-server" className="mb-1.5 block text-sm font-medium text-slate-700">
            Server
          </label>
          <select
            id="domain-server"
            value={activeServer}
            onChange={(e) => setServerId(e.target.value)}
            className="block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm shadow-sm focus-ring"
          >
            {servers.map((s) => (
              <option key={s.id} value={s.id}>
                {s.label || s.hostname} ({s.os})
              </option>
            ))}
          </select>
          {servers.length === 0 && (
            <p className="mt-1.5 text-xs text-slate-500">No servers enrolled yet.</p>
          )}
        </div>

        <Input
          label="Domain"
          placeholder="example.com"
          value={domain}
          onChange={(e) => setDomain(e.target.value)}
          error={clientError ?? undefined}
          hint="Normalized to lowercase. Wildcards (*.example.com) are allowed for aliases only."
          autoComplete="off"
        />

        <div>
          <p className="mb-1.5 text-sm font-medium text-slate-700">Type</p>
          <div className="flex gap-2">
            {(["primary", "subdomain", "alias"] as const).map((t) => (
              <button
                key={t}
                type="button"
                onClick={() => setType(t)}
                className={[
                  "rounded-lg border px-3 py-1.5 text-sm capitalize transition-colors focus-ring",
                  type === t
                    ? "border-indigo-600 bg-indigo-50 font-medium text-indigo-700"
                    : "border-slate-300 bg-white text-slate-600 hover:bg-slate-50",
                ].join(" ")}
              >
                {t}
              </button>
            ))}
          </div>
        </div>

        <div className="flex justify-end gap-2 border-t border-slate-100 pt-4">
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button loading={createMutation.isPending} onClick={submit}>
            Add domain
          </Button>
        </div>
      </div>
    </Modal>
  );
}

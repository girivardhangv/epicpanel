import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { dnsApi } from "../services";
import { useAuth } from "../features/auth/AuthContext";
import { useToast } from "../components/ui/Toast";
import { errMessage } from "./ServersPage";
import type { DNSZone } from "../types/api";
import { Card, Badge } from "../components/ui/Card";
import { Button } from "../components/ui/Button";
import { Modal } from "../components/ui/Modal";
import { Spinner, ErrorState, EmptyState } from "../components/ui/States";

export function DNSPage() {
  const queryClient = useQueryClient();
  const { hasPermission } = useAuth();
  const toast = useToast();
  const [showCreate, setShowCreate] = useState(false);

  const zonesQuery = useQuery({ queryKey: ["dns", "zones"], queryFn: () => dnsApi.listZones() });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => dnsApi.deleteZone(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["dns", "zones"] });
      toast.success("Zone deleted");
    },
    onError: (err) => toast.error(errMessage(err, "Delete failed")),
  });

  if (zonesQuery.isLoading) return <Spinner label="Loading zones…" />;
  if (zonesQuery.isError)
    return <ErrorState error={zonesQuery.error} onRetry={() => void zonesQuery.refetch()} />;

  const zones = zonesQuery.data?.zones ?? [];

  return (
    <div className="mx-auto max-w-6xl space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold text-slate-900">DNS Zones</h1>
          <p className="mt-0.5 text-sm text-slate-500">
            Managed DNS zones synced to Cloudflare or your provider.
          </p>
        </div>
        {hasPermission("domains.create") && (
          <Button size="sm" onClick={() => setShowCreate(true)}>
            Add zone
          </Button>
        )}
      </div>

      {zones.length === 0 ? (
        <EmptyState
          title="No DNS zones"
          description="Add a zone to manage DNS records for a domain."
        />
      ) : (
        <Card>
          <div className="overflow-x-auto">
            <table className="w-full min-w-[640px] text-left text-sm">
              <thead>
                <tr className="border-b border-slate-100 text-xs uppercase tracking-wide text-slate-500">
                  <th className="px-6 py-3 font-medium">Domain</th>
                  <th className="px-4 py-3 font-medium">Provider</th>
                  <th className="px-4 py-3 font-medium">Status</th>
                  <th className="px-4 py-3 font-medium">Created</th>
                  <th className="px-6 py-3 text-right font-medium">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-50">
                {zones.map((z) => (
                  <ZoneRow
                    key={z.id}
                    zone={z}
                    canDelete={hasPermission("domains.delete")}
                    deletePending={deleteMutation.isPending && deleteMutation.variables === z.id}
                    onDelete={() => {
                      if (window.confirm(`Delete zone ${z.domain}? Records will not be removed from the provider.`)) {
                        deleteMutation.mutate(z.id);
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
        <CreateZoneModal
          onClose={() => setShowCreate(false)}
          onCreated={() => {
            setShowCreate(false);
            void queryClient.invalidateQueries({ queryKey: ["dns", "zones"] });
          }}
        />
      )}
    </div>
  );
}

function ZoneRow({
  zone,
  canDelete,
  deletePending,
  onDelete,
}: {
  zone: DNSZone;
  canDelete: boolean;
  deletePending: boolean;
  onDelete: () => void;
}) {
  return (
    <tr className="transition-colors hover:bg-slate-50">
      <td className="px-6 py-3 font-medium text-slate-900">
        <Link to={`/app/dns/zones/${zone.id}`} className="hover:text-indigo-600">
          {zone.domain}
        </Link>
      </td>
      <td className="px-4 py-3 text-slate-700">{zone.provider}</td>
      <td className="px-4 py-3">
        <Badge
          tone={
            zone.status === "synced" ? "success" : zone.status === "error" ? "danger" : "warning"
          }
        >
          {zone.status}
        </Badge>
      </td>
      <td className="px-4 py-3 text-xs text-slate-600">
        {new Date(zone.created_at).toLocaleDateString()}
      </td>
      <td className="px-6 py-3 text-right">
        {canDelete && (
          <Button size="sm" variant="ghost" loading={deletePending} onClick={onDelete}>
            Delete
          </Button>
        )}
      </td>
    </tr>
  );
}

function CreateZoneModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: () => void;
}) {
  const toast = useToast();
  const [domain, setDomain] = useState("");

  const createMutation = useMutation({
    mutationFn: () => dnsApi.createZone(domain),
    onSuccess: onCreated,
    onError: (err) => toast.error(errMessage(err, "Zone creation failed")),
  });

  return (
    <Modal title="Add DNS zone" onClose={onClose}>
      <div className="space-y-4">
        <div>
          <label className="mb-1.5 block text-sm font-medium text-slate-700">Domain</label>
          <input
            value={domain}
            onChange={(e) => setDomain(e.target.value)}
            placeholder="example.com"
            className="block w-full rounded-lg border border-slate-300 px-3 py-2 text-sm shadow-sm focus-ring"
          />
          <p className="mt-1.5 text-xs text-slate-500">
            The zone will be created in the panel and synced to the provider.
          </p>
        </div>
        <div className="flex justify-end gap-2 border-t border-slate-100 pt-4">
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button loading={createMutation.isPending} disabled={!domain.trim()} onClick={() => createMutation.mutate()}>
            Add zone
          </Button>
        </div>
      </div>
    </Modal>
  );
}
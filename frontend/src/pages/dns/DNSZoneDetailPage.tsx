import { useState } from "react";
import { useParams, useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { dnsApi } from "../../services";
import { useAuth } from "../../features/auth/AuthContext";
import { useToast } from "../../components/ui/Toast";
import { errMessage } from "../ServersPage";
import type { DNSRecord, DNSZone } from "../../types/api";
import { Card, Badge } from "../../components/ui/Card";
import { Button } from "../../components/ui/Button";
import { Modal } from "../../components/ui/Modal";
import { Spinner, ErrorState, EmptyState } from "../../components/ui/States";
import { Alert } from "../../components/ui/Alert";

const TYPES = ["A", "AAAA", "CNAME", "MX", "TXT", "NS", "SRV"] as const;
type RecordType = (typeof TYPES)[number];

export function DNSZoneDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { hasPermission } = useAuth();
  const toast = useToast();
  const [showCreate, setShowCreate] = useState(false);

  const zoneQuery = useQuery({
    queryKey: ["dns", "zones", id],
    queryFn: () => dnsApi.getZone(id),
    enabled: !!id,
  });
  const recordsQuery = useQuery({
    queryKey: ["dns", "zones", id, "records"],
    queryFn: () => dnsApi.listRecords(id),
    enabled: !!id,
  });

  const syncMutation = useMutation({
    mutationFn: () => dnsApi.syncZone(id),
    onSuccess: () => {
      toast.success("Zone synced to provider");
      void queryClient.invalidateQueries({ queryKey: ["dns", "zones", id, "records"] });
      void queryClient.invalidateQueries({ queryKey: ["dns", "zones", id] });
    },
    onError: (err) => toast.error(errMessage(err, "Sync failed")),
  });

  const deleteMutation = useMutation({
    mutationFn: (rid: string) => dnsApi.deleteRecord(rid),
    onSuccess: () => {
      toast.success("Record deleted");
      void queryClient.invalidateQueries({ queryKey: ["dns", "zones", id, "records"] });
    },
    onError: (err) => toast.error(errMessage(err, "Delete failed")),
  });

  if (zoneQuery.isLoading || recordsQuery.isLoading) return <Spinner label="Loading zone…" />;
  if (zoneQuery.isError) return <ErrorState error={zoneQuery.error} onRetry={() => void zoneQuery.refetch()} />;
  if (recordsQuery.isError)
    return <ErrorState error={recordsQuery.error} onRetry={() => void recordsQuery.refetch()} />;

  const zone = zoneQuery.data!.zone!;
  const records = recordsQuery.data?.records ?? [];

  return (
    <div className="mx-auto max-w-6xl space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <button className="mb-1 text-xs font-medium text-slate-500 hover:text-indigo-600" onClick={() => navigate("/app/dns")}>
            ← All zones
          </button>
          <h1 className="text-xl font-semibold text-slate-900">{zone.domain}</h1>
          <p className="mt-0.5 text-sm text-slate-500">
            Provider: {zone.provider} · Status: <Badge tone={zone.status === "synced" ? "success" : "warning"}>{zone.status}</Badge>
          </p>
        </div>
        <div className="flex gap-2">
          {hasPermission("domains.manage") && (
            <Button size="sm" variant="outline" loading={syncMutation.isPending} onClick={() => syncMutation.mutate()}>
              Sync to provider
            </Button>
          )}
          {hasPermission("domains.manage") && (
            <Button size="sm" onClick={() => setShowCreate(true)}>
              Add record
            </Button>
          )}
        </div>
      </div>

      {zone.error && (
        <Alert tone="warning" title="Last sync error">{zone.error}</Alert>
      )}

      {records.length === 0 ? (
        <EmptyState
          title="No records"
          description="Add A, AAAA, CNAME, MX, TXT, NS or SRV records for this zone."
        />
      ) : (
        <Card>
          <div className="overflow-x-auto">
            <table className="w-full min-w-[720px] text-left text-sm">
              <thead>
                <tr className="border-b border-slate-100 text-xs uppercase tracking-wide text-slate-500">
                  <th className="px-6 py-3 font-medium">Name</th>
                  <th className="px-4 py-3 font-medium">Type</th>
                  <th className="px-4 py-3 font-medium">Value</th>
                  <th className="px-4 py-3 font-medium">TTL</th>
                  <th className="px-4 py-3 font-medium">Status</th>
                  <th className="px-6 py-3 text-right font-medium">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-50">
                {records.map((r) => (
                  <RecordRow
                    key={r.id}
                    record={r}
                    canDelete={hasPermission("domains.manage")}
                    deletePending={deleteMutation.isPending && deleteMutation.variables === r.id}
                    onDelete={() => {
                      if (window.confirm(`Delete ${r.name || "@"} ${r.type} record?`)) {
                        deleteMutation.mutate(r.id);
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
        <CreateRecordModal
          zone={zone}
          onClose={() => setShowCreate(false)}
          onCreated={() => {
            setShowCreate(false);
            void queryClient.invalidateQueries({ queryKey: ["dns", "zones", id, "records"] });
          }}
        />
      )}
    </div>
  );
}

function RecordRow({
  record,
  canDelete,
  deletePending,
  onDelete,
}: {
  record: DNSRecord;
  canDelete: boolean;
  deletePending: boolean;
  onDelete: () => void;
}) {
  return (
    <tr className="transition-colors hover:bg-slate-50">
      <td className="px-6 py-3 font-medium text-slate-900">{record.name || "@"}</td>
      <td className="px-4 py-3">
        <Badge tone={record.type === "CNAME" ? "info" : "neutral"}>{record.type}</Badge>
      </td>
      <td className="max-w-xs truncate px-4 py-3 text-slate-700" title={record.value}>
        {record.value}
      </td>
      <td className="px-4 py-3 text-xs text-slate-600">{record.ttl}s</td>
      <td className="px-4 py-3">
        <Badge
          tone={record.status === "synced" ? "success" : record.status === "error" ? "danger" : "warning"}
        >
          {record.status}
        </Badge>
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

function CreateRecordModal({
  zone,
  onClose,
  onCreated,
}: {
  zone: DNSZone;
  onClose: () => void;
  onCreated: () => void;
}) {
  const toast = useToast();
  const [type, setType] = useState<RecordType>("A");
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [priority, setPriority] = useState(10);
  const [ttl, setTtl] = useState(300);

  const createMutation = useMutation({
    mutationFn: () =>
      dnsApi.createRecord(zone.id, {
        name: name.trim(),
        type,
        value: value.trim(),
        priority,
        ttl,
      }),
    onSuccess: onCreated,
    onError: (err) => toast.error(errMessage(err, "Record creation failed")),
  });

  return (
    <Modal title={`Add ${type} record`} onClose={onClose}>
      <div className="space-y-4">
        <div>
          <label className="mb-1.5 block text-sm font-medium text-slate-700">Type</label>
          <div className="flex flex-wrap gap-1.5">
            {TYPES.map((t) => (
              <button
                key={t}
                type="button"
                onClick={() => setType(t)}
                className={[
                  "rounded-md border px-2.5 py-1 text-xs font-medium transition-colors focus-ring",
                  type === t
                    ? "border-indigo-600 bg-indigo-50 text-indigo-700"
                    : "border-slate-300 bg-white text-slate-600 hover:bg-slate-50",
                ].join(" ")}
              >
                {t}
              </button>
            ))}
          </div>
        </div>

        <div>
          <label className="mb-1.5 block text-sm font-medium text-slate-700">
            Name <span className="font-normal text-slate-400">(leave empty for @)</span>
          </label>
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="www"
            className="block w-full rounded-lg border border-slate-300 px-3 py-2 text-sm shadow-sm focus-ring"
          />
        </div>

        <div>
          <label className="mb-1.5 block text-sm font-medium text-slate-700">Value</label>
          <input
            value={value}
            onChange={(e) => setValue(e.target.value)}
            placeholder={type === "A" ? "192.0.2.1" : "target.example.com"}
            className="block w-full rounded-lg border border-slate-300 px-3 py-2 text-sm shadow-sm focus-ring"
          />
        </div>

        {(type === "MX" || type === "SRV") && (
          <div>
            <label className="mb-1.5 block text-sm font-medium text-slate-700">Priority</label>
            <input
              type="number"
              value={priority}
              min={0}
              max={65535}
              onChange={(e) => setPriority(Number(e.target.value) || 0)}
              className="block w-full rounded-lg border border-slate-300 px-3 py-2 text-sm shadow-sm focus-ring"
            />
          </div>
        )}

        <div>
          <label className="mb-1.5 block text-sm font-medium text-slate-700">TTL (seconds)</label>
          <input
            type="number"
            value={ttl}
            min={60}
            max={86400}
            step={60}
            onChange={(e) => setTtl(Number(e.target.value) || 300)}
            className="block w-full rounded-lg border border-slate-300 px-3 py-2 text-sm shadow-sm focus-ring"
          />
        </div>

        <div className="flex justify-end gap-2 border-t border-slate-100 pt-4">
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button loading={createMutation.isPending} disabled={!value.trim()} onClick={() => createMutation.mutate()}>
            Add record
          </Button>
        </div>
      </div>
    </Modal>
  );
}
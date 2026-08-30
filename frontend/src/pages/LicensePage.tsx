import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { licenseApi } from "../services";
import type { LicenseInfo } from "../types/api";
import { Card, CardBody, CardHeader, Badge } from "../components/ui/Card";
import { Button } from "../components/ui/Button";
import { Alert } from "../components/ui/Alert";
import { Spinner, ErrorState, EmptyState } from "../components/ui/States";

export function LicensePage() {
  const queryClient = useQueryClient();
  const [notice, setNotice] = useState<{ tone: "success" | "danger"; text: string } | null>(null);

  const statusQuery = useQuery({
    queryKey: ["license", "status"],
    queryFn: licenseApi.status,
  });

  const refreshMutation = useMutation({
    mutationFn: () => licenseApi.refresh(),
    onSuccess: (res) => {
      if (res.error_message) {
        setNotice({ tone: "danger", text: res.error_message });
      } else {
        setNotice({ tone: "success", text: "License validated with the licensing server." });
      }
      void queryClient.invalidateQueries({ queryKey: ["license"] });
      void queryClient.invalidateQueries({ queryKey: ["dashboard"] });
    },
    onError: (err) => setNotice({ tone: "danger", text: messageOf(err) }),
  });

  const deactivateMutation = useMutation({
    mutationFn: () => licenseApi.deactivate(),
    onSuccess: () => {
      setNotice({
        tone: "success",
        text: "License deactivated. This panel cannot operate until a new license is activated.",
      });
      void queryClient.invalidateQueries();
    },
    onError: (err) => setNotice({ tone: "danger", text: messageOf(err) }),
  });

  if (statusQuery.isLoading) return <Spinner label="Reading license state…" />;
  if (statusQuery.isError)
    return <ErrorState error={statusQuery.error} onRetry={() => void statusQuery.refetch()} />;

  const lic: LicenseInfo = statusQuery.data!;

  const tone =
    lic.status === "active"
      ? ("success" as const)
      : lic.status === "grace"
        ? ("warning" as const)
        : lic.status === "inactive"
          ? ("neutral" as const)
          : ("danger" as const);

  return (
    <div className="mx-auto max-w-3xl space-y-6">
      <div>
        <h1 className="text-xl font-semibold text-slate-900">License</h1>
        <p className="mt-0.5 text-sm text-slate-500">
          Commercial licensing for this EpicPanel installation.
        </p>
      </div>

      {notice && (
        <Alert
          tone={notice.tone}
          title={
            notice.tone === "danger" && notice.text.includes("unreachable")
              ? "Licensing server unreachable"
              : undefined
          }
        >
          {notice.text}
        </Alert>
      )}

      {lic.status === "grace" && (
        <Alert tone="warning" title="Operating in grace period">
          The licensing server could not be reached during the last validation. The panel remains
          usable until the configured grace window elapses; no action is needed unless the outage
          continues.
        </Alert>
      )}

      <Card>
        <CardHeader title="Current license" />
        <CardBody>
          {lic.status === "inactive" ? (
            <EmptyState
              title="No active license stored"
              description="Activate a license through the first-run installer. After activation you can validate or deactivate it here."
            />
          ) : (
            <>
              <div className="flex items-center gap-3">
                <span className="text-2xl font-semibold capitalize text-slate-900">
                  {lic.plan || "EpicPanel"}
                </span>
                <Badge tone={tone}>{lic.status}</Badge>
              </div>

              <dl className="mt-5 grid grid-cols-2 gap-x-6 gap-y-3 text-sm">
                <Row label="Key hint" value={lic.key_hint || "—"} mono />
                <Row label="License ID" value={lic.external_id || "—"} mono />
                <Row
                  label="Activated"
                  value={lic.activated_at ? new Date(lic.activated_at).toLocaleString() : "—"}
                />
                <Row
                  label="Last validated"
                  value={
                    lic.last_validated_at ? new Date(lic.last_validated_at).toLocaleString() : "—"
                  }
                />
                <Row
                  label="Expires"
                  value={lic.expires_at ? new Date(lic.expires_at).toLocaleDateString() : "never"}
                />
                <Row label="Seats" value={lic.seats != null ? String(lic.seats) : "—"} />
                <Row label="Fingerprint" value={lic.fingerprint || "—"} mono />
                <Row label="Features" value={(lic.features ?? []).join(", ") || "—"} />
              </dl>

              <div className="mt-6 flex justify-end gap-2 border-t border-slate-100 pt-4">
                <Button
                  variant="outline"
                  size="sm"
                  loading={refreshMutation.isPending}
                  onClick={() => refreshMutation.mutate()}
                >
                  Validate now
                </Button>
                <Button
                  variant="danger"
                  size="sm"
                  loading={deactivateMutation.isPending}
                  onClick={() => {
                    if (
                      window.confirm(
                        "Deactivate this license? The panel will stop operating until it is reactivated.",
                      )
                    )
                      deactivateMutation.mutate();
                  }}
                >
                  Deactivate
                </Button>
              </div>
            </>
          )}
        </CardBody>
      </Card>
    </div>
  );
}

function messageOf(err: unknown): string {
  return err instanceof Error ? err.message : "Operation failed";
}

function Row({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <>
      <dt className="text-slate-500">{label}</dt>
      <dd
        className={`break-all text-right ${mono ? "font-mono text-xs" : ""} text-slate-800`}
      >
        {value}
      </dd>
    </>
  );
}

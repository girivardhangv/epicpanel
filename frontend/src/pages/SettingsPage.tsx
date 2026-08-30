import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { notificationsApi, settingsApi } from "../services";
import { useToast } from "../components/ui/Toast";
import { errMessage } from "./ServersPage";
import type { NotificationChannel } from "../types/api";
import { Card, CardBody, CardHeader, Badge } from "../components/ui/Card";
import { Button } from "../components/ui/Button";
import { Input } from "../components/ui/Input";
import { Modal } from "../components/ui/Modal";
import { Spinner, ErrorState } from "../components/ui/States";
import { Alert } from "../components/ui/Alert";

// ---------------------------------------------------------------------------
// Settings page (Phase 4 + 5 operator configuration)
// ---------------------------------------------------------------------------

export function SettingsPage() {
  return (
    <div className="mx-auto max-w-4xl space-y-6">
      <div>
        <h1 className="text-xl font-semibold text-slate-900">Settings</h1>
        <p className="mt-0.5 text-sm text-slate-500">
          SSL/ACME defaults and alert notification channels.
        </p>
      </div>

      <ACMESettingsCard />
      <NotificationChannelsCard />
    </div>
  );
}

const ACME_FIELDS = [
  { key: "ssl.acme_mode", label: "ACME mode", hint: "production | staging | mock (mock = self-signed, for development)" },
  { key: "ssl.acme_email", label: "ACME account email", hint: "Used as the Let's Encrypt account contact" },
  { key: "ssl.auto_renew_days", label: "Auto-renew days", hint: "Renew certificates this many days before expiry" },
] as const;

function ACMESettingsCard() {
  const toast = useToast();
  const queryClient = useQueryClient();
  const q = useQuery({ queryKey: ["settings"], queryFn: settingsApi.get });
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [saved, setSaved] = useState(false);

  const save = useMutation({
    mutationFn: () => settingsApi.patch(draft),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["settings"] });
      setDraft({});
      setSaved(true);
      window.setTimeout(() => setSaved(false), 3000);
      toast.success("Settings saved");
    },
    onError: (e) => toast.error(errMessage(e, "Save failed")),
  });

  if (q.isLoading) return <Spinner label="Loading settings…" />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;

  const settings = q.data?.settings ?? {};

  return (
    <Card>
      <CardHeader
        title="SSL / ACME"
        subtitle="Defaults applied when certificates are issued"
        actions={
          <Button
            size="sm"
            loading={save.isPending}
            disabled={Object.keys(draft).length === 0}
            onClick={() => save.mutate()}
          >
            Save
          </Button>
        }
      />
      <CardBody className="space-y-4">
        {saved && <Alert tone="success" title="Saved" />}
        {ACME_FIELDS.map((f) => {
          const current = settings[f.key];
          const value = draft[f.key] ?? String(current ?? "");
          return (
            <Input
              key={f.key}
              label={f.label}
              hint={f.hint}
              value={value}
              onChange={(e) => setDraft((d) => ({ ...d, [f.key]: e.target.value }))}
            />
          );
        })}
        <p className="text-xs text-slate-400">
          Tip: use mode <code className="font-mono">mock</code> on a development panel without
          public DNS — certificates will be self-signed so you can test the full flow.
        </p>
      </CardBody>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Notification channels
// ---------------------------------------------------------------------------

function NotificationChannelsCard() {
  const q = useQuery({ queryKey: ["notifications"], queryFn: notificationsApi.list });
  const queryClient = useQueryClient();
  const toast = useToast();
  const [open, setOpen] = useState(false);

  const del = useMutation({
    mutationFn: (id: string) => notificationsApi.remove(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["notifications"] });
      toast.success("Channel removed");
    },
    onError: (e) => toast.error(errMessage(e, "Remove failed")),
  });

  const test = useMutation({
    mutationFn: (id: string) => notificationsApi.test(id),
    onSuccess: () => toast.success("Test message sent"),
    onError: (e) => toast.error(errMessage(e, "Test delivery failed")),
  });

  return (
    <Card>
      <CardHeader
        title="Notification channels"
        subtitle="Where alert notifications are delivered"
        actions={
          <Button size="sm" onClick={() => setOpen(true)}>
            Add channel
          </Button>
        }
      />
      <CardBody>
        {q.isLoading && <Spinner label="Loading channels…" />}
        {q.isError && <ErrorState error={q.error} onRetry={() => void q.refetch()} />}
        {q.data && q.data.channels.length === 0 && (
          <p className="text-sm text-slate-500">
            No channels yet. Add a webhook, Slack, Discord or email channel to start receiving
            alert notifications.
          </p>
        )}
        {q.data && q.data.channels.length > 0 && (
          <ul className="divide-y divide-slate-100">
            {q.data.channels.map((ch) => (
              <li key={ch.id} className="flex items-center justify-between gap-3 py-3">
                <div>
                  <div className="flex items-center gap-2">
                    <p className="text-sm font-medium text-slate-800">{ch.name}</p>
                    <Badge tone="neutral">{ch.type}</Badge>
                    <Badge tone={ch.enabled ? "success" : "neutral"}>
                      {ch.enabled ? "enabled" : "disabled"}
                    </Badge>
                    <Badge tone={ch.severity === "critical" ? "danger" : "warning"}>
                      {ch.severity}+
                    </Badge>
                  </div>
                  <p className="mt-0.5 text-xs text-slate-500">
                    {channelSummary(ch)}
                  </p>
                </div>
                <div className="flex shrink-0 gap-2">
                  <Button size="sm" variant="outline" loading={test.isPending && test.variables === ch.id} onClick={() => test.mutate(ch.id)}>
                    Test
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    loading={del.isPending && del.variables === ch.id}
                    onClick={() => {
                      if (window.confirm(`Delete channel ${ch.name}?`)) del.mutate(ch.id);
                    }}
                  >
                    Delete
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        )}
      </CardBody>

      {open && (
        <ChannelModal
          onClose={() => setOpen(false)}
          onCreated={() => {
            setOpen(false);
            void queryClient.invalidateQueries({ queryKey: ["notifications"] });
            toast.success("Channel created");
          }}
        />
      )}
    </Card>
  );
}

function channelSummary(ch: NotificationChannel): string {
  switch (ch.type) {
    case "email":
      return `to ${ch.config.to ?? "—"} via ${ch.config.smtp_host ?? "—"}`;
    case "slack":
    case "discord":
    case "webhook": {
      const url = (ch.config.webhook_url as string) ?? "";
      return url ? `→ ${url.slice(0, 48)}${url.length > 48 ? "…" : ""}` : "";
    }
    default:
      return "";
  }
}

function ChannelModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: () => void;
}) {
  const toast = useToast();
  const [name, setName] = useState("");
  const [type, setType] = useState<NotificationChannel["type"]>("webhook");
  const [severity, setSeverity] = useState<"warning" | "critical">("warning");
  const [webhookURL, setWebhookURL] = useState("");
  const [enabled, setEnabled] = useState(true);

  // email fields
  const [smtpHost, setSmtpHost] = useState("");
  const [smtpPort, setSmtpPort] = useState("587");
  const [smtpUser, setSmtpUser] = useState("");
  const [smtpPass, setSmtpPass] = useState("");
  const [fromAddr, setFromAddr] = useState("");
  const [toAddr, setToAddr] = useState("");

  const create = useMutation({
    mutationFn: () =>
      notificationsApi.create({
        name: name.trim() || (type === "email" ? "SMTP" : "Webhook"),
        type,
        config:
          type === "email"
            ? {
                smtp_host: smtpHost,
                smtp_port: Number(smtpPort) || 587,
                smtp_username: smtpUser,
                smtp_password: smtpPass,
                from: fromAddr,
                to: toAddr,
              }
            : { webhook_url: webhookURL.trim() },
        severity,
        enabled,
      }),
    onSuccess: onCreated,
    onError: (e) => toast.error(errMessage(e, "Create failed")),
  });

  return (
    <Modal title="Add notification channel" onClose={onClose} wide>
      <div className="space-y-4">
        <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} placeholder="on-call webhook" />
        <div>
          <p className="mb-1.5 text-sm font-medium text-slate-700">Type</p>
          <div className="flex flex-wrap gap-2">
            {(["webhook", "slack", "discord", "email"] as const).map((t) => (
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

        {type === "email" ? (
          <div className="grid gap-3 sm:grid-cols-2">
            <Input label="SMTP host" value={smtpHost} onChange={(e) => setSmtpHost(e.target.value)} placeholder="smtp.example.com" />
            <Input label="SMTP port" value={smtpPort} onChange={(e) => setSmtpPort(e.target.value)} />
            <Input label="Username" value={smtpUser} onChange={(e) => setSmtpUser(e.target.value)} />
            <Input label="Password" type="password" value={smtpPass} onChange={(e) => setSmtpPass(e.target.value)} />
            <Input label="From" value={fromAddr} onChange={(e) => setFromAddr(e.target.value)} placeholder="alerts@panel" />
            <Input label="To" value={toAddr} onChange={(e) => setToAddr(e.target.value)} placeholder="ops@example.com" />
          </div>
        ) : (
          <Input
            label={type === "slack" ? "Slack incoming webhook URL" : type === "discord" ? "Discord webhook URL" : "Webhook URL"}
            value={webhookURL}
            onChange={(e) => setWebhookURL(e.target.value)}
            placeholder="https://hooks.slack.com/services/..."
          />
        )}

        <div>
          <p className="mb-1.5 text-sm font-medium text-slate-700">Minimum severity</p>
          <div className="flex gap-2">
            {(["warning", "critical"] as const).map((s) => (
              <button
                key={s}
                type="button"
                onClick={() => setSeverity(s)}
                className={[
                  "rounded-lg border px-3 py-1.5 text-sm capitalize transition-colors focus-ring",
                  severity === s
                    ? "border-indigo-600 bg-indigo-50 font-medium text-indigo-700"
                    : "border-slate-300 bg-white text-slate-600 hover:bg-slate-50",
                ].join(" ")}
              >
                {s}+
              </button>
            ))}
          </div>
        </div>

        <label className="flex items-center gap-2 text-sm text-slate-700">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
            className="h-4 w-4 rounded border-slate-300 text-indigo-600 focus-ring"
          />
          Channel enabled
        </label>

        <div className="flex justify-end gap-2 border-t border-slate-100 pt-4">
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button loading={create.isPending} onClick={() => create.mutate()}>Add channel</Button>
        </div>
      </div>
    </Modal>
  );
}

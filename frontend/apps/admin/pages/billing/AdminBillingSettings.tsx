import { useCallback, useEffect, useState } from 'react'
import { RefreshCw, Save, Settings2 } from 'lucide-react'
import { api } from '@epicpanel/core'
import { Card, CardHeader, EmptyState, SkeletonRows, PageTitle, pushToast } from '@epicpanel/ui'
import { Field, ErrorNote, InfoNote } from '@epicpanel/forms'

/** Wire shape of the billing settings (Phase 10 admin API). */
export interface BillingSettings {
  grace_days: number
  suspend_terminate_days: number
  invoice_due_days: number
  webhook_secret_set: boolean
  providers: string[]
}

export function AdminBillingSettingsPage() {
  const [settings, setSettings] = useState<BillingSettings | null>(null)
  const [form, setForm] = useState({ grace_days: '', suspend_terminate_days: '', invoice_due_days: '', webhook_secret: '' })
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const load = useCallback(() => {
    api
      .get<BillingSettings>('/v1/admin/billing/settings')
      .then((r) => {
        setSettings(r)
        setForm({
          grace_days: String(r.grace_days),
          suspend_terminate_days: String(r.suspend_terminate_days),
          invoice_due_days: String(r.invoice_due_days),
          webhook_secret: '',
        })
      })
      .catch((e) => setErr(e.message ?? 'Failed to load billing settings'))
  }, [])

  useEffect(() => {
    load()
  }, [load])

  const save = async () => {
    setErr('')
    setBusy(true)
    try {
      const body: Record<string, unknown> = {}
      if (form.grace_days !== '') body.grace_days = Number(form.grace_days)
      if (form.suspend_terminate_days !== '') body.suspend_terminate_days = Number(form.suspend_terminate_days)
      if (form.invoice_due_days !== '') body.invoice_due_days = Number(form.invoice_due_days)
      if (form.webhook_secret !== '') body.webhook_secret = form.webhook_secret
      const updated = await api.patch<BillingSettings>('/v1/admin/billing/settings', body)
      setSettings(updated)
      setForm({ ...form, webhook_secret: '' })
      pushToast('success', 'Billing settings saved')
    } catch (ex: any) {
      setErr(ex.message ?? 'Save failed')
    } finally {
      setBusy(false)
    }
  }

  const days = (n?: number) => (n === 1 ? '1 day' : `${n ?? '-'} days`)

  return (
    <div className="fade-up">
      <PageTitle
        title="Billing Settings"
        subtitle="Renewal failure walk: payment failed -> grace -> suspend -> terminate"
        actions={
          <>
            <button className="btn-ghost" onClick={load} aria-label="Refresh">
              <RefreshCw size={15} /> Refresh
            </button>
            <button className="btn-brand" onClick={save} disabled={busy}>
              <Save size={15} /> Save
            </button>
          </>
        }
      />

      {settings === null ? (
        <Card>
          <CardHeader title="Settings" />
          {err ? <div className="px-4 pb-4"><ErrorNote message={err} /></div> : <SkeletonRows rows={2} />}
        </Card>
      ) : (
        <div className="grid grid-cols-1 gap-3.5 xl:grid-cols-2">
          <Card>
            <CardHeader title="Failure walk" subtitle="What happens after a renewal payment fails" />
            <div className="px-4 pb-4">
              <ErrorNote message={err} />
              <Field label="Grace period (days)" hint={`currently ${days(settings.grace_days)} - workload keeps running, renewal retries allowed`}>
                <input
                  className="input"
                  value={form.grace_days}
                  onChange={(e) => setForm({ ...form, grace_days: e.target.value.replace(/[^0-9]/g, '') })}
                  placeholder="3"
                />
              </Field>
              <Field label="Suspend -> terminate (days)" hint={`currently ${days(settings.suspend_terminate_days)} - terminated after this long suspended (backup-first, irreversible)`}>
                <input
                  className="input"
                  value={form.suspend_terminate_days}
                  onChange={(e) => setForm({ ...form, suspend_terminate_days: e.target.value.replace(/[^0-9]/g, '') })}
                  placeholder="7"
                />
              </Field>
              <Field label="Invoice due window (days)" hint={`currently ${days(settings.invoice_due_days)}`}>
                <input
                  className="input"
                  value={form.invoice_due_days}
                  onChange={(e) => setForm({ ...form, invoice_due_days: e.target.value.replace(/[^0-9]/g, '') })}
                  placeholder="7"
                />
              </Field>
              <InfoNote message="Suspension uses the existing workload lifecycle jobs (site offline / instance stop) and is reversible until termination." />
            </div>
          </Card>

          <Card>
            <CardHeader title="Gateway webhook" subtitle="Signature-verified callbacks from the payment gateway" />
            <div className="px-4 pb-4">
              <Field label="Webhook secret" hint={settings.webhook_secret_set ? 'a secret is configured (write-only)' : 'not configured - webhook endpoint refuses traffic'}>
                <input
                  className="input"
                  type="password"
                  value={form.webhook_secret}
                  onChange={(e) => setForm({ ...form, webhook_secret: e.target.value })}
                  placeholder={settings.webhook_secret_set ? 'configured - enter to replace' : 'whsec_…'}
                />
              </Field>
              <InfoNote
                tone={settings.webhook_secret_set ? 'ok' : 'warn'}
                message={
                  settings.webhook_secret_set
                    ? 'Webhook endpoint: POST /v1/billing/webhook/{provider} - the signature is verified before anything is parsed.'
                    : 'Set a secret to enable gateway webhooks; without it the endpoint refuses all traffic.'
                }
              />
              <div className="section-title mt-3">Registered gateways</div>
              <div className="space-y-2">
                {(settings.providers ?? []).map((p) => (
                  <div key={p} className="flex items-center justify-between rounded-[9px] border border-line px-3 py-2.5">
                    <strong className="text-[12px] text-ink capitalize">{p}</strong>
                    <span className="text-[10.5px] text-muted">{p === 'manual' ? 'offline payments, no webhooks' : 'checkout + webhooks'}</span>
                  </div>
                ))}
              </div>
            </div>
          </Card>

          <Card>
            <CardHeader title="Failure walk reference" subtitle="The verbatim state machine" />
            <div className="px-4 pb-4">
              <EmptyState
                icon={<Settings2 size={22} strokeWidth={1.7} />}
                title="PENDING > PROVISIONING > ACTIVE > SUSPENDING > SUSPENDED > TERMINATING > TERMINATED"
                subtitle="FAILED is reachable from provisioning/suspend/terminate and retains artifacts for manual retry. Illegal transitions are rejected and audited."
              />
            </div>
          </Card>
        </div>
      )}
    </div>
  )
}

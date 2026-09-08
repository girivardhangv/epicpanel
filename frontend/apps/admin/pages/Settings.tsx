import { useEffect, useState } from 'react'
import { Settings as SettingsIcon, Save } from 'lucide-react'
import { api } from '@epicpanel/core'
import { Card, CardHeader, PageTitle, pushToast, SkeletonRows } from '@epicpanel/ui'
import { Field, FormRow, InfoNote, ErrorNote } from '@epicpanel/forms'

/**
 * WHM settings: panel hostname (settings store) + honest pointers for the
 * values the platform enforces elsewhere (Phase 12 hardens the rest).
 */
export function SettingsPage() {
  const [hostname, setHostname] = useState('')
  const [loaded, setLoaded] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  useEffect(() => {
    api.get<{ hostname: string }>('/v1/settings')
      .then((r) => {
        setHostname(r.hostname ?? '')
        setLoaded(true)
      })
      .catch(() => setLoaded(true))
  }, [])

  const save = async () => {
    setErr('')
    setBusy(true)
    try {
      await api.patch('/v1/settings/hostname', { hostname })
      pushToast('success', 'Settings saved')
    } catch (ex: any) {
      setErr(ex.message ?? 'Save failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="mx-auto max-w-[1000px] px-4 py-6 lg:px-6">
      <PageTitle
        title="WHM settings"
        subtitle="Panel configuration behind focused controls."
        actions={
          <button className="btn-primary" onClick={save} disabled={busy || !loaded}>
            <Save size={14} /> Save changes
          </button>
        }
      />

      <div className="grid grid-cols-1 gap-3.5">
        <Card>
          <CardHeader title="Control panel" subtitle="Addressing and defaults" />
          {!loaded ? (
            <SkeletonRows rows={1} height="h-9" />
          ) : (
            <>
              <ErrorNote message={err} />
              <Field label="Panel hostname" hint="Must resolve to this server; agents and customers reach the panel here.">
                <input className="input" value={hostname} onChange={(e) => setHostname(e.target.value)} placeholder="panel.example.com" />
              </Field>
              <FormRow>
                <Field label="Registration policy">
                  <input className="input" value="Locked after setup (operator-created accounts)" disabled />
                </Field>
                <Field label="Live metrics pipeline">
                  <input className="input" value="Agent stream — LIVE / STALE / OFFLINE with age" disabled />
                </Field>
              </FormRow>
            </>
          )}
        </Card>

        <Card>
          <CardHeader title="Security defaults" subtitle="Enforced server-side, not toggleable here" />
          <div className="divide-y divide-line">
            <Row title="Force HTTPS" sub="Panel cookies are Secure; deployment TLS is configured at the reverse proxy." chip="Enforced" />
            <Row title="2FA for admins" sub="Per-user TOTP enrollment; platform admins should enable it in Account security." chip="Opt-in" />
            <Row title="Session policy" sub="Server-side session store with revocation; rotation on privilege change lands with Phase 12." chip="Phase 12" />
            <Row title="Rate limiting" sub="Token-bucket per client on every request; login routes get the strictest bucket." chip="Enforced" />
          </div>
        </Card>

        <InfoNote message="Values marked Phase 12 are owned by the security-hardening phase — this screen will grow controls as those land." tone="warn" />
      </div>
    </div>
  )
}

function Row({ title, sub, chip }: { title: string; sub: string; chip: string }) {
  return (
    <div className="flex items-center gap-3 py-3">
      <span className="grid h-[30px] w-[30px] flex-none place-items-center rounded-[8px] bg-surface-2 text-[#667085]">
        <SettingsIcon size={13} />
      </span>
      <div className="min-w-0 flex-1">
        <strong className="block text-[11px] text-ink">{title}</strong>
        <span className="block text-[9.5px] leading-relaxed text-muted">{sub}</span>
      </div>
      <span className="badge-neutral">{chip}</span>
    </div>
  )
}

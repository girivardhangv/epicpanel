import { useState } from 'react'
import { Lock, ShieldAlert } from 'lucide-react'
import { api, useAuth } from '@epicpanel/core'
import { Card, CardHeader } from '@epicpanel/ui'
import { ErrorNote } from '@epicpanel/forms'

type MfaSetup = { secret: string; otpauth_uri: string }
type MfaEnabled = { enabled: boolean; recovery_codes: string[] }

/**
 * Two-factor authentication card (Phase 2 backend, TOTP + recovery codes).
 * Rendered inside Security; kept as a separate component so the admin app
 * (Phase 6) can reuse it directly.
 */
export function TwoFactorCard() {
  const { user, refresh } = useAuth()
  const [setup, setSetup] = useState<MfaSetup | null>(null)
  const [code, setCode] = useState('')
  const [recovery, setRecovery] = useState<string[]>([])
  const [disabling, setDisabling] = useState(false)
  const [password, setPassword] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const enabled = user?.mfa_enabled === true

  const startSetup = async () => {
    setErr(''); setBusy(true)
    try {
      setSetup(await api.post<MfaSetup>('/v1/auth/mfa/setup'))
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to start setup')
    } finally { setBusy(false) }
  }

  const confirmSetup = async () => {
    setErr(''); setBusy(true)
    try {
      const res = await api.post<MfaEnabled>('/v1/auth/mfa/enable', { code })
      setSetup(null)
      setRecovery(res.recovery_codes ?? [])
      await refresh()
    } catch (ex: any) {
      setErr(ex.message ?? 'Invalid code')
    } finally { setBusy(false) }
  }

  const disable = async () => {
    setErr(''); setBusy(true)
    try {
      await api.post('/v1/auth/mfa/disable', { password })
      setDisabling(false); setPassword('')
      await refresh()
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to disable')
    } finally { setBusy(false) }
  }

  if (recovery.length > 0) {
    return (
      <Card className="mb-4 border-2 border-[#ffd0d7]">
        <CardHeader title="Two-factor authentication is ON" subtitle="Save your recovery codes now — they are shown only once." />
        <div className="grid grid-cols-2 gap-1.5 rounded-[9px] bg-surface-2 p-3 font-mono text-[11.5px] sm:grid-cols-5">
          {recovery.map((c) => <span key={c}>{c}</span>)}
        </div>
        <button className="btn-brand mt-3 !min-h-[32px] !px-3 !text-[11px]" onClick={() => setRecovery([])}>
          I saved my codes
        </button>
      </Card>
    )
  }

  return (
    <Card className="mb-4">
      <CardHeader
        title="Two-factor authentication"
        subtitle={enabled ? 'Active — a code is required at every login.' : 'Add a TOTP app (Google Authenticator, 1Password, ...) as a second factor.'}
      />
      <ErrorNote message={err} />
      {enabled ? (
        disabling ? (
          <div className="flex flex-wrap items-end gap-2">
            <label className="block">
              <span className="mb-1.5 block text-[10px] font-extrabold text-[#566278]">Confirm password</span>
              <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
            </label>
            <button className="btn-ghost !text-danger" disabled={busy} onClick={disable}>Turn off 2FA</button>
            <button className="btn-ghost" onClick={() => setDisabling(false)}>Cancel</button>
          </div>
        ) : (
          <button className="btn-ghost !text-danger" onClick={() => setDisabling(true)}>
            <ShieldAlert size={13} /> Disable two-factor
          </button>
        )
      ) : setup ? (
        <div className="space-y-3">
          <div className="break-all rounded-[9px] bg-surface-2 p-2.5 font-mono text-[11px]">{setup.secret}</div>
          <div className="text-[11px] text-sub">Add this secret to your authenticator app (or scan its otpauth:// URI from the setup response), then enter the 6-digit code.</div>
          <div className="flex items-end gap-2">
            <input className="input max-w-[140px]" inputMode="numeric" placeholder="123456" value={code} onChange={(e) => setCode(e.target.value)} />
            <button className="btn-brand !min-h-[32px] !px-3 !text-[11px]" disabled={busy} onClick={confirmSetup}>Verify & enable</button>
          </div>
        </div>
      ) : (
        <button className="btn-brand !min-h-[32px] !px-3 !text-[11px]" disabled={busy} onClick={startSetup}>
          <Lock size={13} /> Set up two-factor
        </button>
      )}
    </Card>
  )
}

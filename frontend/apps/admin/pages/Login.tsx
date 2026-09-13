import { useState } from 'react'
import { Spinner } from '../loading'
import { Navigate, useNavigate } from 'react-router-dom'
import { CloudCog } from 'lucide-react'
import { useAuth } from '@epicpanel/core'
import { Field, ErrorNote } from '@epicpanel/forms'

export function LoginPage() {
  const { user, login, verifyMfa } = useAuth()
  const navigate = useNavigate()
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [mfaToken, setMfaToken] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  if (user) return <Navigate to="/" replace />

  const submit = async () => {
    setErr('')
    setBusy(true)
    try {
      if (mfaToken) {
        await verifyMfa(mfaToken, code)
      } else {
        await login(email, password)
      }
      navigate('/', { replace: true })
    } catch (ex: any) {
      if (ex?.mfaRequired) {
        setMfaToken(ex.mfaToken ?? '')
        setErr('Enter your two-factor code.')
      } else {
        setErr(ex.message ?? 'Sign in failed')
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-app p-4">
      <div className="w-full max-w-[400px] fade-up">
        <div className="mb-6 flex flex-col items-center gap-3">
          <div
            className="grid h-[42px] w-[42px] items-center justify-center rounded-[12px] text-white"
            style={{ background: 'linear-gradient(135deg, #2f73ff, #1646b9)', boxShadow: '0 8px 18px rgba(37,99,235,.27)' }}
          >
            <CloudCog size={21} strokeWidth={1.8} />
          </div>
          <div className="text-[21px] font-bold tracking-[-.025em] text-ink">EpicHost WHM</div>
          <p className="text-[12px] text-muted">Platform administrator sign in</p>
        </div>
        <div className="card p-6">
          <ErrorNote message={err} />
          {!mfaToken ? (
            <>
              <Field label="Email">
                <input className="input" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="admin@example.com" autoFocus onKeyDown={(e) => e.key === 'Enter' && email && password && submit()} />
              </Field>
              <Field label="Password">
                <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && email && password && submit()} />
              </Field>
            </>
          ) : (
            <Field label="Two-factor code" hint="Six-digit code from your authenticator app.">
              <input className="input tracking-[.3em]" value={code} onChange={(e) => setCode(e.target.value)} autoFocus onKeyDown={(e) => e.key === 'Enter' && code && submit()} />
            </Field>
          )}
          <button
            className="btn-brand mt-2 w-full justify-center"
            onClick={submit}
            disabled={busy || Boolean(!mfaToken && (!email || !password)) || Boolean(mfaToken && !code)}
          >
            {busy ? (<><Spinner size={13} /> Signing in…</>) : mfaToken ? 'Verify' : 'Sign in'}
          </button>
        </div>
      </div>
    </div>
  )
}

import { useState, FormEvent, ReactNode, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { Globe } from 'lucide-react'
import { useAuth } from '@epicpanel/core'
import { ErrorNote, Field } from '@epicpanel/forms'

function AuthShell({ children, title, subtitle }: { children: ReactNode; title: string; subtitle: string }) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-app p-4">
      <div className="w-full max-w-[400px] fade-up">
        <div className="mb-8 flex flex-col items-center gap-3">
          <div
            className="flex h-[42px] w-[42px] items-center justify-center rounded-[12px] text-white"
            style={{ background: 'linear-gradient(135deg, #2f73ff, #1646b9)', boxShadow: '0 8px 18px rgba(37,99,235,.27)' }}
          >
            <Globe size={21} strokeWidth={1.8} />
          </div>
          <div className="text-[19px] font-bold tracking-[-.02em] text-ink">EpicHost</div>
        </div>
        <div className="card p-6">
          <h1 className="text-[17px] font-bold tracking-[-.02em] text-ink">{title}</h1>
          <p className="mb-6 mt-1 text-[12px] text-muted">{subtitle}</p>
          {children}
        </div>
      </div>
    </div>
  )
}

// Redirect to the dashboard as soon as the auth state has a user (covers
// both fresh logins and already-authenticated visits to /login|/register).
function useRedirectWhenAuthenticated() {
  const { user, loading } = useAuth()
  const navigate = useNavigate()
  useEffect(() => {
    if (!loading && user) navigate('/', { replace: true })
  }, [user, loading, navigate])
}

export function LoginPage() {
  const { login, verifyMfa } = useAuth()
  const navigate = useNavigate()
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [mfaToken, setMfaToken] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  useRedirectWhenAuthenticated()

  const submit = async (e: FormEvent) => {
    e.preventDefault()
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
        setErr('Enter the 6-digit code from your authenticator app.')
      } else {
        setErr(ex.message ?? 'Login failed')
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <AuthShell title="Welcome back" subtitle="Sign in to your hosting control panel.">
      <form onSubmit={submit}>
        <ErrorNote message={err} />
        {!mfaToken ? (
          <>
            <Field label="Email">
              <input className="input" type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@example.com" autoFocus required />
            </Field>
            <Field label="Password">
              <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} placeholder="••••••••" required />
            </Field>
          </>
        ) : (
          <Field label="Two-factor code">
            <input className="input" inputMode="numeric" value={code} onChange={(e) => setCode(e.target.value)} placeholder="123456" autoFocus />
          </Field>
        )}
        <button className="btn-brand w-full justify-center" disabled={busy}>
          {busy ? 'Signing in...' : mfaToken ? 'Verify & sign in' : 'Sign in'}
        </button>
      </form>
    </AuthShell>
  )
}

export function RegisterPage() {
  const { register } = useAuth()
  const navigate = useNavigate()
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [name, setName] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  useRedirectWhenAuthenticated()

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setErr('')
    setBusy(true)
    try {
      await register(email, password, name)
      navigate('/', { replace: true })
    } catch (ex: any) {
      setErr(ex.message ?? 'Registration failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <AuthShell title="Create your account" subtitle="Get a hosting workspace in under a minute.">
      <form onSubmit={submit}>
        <ErrorNote message={err} />
        <Field label="Name">
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="Ada Lovelace" autoFocus required />
        </Field>
        <Field label="Email">
          <input className="input" type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@example.com" required />
        </Field>
        <Field label="Password" hint="At least 10 characters.">
          <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} placeholder="••••••••" required />
        </Field>
        <button className="btn-brand w-full justify-center" disabled={busy}>
          {busy ? 'Creating...' : 'Create Account'}
        </button>
        <button type="button" className="mt-3 w-full text-center text-[12px] text-muted hover:text-sub" onClick={() => navigate('/login')}>
          Already have an account? Sign in
        </button>
      </form>
    </AuthShell>
  )
}

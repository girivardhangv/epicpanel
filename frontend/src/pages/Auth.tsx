import { useState, FormEvent, ReactNode, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { useAuth } from '@/context/AuthContext'
import { ErrorNote } from '@/components/ui'
import { Globe } from 'lucide-react'

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
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  // Two-factor state: after a 202 mfa_required response we ask for the code.
  const [mfaToken, setMfaToken] = useState('')
  const [mfaCode, setMfaCode] = useState('')
  useRedirectWhenAuthenticated()

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setErr('')
    setBusy(true)
    try {
      if (mfaToken) {
        await verifyMfa(mfaToken, mfaCode)
      } else {
        try {
          await login(email, password)
        } catch (ex: any) {
          if (ex?.mfaRequired) {
            setMfaToken(ex.mfaToken ?? '')
            setErr('Enter the 6-digit code from your authenticator app.')
            return
          }
          throw ex
        }
      }
      navigate('/', { replace: true })
    } catch (ex: any) {
      setErr(ex.message ?? 'Login failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <AuthShell title="Welcome back" subtitle="Sign in to manage your hosting.">
      <form onSubmit={submit}>
        <ErrorNote message={err} />
        {!mfaToken && (
          <>
            <label className="mb-4 block">
              <span className="mb-1.5 block text-[10px] font-extrabold text-[#566278]">Email</span>
              <input className="input" type="email" required value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@company.com" />
            </label>
            <label className="mb-6 block">
              <span className="mb-1.5 block text-[10px] font-extrabold text-[#566278]">Password</span>
              <input className="input" type="password" required value={password} onChange={(e) => setPassword(e.target.value)} placeholder="••••••••••" />
            </label>
          </>
        )}
        {mfaToken && (
          <label className="mb-6 block">
            <span className="mb-1.5 block text-[10px] font-extrabold text-[#566278]">Two-factor code</span>
            <input className="input" inputMode="numeric" pattern="[0-9a-zA-Z-]*" required autoFocus value={mfaCode} onChange={(e) => setMfaCode(e.target.value)} placeholder="123456 or recovery-code" />
          </label>
        )}
        <button className="btn-brand w-full justify-center py-2.5" disabled={busy}>
          {busy ? 'Verifying...' : mfaToken ? 'Verify code' : 'Sign in'}
        </button>
      </form>
      {!mfaToken && (
        <p className="mt-5 text-center text-[13px] text-sub">
          No account yet? <a href="/register" className="font-semibold text-brand hover:underline">Create one</a>
        </p>
      )}
    </AuthShell>
  )
}

export function RegisterPage() {
  const { register } = useAuth()
  const navigate = useNavigate()
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
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
    <AuthShell title="Create your account" subtitle="Start hosting in minutes. The first account becomes the platform administrator.">
      <form onSubmit={submit}>
        <ErrorNote message={err} />
        <label className="mb-4 block">
          <span className="mb-1.5 block text-[10px] font-extrabold text-[#566278]">Full name</span>
          <input className="input" required value={name} onChange={(e) => setName(e.target.value)} placeholder="Ada Lovelace" />
        </label>
        <label className="mb-4 block">
          <span className="mb-1.5 block text-[10px] font-extrabold text-[#566278]">Email</span>
          <input className="input" type="email" required value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@company.com" />
        </label>
        <label className="mb-6 block">
          <span className="mb-1.5 block text-[10px] font-extrabold text-[#566278]">Password</span>
          <input className="input" type="password" required minLength={10} value={password} onChange={(e) => setPassword(e.target.value)} placeholder="At least 10 characters" />
        </label>
        <button className="btn-brand w-full justify-center py-2.5" disabled={busy}>
          {busy ? 'Creating account...' : 'Create account'}
        </button>
      </form>
      <p className="mt-5 text-center text-[13px] text-sub">
        Already registered? <a href="/login" className="font-semibold text-brand hover:underline">Sign in</a>
      </p>
    </AuthShell>
  )
}

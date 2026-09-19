import { createContext, useContext, useEffect, useState, useCallback, ReactNode } from 'react'
import { api, reconnect } from './api'
import type { User, Organization } from './api'

export interface AuthState {
  user: User | null
  orgs: Organization[]
  org: Organization | null
  loading: boolean
  /** Current user's role in the active organization ("" = platform admin/none). */
  myRole: string
  login: (email: string, password: string, mfaCode?: string, mfaToken?: string) => Promise<void>
  verifyMfa: (mfaToken: string, code: string) => Promise<void>
  register: (email: string, password: string, name: string) => Promise<void>
  logout: () => Promise<void>
  setOrg: (org: Organization) => void
  createOrg: (name: string) => Promise<Organization>
  refresh: () => Promise<void>
}

const Ctx = createContext<AuthState>(null as never)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null)
  const [orgs, setOrgs] = useState<Organization[]>([])
  const [org, setOrgState] = useState<Organization | null>(null)
  const [myRole, setMyRole] = useState('')
  const [loading, setLoading] = useState(true)

  const refresh = useCallback(async () => {
    try {
      const me = await api.get<User>('/v1/auth/me')
      setUser(me)
      reconnect()
      const orgsRes = await api.get<{ organizations: Organization[] }>('/v1/organizations')
      setOrgs(orgsRes.organizations ?? [])
      setOrgState((prev) => {
        if (prev) return orgsRes.organizations?.find((o) => o.id === prev.id) ?? orgsRes.organizations?.[0] ?? null
        return orgsRes.organizations?.[0] ?? null
      })
    } catch {
      setUser(null)
      setOrgs([])
      setOrgState(null)
      setMyRole('')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void refresh()
  }, [refresh])

  const login = useCallback(async (email: string, password: string, mfaCode?: string, mfaToken?: string) => {
    const res: any = await api.post('/v1/auth/login', { email, password })
    if (res?.mfa_required) {
      // Two-step login: throw a typed signal the Auth page renders a code
      // input for; the actual session comes from verifyMfa.
      const err = new Error('mfa_required') as Error & { mfaRequired?: boolean; mfaToken?: string }
      err.mfaRequired = true
      err.mfaToken = res.mfa_token
      throw err
    }
    if (mfaCode !== undefined || mfaToken !== undefined) {
      // legacy signature guard — direct verify not intended here
    }
    await refresh()
  }, [refresh])

  const verifyMfa = useCallback(async (mfaToken: string, code: string) => {
    await api.post('/v1/auth/mfa/verify', { mfa_token: mfaToken, code })
    await refresh()
  }, [refresh])

  const register = useCallback(async (email: string, password: string, name: string) => {
    await api.post('/v1/auth/register', { email, password, name })
    await refresh()
  }, [refresh])

  const logout = useCallback(async () => {
    try {
      await api.post('/v1/auth/logout')
    } finally {
      setUser(null)
      setOrgs([])
      setOrgState(null)
    }
  }, [])

  const setOrg = useCallback((o: Organization) => {
    setOrgState(o)
    localStorage.setItem('epichost_org', o.id)
  }, [])

  const createOrg = useCallback(async (name: string): Promise<Organization> => {
    const created = await api.post<Organization>('/v1/organizations', { name })
    await refresh()
    setOrg(created)
    return created
  }, [refresh, setOrg])

  // restore last selected org once orgs load
  useEffect(() => {
    if (orgs.length > 0 && !org) {
      const saved = localStorage.getItem('epichost_org')
      const found = saved ? orgs.find((o) => o.id === saved) : null
      setOrgState(found ?? orgs[0])
    }
  }, [orgs]) // eslint-disable-line react-hooks/exhaustive-deps

  // Resolve the current user's role inside the active organization (used for
  // role-aware navigation and action gating).
  useEffect(() => {
    if (!org || !user) {
      setMyRole(user?.is_platform_admin ? 'owner' : '')
      return
    }
    api
      .get<{ members: { user_id: string; role: string }[] }>(`/v1/organizations/${org.id}/members`)
      .then((r) => {
        const me = (r.members ?? []).find((m) => m.user_id === user.id)
        setMyRole(me?.role ?? (user.is_platform_admin ? 'owner' : ''))
      })
      .catch(() => setMyRole(user.is_platform_admin ? 'owner' : ''))
  }, [org?.id, user?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <Ctx.Provider value={{ user, orgs, org, loading, myRole, login, verifyMfa, register, logout, setOrg, createOrg, refresh }}>
      {children}
    </Ctx.Provider>
  )
}

export const useAuth = () => useContext(Ctx)

/** Organization role rank for client-side display gating (server enforces). */
export const ROLE_RANK: Record<string, number> = { support: 1, billing: 2, developer: 3, admin: 4, owner: 5 }

export function roleLabel(user: { is_platform_admin?: boolean } | null | undefined, myRole: string | null | undefined): string {
  if (user?.is_platform_admin) return 'Administrator'
  const r = myRole ?? ''
  return r ? r.charAt(0).toUpperCase() + r.slice(1) : 'Member'
}

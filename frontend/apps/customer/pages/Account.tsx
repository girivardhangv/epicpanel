import { Link } from 'react-router-dom'
import { KeyRound, ShieldCheck } from 'lucide-react'
import { useAuth } from '@epicpanel/core'
import { Card, CardHeader, PageTitle } from '@epicpanel/ui'

export function AccountPage() {
  const { user, org } = useAuth()

  const rows: [string, string][] = [
    ['Name', user?.name ?? ''],
    ['Email', user?.email ?? ''],
    ['Role', user?.is_platform_admin ? 'Platform Administrator' : 'Member'],
    ['Organization', org?.name ?? '—'],
    ['Member since', user?.created_at ? new Date(user.created_at).toLocaleDateString() : '—'],
  ]

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle title="Account" subtitle="Your profile and quick links to account settings." />

      <div className="grid grid-cols-1 gap-3.5 xl:grid-cols-2">
        <Card>
          <CardHeader title="Profile" subtitle="Your account identity" />
          <div className="space-y-2.5">
            {rows.map(([k, v]) => (
              <div key={k} className="flex items-center justify-between rounded-[9px] border border-line px-3 py-2.5">
                <span className="text-[11px] font-bold text-[#566278]">{k}</span>
                <span className="max-w-[60%] truncate text-[11px] font-semibold text-ink">{v}</span>
              </div>
            ))}
          </div>
        </Card>

        <Card>
          <CardHeader title="Quick actions" subtitle="Security and access" />
          <div className="space-y-2.5">
            <Link to="/security" className="flex items-center gap-3 rounded-[10px] border border-line px-3 py-2.5 transition hover:border-[#cddaff] hover:shadow-card">
              <div className="grid h-[30px] w-[30px] place-items-center rounded-[8px] bg-ok-soft text-ok"><ShieldCheck size={14} strokeWidth={1.8} /></div>
              <div className="min-w-0 flex-1">
                <strong className="block text-[11px] text-ink">Two-factor authentication</strong>
                <span className="block text-[9.5px] text-muted">{user?.mfa_enabled ? 'Enabled' : 'Not enabled — protect your login'}</span>
              </div>
            </Link>
            <Link to="/security" className="flex items-center gap-3 rounded-[10px] border border-line px-3 py-2.5 transition hover:border-[#cddaff] hover:shadow-card">
              <div className="grid h-[30px] w-[30px] place-items-center rounded-[8px] bg-brand-soft text-brand"><KeyRound size={14} strokeWidth={1.8} /></div>
              <div className="min-w-0 flex-1">
                <strong className="block text-[11px] text-ink">API keys</strong>
                <span className="block text-[9.5px] text-muted">Automation access for CI and billing</span>
              </div>
            </Link>
            <Link to="/ssl" className="flex items-center gap-3 rounded-[10px] border border-line px-3 py-2.5 transition hover:border-[#cddaff] hover:shadow-card">
              <div className="grid h-[30px] w-[30px] place-items-center rounded-[8px] bg-purple-soft text-purple"><ShieldCheck size={14} strokeWidth={1.8} /></div>
              <div className="min-w-0 flex-1">
                <strong className="block text-[11px] text-ink">SSL certificates</strong>
                <span className="block text-[9.5px] text-muted">Issue, renew and monitor certificates</span>
              </div>
            </Link>
          </div>
        </Card>
      </div>
    </div>
  )
}

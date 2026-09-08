import { useEffect, useState } from 'react'
import { RefreshCw, HardDriveDownload, Server as ServerIcon } from 'lucide-react'
import { api, useAuth } from '@epicpanel/core'
import type { Server, Website } from '@epicpanel/core'
import { Card, CardHeader, EmptyState, SkeletonRows, PageTitle, StatusBadge, pushToast } from '@epicpanel/ui'

interface Runtime {
  id: string
  server_id: string
  type: string
  version: string
  status: string
  error_message?: string
}

interface Extension {
  id: string
  runtime_id: string
  name: string
  status: string
  error_message?: string
}

export function PhpPage() {
  const { org } = useAuth()
  const [servers, setServers] = useState<Server[]>([])
  const [sites, setSites] = useState<Website[]>([])
  const [runtimes, setRuntimes] = useState<Record<string, Runtime[]>>({})
  const [extensions, setExtensions] = useState<Record<string, Extension[]>>({})
  const [catalog, setCatalog] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState('')

  const load = () => {
    if (!org) return
    setLoading(true)
    api
      .get<{ servers: Server[] }>(`/v1/organizations/${org.id}/servers`)
      .then(async (r) => {
        const sv = r.servers ?? []
        setServers(sv)
        api
          .get<{ websites: Website[] }>(`/v1/organizations/${org.id}/websites`)
          .then((w) => setSites((w.websites ?? []).filter((x) => x.runtime === 'php')))
          .catch(() => setSites([]))
        const nextRuntimes: Record<string, Runtime[]> = {}
        await Promise.all(
          sv.map(async (s) => {
            try {
              const rr = await api.get<{ runtimes: Runtime[] }>(`/v1/organizations/${org.id}/servers/${s.id}/runtimes`)
              nextRuntimes[s.id] = (rr.runtimes ?? []).filter((rt) => rt.type === 'php')
              await Promise.all(
                nextRuntimes[s.id].map(async (rt) => {
                  try {
                    const er = await api.get<{ extensions: Extension[]; catalog: { name: string; label: string }[] }>(
                      `/v1/organizations/${org.id}/servers/${s.id}/runtimes/${rt.id}/extensions`,
                    )
                    setExtensions((prev) => ({ ...prev, [rt.id]: er.extensions ?? [] }))
                    setCatalog((prev) => {
                      const merged = { ...prev }
                      for (const c of er.catalog ?? []) merged[c.name] = c.label
                      return merged
                    })
                  } catch {
                    setExtensions((prev) => ({ ...prev, [rt.id]: [] }))
                  }
                }),
              )
            } catch {
              nextRuntimes[s.id] = []
            }
          }),
        )
        setRuntimes(nextRuntimes)
      })
      .catch(() => setServers([]))
      .finally(() => setLoading(false))
  }

  useEffect(() => {
    load()
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  const manageExtension = async (serverId: string, runtimeId: string, name: string, installed: boolean) => {
    if (!org) return
    setBusy(`${runtimeId}:${name}`)
    try {
      await api.post(`/v1/organizations/${org.id}/servers/${serverId}/runtimes/${runtimeId}/extensions`, {
        name,
        action: installed ? 'remove' : 'install',
      })
      pushToast('success', `${name} ${installed ? 'removal' : 'installation'} queued.`)
      setTimeout(() => load(), 1500)
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Extension action failed')
    } finally {
      setBusy('')
    }
  }

  const sitesForServer = (serverId: string) => sites.filter((w) => w.server_id === serverId)

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <PageTitle
        title="PHP"
        subtitle="PHP versions and extensions for your hosting — switching a site's version re-runs its vhost safely."
        actions={<button className="btn-ghost" onClick={load}><RefreshCw size={14} /> Refresh</button>}
      />

      {loading ? (
        <Card><SkeletonRows rows={3} /></Card>
      ) : servers.length === 0 ? (
        <Card><EmptyState icon={<ServerIcon size={22} />} title="No servers" subtitle="Your hosting provider connects servers for you." /></Card>
      ) : (
        <div className="space-y-3.5">
          {servers.map((s) => {
            const phpRuntimes = runtimes[s.id] ?? []
            const siteList = sitesForServer(s.id)
            return (
              <Card key={s.id} className="overflow-hidden !p-0">
                <CardHeader
                  className="mx-4 mt-4 !mb-0"
                  title={s.name}
                  subtitle={`${s.hostname || 'server'} · ${phpRuntimes.length} PHP version${phpRuntimes.length === 1 ? '' : 's'} installed`}
                />
                {phpRuntimes.length === 0 ? (
                  <div className="px-4 pb-4 pt-1 text-[11px] text-muted">No PHP runtimes installed on this server yet.</div>
                ) : (
                  <div className="divide-y divide-line">
                    {phpRuntimes.map((rt) => {
                      const exts = extensions[rt.id] ?? []
                      const usedBy = siteList.filter((w) => w.runtime_version === rt.version)
                      return (
                        <div key={rt.id} className="px-4 py-3.5">
                          <div className="mb-2.5 flex flex-wrap items-center gap-2.5">
                            <div className="grid h-[30px] w-[30px] flex-none place-items-center rounded-[8px] bg-purple-soft text-purple">
                              <ServerIcon size={14} strokeWidth={1.8} />
                            </div>
                            <div className="min-w-0 flex-1">
                              <strong className="block text-[11.5px] text-ink">PHP {rt.version}</strong>
                              <span className="block text-[9.5px] text-muted">
                                {usedBy.length > 0 ? `used by ${usedBy.map((w) => w.primary_domain || w.name).join(', ')}` : 'not used by any site yet'}
                              </span>
                            </div>
                            <StatusBadge status={rt.status} />
                          </div>
                          <div className="flex flex-wrap gap-1.5">
                            {exts.map((e) => {
                              const installed = e.status === 'available' || e.status === 'ready'
                              const pending = e.status === 'installing' || e.status === 'removing'
                              return (
                                <button
                                  key={e.id}
                                  className={`rounded-full border px-2.5 py-1 text-[10.5px] font-bold transition disabled:opacity-60 ${
                                    installed
                                      ? 'border-[#cef0e1] bg-ok-soft text-ok hover:border-ok'
                                      : 'border-line bg-white text-sub hover:border-brand/40 hover:text-brand'
                                  }`}
                                  disabled={pending || busy === `${rt.id}:${e.name}`}
                                  title={catalog[e.name] ?? e.name}
                                  onClick={() => void manageExtension(s.id, rt.id, e.name, installed)}
                                >
                                  {e.name}
                                  {pending ? ' · working' : busy === `${rt.id}:${e.name}` ? ' · ...' : installed ? '' : ' +'}
                                </button>
                              )
                            })}
                            {exts.length === 0 && <span className="text-[10.5px] text-muted">No managed extensions yet — click a catalog item below to install.</span>}
                          </div>
                          {Object.keys(catalog).length > 0 && (
                            <div className="mt-2.5 flex flex-wrap items-center gap-1.5">
                              <span className="text-[9.5px] font-extrabold uppercase tracking-[.06em] text-muted">Catalog</span>
                              {Object.entries(catalog)
                                .filter(([name]) => !exts.some((e) => e.name === name))
                                .map(([name, label]) => (
                                  <button
                                    key={name}
                                    className="rounded-full border border-line bg-white px-2.5 py-1 text-[10.5px] font-semibold text-sub transition hover:border-brand/40 hover:text-brand disabled:opacity-60"
                                    disabled={busy === `${rt.id}:${name}`}
                                    title={label}
                                    onClick={() => void manageExtension(s.id, rt.id, name, false)}
                                  >
                                    <HardDriveDownload size={11} className="mr-1 inline" />
                                    {name}
                                  </button>
                                ))}
                            </div>
                          )}
                        </div>
                      )
                    })}
                  </div>
                )}
              </Card>
            )
          })}
        </div>
      )}
    </div>
  )
}

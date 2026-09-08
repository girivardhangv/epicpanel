import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { Folder, Globe, ArrowRight } from 'lucide-react'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Card, EmptyState, SkeletonRows, StatusBadge } from '@/components/cards'
import type { Website } from '@/lib/types'

// "Files" sidebar entry: pick which site's file manager to open.
export function FilesLandingPage() {
  const { org } = useAuth()
  const [sites, setSites] = useState<Website[] | null>(null)

  useEffect(() => {
    if (!org) return
    api
      .get<{ websites: Website[] }>(`/v1/organizations/${org.id}/websites`)
      .then((r) => setSites((r.websites ?? []).filter((w) => w.status !== 'deleted' && w.status !== 'deleting')))
      .catch(() => setSites([]))
  }, [org?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      <div className="mb-5">
        <h1 className="text-[23px] font-bold leading-[1.25] tracking-[-.025em] text-ink">Files</h1>
        <p className="mt-[5px] text-[12px] text-muted">Pick a site to browse and edit its files.</p>
      </div>
      <Card>
        {sites === null ? (
          <SkeletonRows rows={3} />
        ) : sites.length === 0 ? (
          <EmptyState
            icon={<Globe size={22} />}
            title="No sites yet"
            subtitle="Create a site first — every site gets its own isolated file space."
            action={<Link className="btn-brand" to="/sites?create=1">Go to Sites</Link>}
          />
        ) : (
          <div className="space-y-3">
            {sites.map((w) => (
              <Link
                key={w.id}
                to={`/sites/${w.id}/files`}
                className="flex items-center gap-3.5 rounded-[12px] border border-line bg-white p-4 transition hover:-translate-y-[2px] hover:border-[#cddaff] hover:shadow-pop"
              >
                <div className="grid h-[34px] w-[34px] shrink-0 place-items-center rounded-[10px] bg-brand-soft text-brand">
                  <Folder size={16} strokeWidth={1.8} />
                </div>
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2.5">
                    <span className="truncate text-[11.5px] font-bold text-[#243047]">{w.primary_domain || w.name}</span>
                    <StatusBadge status={w.status} />
                  </div>
                  <div className="mt-0.5 truncate font-mono text-[10px] text-muted">{w.document_root}</div>
                </div>
                <ArrowRight size={14} className="text-[#98a2b3]" />
              </Link>
            ))}
          </div>
        )}
      </Card>
    </div>
  )
}

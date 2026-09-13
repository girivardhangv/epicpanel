import { useCallback, useEffect, useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import {
  ArrowLeft, Folder, FileText, Trash2, Pencil, Download, Upload,
  FilePlus, FolderPlus, ChevronRight, Save, X, Search,
} from 'lucide-react'
import { api } from '@/lib/api'
import { useAuth } from '@/context/AuthContext'
import { Card, EmptyState, SkeletonRows, StatusBadge } from '@/components/cards'
import { Modal, Field, ErrorNote } from '@/components/ui'
import { fmtBytes, timeAgo } from '@/lib/types'
import type { Website } from '@/lib/types'
import { confirmAction } from '@/lib/confirm'

interface Entry {
  name: string
  size: number
  mode: string
  modified: string
  is_dir: boolean
}

export function FileManagerPage() {
  const { org } = useAuth()
  const { website_id: websiteId = '' } = useParams()
  const [site, setSite] = useState<Website | null>(null)
  const [path, setPath] = useState('/')
  const [entries, setEntries] = useState<Entry[] | null>(null)
  const [error, setError] = useState('')
  const [editing, setEditing] = useState<{ path: string; content: string } | null>(null)
  const [saveBusy, setSaveBusy] = useState(false)
  const [filter, setFilter] = useState('')

  const base = `/v1/organizations/${org?.id}/websites/${websiteId}/files`

  const load = useCallback(async (p: string) => {
    setError('')
    try {
      const r = await api.get<{ entries: Entry[] }>(`${base}?path=${encodeURIComponent(p)}`)
      setEntries([...r.entries].sort((a, b) => (a.is_dir === b.is_dir ? a.name.localeCompare(b.name) : a.is_dir ? -1 : 1)))
    } catch (ex: any) {
      setError(ex.message)
      setEntries([])
    }
  }, [base])

  useEffect(() => {
    if (org) {
      void load(path)
      api.get<Website>(`${'/v1/organizations/' + org.id + '/websites/' + websiteId}`).then(setSite).catch(() => undefined)
    }
  }, [org?.id, websiteId, path]) // eslint-disable-line react-hooks/exhaustive-deps

  const join = (dir: string, name: string) => (dir === '/' ? '/' + name : dir + '/' + name)

  const openDir = (name: string) => setPath(join(path, name))
  const up = () => setPath(path === '/' ? '/' : path.split('/').slice(0, -1).join('/') || '/')

  const del = async (e: Entry) => {
    if (!(await confirmAction({ title: 'Delete', message: `Delete ${e.is_dir ? 'folder' : 'file'} "${e.name}"? This cannot be undone.`, confirmLabel: 'Delete' }))) return
    await api.del(`${base}?path=${encodeURIComponent(join(path, e.name))}`)
    await load(path)
  }

  const rename = async (e: Entry, to: string) => {
    await api.patch(base, { path: join(path, e.name), to: join(path, to) })
    await load(path)
  }

  const create = async (name: string, type: 'dir' | 'file') => {
    await api.post(base, { path: join(path, name), type })
    await load(path)
  }

  const upload = async (file: File) => {
    const fd = new FormData()
    fd.append('file', file)
    await fetch(`${base}/upload?path=${encodeURIComponent(path)}`, {
      method: 'POST',
      credentials: 'include',
      body: fd,
    })
    await load(path)
  }

  const openEditor = async (e: Entry) => {
    const r = await api.get<{ path: string; content: string }>(`${base}/content?path=${encodeURIComponent(join(path, e.name))}`)
    setEditing({ path: join(path, e.name), content: r.content })
  }

  const save = async () => {
    if (!editing) return
    setSaveBusy(true)
    try {
      await api.put(`${base}/content`, { path: editing.path, content: editing.content })
      setEditing(null)
      await load(path)
    } finally {
      setSaveBusy(false)
    }
  }

  const crumbs = path.split('/').filter(Boolean)

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      {/* Header */}
      <div className="mb-5 flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <Link to={`/sites/${websiteId ?? ''}`} className="icon-btn !h-[34px] !w-[34px]" title="Back" aria-label="Back to site">
            <ArrowLeft size={15} />
          </Link>
          <div>
            <div className="flex items-center gap-2.5">
              <h1 className="text-[23px] font-bold leading-[1.25] tracking-[-.025em] text-ink">{site?.name ?? '...'}</h1>
              {site && <StatusBadge status={site.status} />}
            </div>
            <p className="mt-[5px] text-[12px] text-muted">File manager — <span className="font-mono">{site?.document_root}</span></p>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <label className="btn-ghost cursor-pointer">
            <Upload size={15} /> Upload
            <input type="file" className="hidden" onChange={(e) => e.target.files?.[0] && upload(e.target.files[0])} />
          </label>
          <a className="btn-ghost" href={`${base}/download?path=`}><Download size={15} /> Download All</a>
        </div>
      </div>

      {/* Breadcrumb + actions */}
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-1 text-[13.5px] font-medium text-sub">
          <button className="rounded-[7px] px-2 py-1 text-[11.5px] font-bold text-brand hover:bg-surface-2" onClick={up}>root</button>
          {crumbs.map((c, i) => (
            <span key={i} className="flex items-center gap-1">
              <ChevronRight size={13} className="text-muted" />
              <button
                className="rounded-[7px] px-2 py-1 text-[11.5px] font-semibold text-ink hover:bg-surface-2"
                onClick={() => setPath('/' + crumbs.slice(0, i + 1).join('/'))}
              >{c}</button>
            </span>
          ))}
        </div>
        <NewButtons onCreate={create} />
      </div>

      {error && (
        <div className="mb-4 rounded-[9px] border border-[#ffd0d7] bg-danger-soft px-3 py-2 text-[11px] font-semibold text-danger">
          {error} — check the path or refresh.
        </div>
      )}

      <Card className="!p-0">
        <div className="flex items-center gap-2 border-b border-line bg-[#fbfcfe] px-4 py-3">
          <Search size={15} className="text-muted" />
          <input className="w-full bg-transparent text-[11.5px] outline-none placeholder:text-[#98a2b3]" placeholder="Filter files..." value={filter} onChange={(e) => setFilter(e.target.value)} />
        </div>
        {entries === null ? (
          <div className="p-5"><SkeletonRows rows={4} height="h-10" /></div>
        ) : entries.length === 0 ? (
          <EmptyState icon={<Folder size={22} />} title="Empty folder" subtitle="Upload files or create something new." />
        ) : (
          <div className="divide-y divide-line">
            {entries
              .filter((e) => e.name.toLowerCase().includes(filter.toLowerCase()))
              .map((e) => (
                <FileRow
                  key={e.name}
                  entry={e}
                  downloadBase={base}
                  dirPath={path}
                  onOpen={e.is_dir ? () => openDir(e.name) : undefined}
                  onEdit={e.is_dir ? undefined : () => openEditor(e)}
                  onDelete={() => del(e)}
                  onRename={(to) => rename(e, to)}
                />
              ))}
          </div>
        )}
      </Card>

      {/* Editor */}
      <Modal open={!!editing} onClose={() => setEditing(null)} title={`Editing ${editing?.path ?? ''}`} width="max-w-3xl">
        {editing && (
          <>
            <textarea
              className="h-[55vh] w-full rounded-[10px] border border-line bg-[#0D1321] p-4 font-mono text-[12px] leading-relaxed text-slate-200 outline-none focus:ring-2 focus:ring-brand/30"
              value={editing.content}
              onChange={(ev) => setEditing({ ...editing, content: ev.target.value })}
              spellCheck={false}
            />
            <div className="mt-4 flex justify-end gap-2">
              <button className="btn-ghost" onClick={() => setEditing(null)}><X size={14} /> Cancel</button>
              <button className="btn-brand" onClick={save} disabled={saveBusy}>
                <Save size={14} /> {saveBusy ? 'Saving...' : 'Save'}
              </button>
            </div>
          </>
        )}
      </Modal>
    </div>
  )
}

function FileRow({ entry: e, downloadBase, dirPath, onOpen, onEdit, onDelete, onRename }: {
  entry: Entry
  downloadBase: string
  dirPath: string
  onOpen?: () => void
  onEdit?: () => void
  onDelete: () => void
  onRename: (to: string) => void
}) {
  const [renaming, setRenaming] = useState(false)
  const [to, setTo] = useState(e.name)

  if (renaming) {
    return (
      <div className="flex items-center gap-3 px-5 py-3">
        {e.is_dir ? <Folder size={17} className="text-brand" /> : <FileText size={17} className="text-sub" />}
        <input className="input flex-1 !py-1.5" value={to} onChange={(ev) => setTo(ev.target.value)} autoFocus
          onKeyDown={(ev) => {
            if (ev.key === 'Enter' && to && to !== e.name) { onRename(to); setRenaming(false) }
            if (ev.key === 'Escape') setRenaming(false)
          }} />
        <button className="btn-brand !py-1.5" onClick={() => { if (to && to !== e.name) onRename(to); setRenaming(false) }}>Save</button>
        <button className="btn-ghost !py-1.5" onClick={() => setRenaming(false)} aria-label="Cancel rename"><X size={14} /></button>
      </div>
    )
  }

  return (
    <div className="group flex items-center gap-3.5 px-5 py-3 transition hover:bg-surface-2">
      <div className={`flex h-9 w-9 shrink-0 items-center justify-center rounded-lg ${e.is_dir ? 'bg-brand-soft text-brand' : 'bg-surface-2 text-sub'}`}>
        {e.is_dir ? <Folder size={17} /> : <FileText size={17} />}
      </div>
      <div className="min-w-0 flex-1">
        {onOpen ? (
          <button className="text-[14.5px] font-semibold text-ink hover:text-brand hover:underline" onClick={onOpen}>{e.name}</button>
        ) : (
          <span className="text-[14.5px] font-medium text-ink">{e.name}</span>
        )}
        <span className="ml-2 text-xs text-muted">{e.is_dir ? 'folder' : fmtBytes(e.size)} · {timeAgo(e.modified)}</span>
      </div>
      <span className="hidden font-mono text-xs text-muted sm:inline">{e.mode}</span>
      <div className="flex items-center gap-1 opacity-60 transition group-hover:opacity-100">
        {onEdit && (
          <button className="rounded-lg p-2 text-sub transition hover:bg-app hover:text-ink" onClick={onEdit} title="Edit" aria-label={`Edit ${e.name}`}>
            <Pencil size={15} />
          </button>
        )}
        {!e.is_dir && (
          <a
            className="rounded-lg p-2 text-sub transition hover:bg-app hover:text-ink"
            href={`${downloadBase}/download?path=${encodeURIComponent(joinPath(dirPath, e.name))}`}
            title="Download"
            aria-label={`Download ${e.name}`}
          >
            <Download size={15} />
          </a>
        )}
        <button className="rounded-lg p-2 text-sub transition hover:bg-app hover:text-ink" onClick={() => { setTo(e.name); setRenaming(true) }} title="Rename" aria-label={`Rename ${e.name}`}>
          <Pencil size={15} />
        </button>
        <button className="rounded-lg p-2 text-sub transition hover:border-[#ffd0d7] hover:bg-danger-soft hover:text-danger" onClick={onDelete} title="Delete" aria-label={`Delete ${e.name}`}>
          <Trash2 size={15} />
        </button>
      </div>
    </div>
  )
}

function joinPath(a: string, b: string) { return a === '/' ? '/' + b : a + '/' + b }

function NewButtons({ onCreate }: { onCreate: (name: string, type: 'dir' | 'file') => Promise<void> }) {
  const [open, setOpen] = useState<'dir' | 'file' | null>(null)
  const [name, setName] = useState('')
  const [err, setErr] = useState('')

  const submit = async () => {
    if (!name) return
    try {
      await onCreate(name, open!)
      setOpen(null)
      setName('')
    } catch (ex: any) {
      setErr(ex.message)
    }
  }

  return (
    <>
      <div className="flex items-center gap-2">
        <button className="btn-ghost" onClick={() => { setOpen('file'); setErr('') }}><FilePlus size={15} /> New File</button>
        <button className="btn-ghost" onClick={() => { setOpen('dir'); setErr('') }}><FolderPlus size={15} /> New Folder</button>
      </div>
      <Modal open={!!open} onClose={() => setOpen(null)} title={open === 'dir' ? 'New Folder' : 'New File'}>
        <ErrorNote message={err} />
        <Field label="Name">
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} autoFocus
            onKeyDown={(e) => e.key === 'Enter' && name && submit()} placeholder={open === 'dir' ? 'assets' : 'index.php'} />
        </Field>
        <button className="btn-brand w-full justify-center" onClick={submit} disabled={!name}>Create</button>
      </Modal>
    </>
  )
}

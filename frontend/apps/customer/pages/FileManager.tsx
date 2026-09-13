import { useCallback, useEffect, useState } from 'react'
import { Spinner } from '../loading'
import { useParams, Link } from 'react-router-dom'
import {
  ArrowLeft, Folder, FileText, FileCode2, Trash2, Pencil, Download, Upload,
  FilePlus, FolderPlus, Save, X, Search,
} from 'lucide-react'
import { api, useAuth, fmtBytes } from '@epicpanel/core'
import { Card, EmptyState, SkeletonRows, Breadcrumbs, ConfirmDialog, pushToast } from '@epicpanel/ui'
import { Modal, Field, ErrorNote } from '@epicpanel/forms'

interface Entry {
  name: string
  size: number
  mode: string
  modified: string
  is_dir: boolean
}

function fileKind(e: Entry): { label: string; icon: React.ReactNode } {
  if (e.is_dir) return { label: 'Folder', icon: <Folder size={15} className="text-brand" /> }
  const ext = e.name.split('.').pop()?.toLowerCase() ?? ''
  if (['php', 'phtml'].includes(ext)) return { label: 'PHP', icon: <FileCode2 size={15} className="text-sub" /> }
  if (['conf', 'env', 'ini', 'htaccess'].includes(ext) || e.name.startsWith('.ht')) return { label: 'Config', icon: <FileText size={15} className="text-sub" /> }
  return { label: ext ? ext.toUpperCase() : 'File', icon: <FileText size={15} className="text-sub" /> }
}

function fmtDate(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso ?? '—'
  const today = new Date()
  const sameDay = d.toDateString() === today.toDateString()
  const yest = new Date(today.getTime() - 86400000).toDateString() === d.toDateString()
  const hm = `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
  if (sameDay) return `Today, ${hm}`
  if (yest) return `Yesterday, ${hm}`
  return d.toLocaleDateString()
}

export function FileManagerPage() {
  const { org } = useAuth()
  const { website_id: websiteId = '' } = useParams()
  const [site, setSite] = useState<{ name: string; primary_domain: string; document_root: string; status: string } | null>(null)
  const [path, setPath] = useState('/')
  const [entries, setEntries] = useState<Entry[] | null>(null)
  const [error, setError] = useState('')
  const [filter, setFilter] = useState('')
  const [editing, setEditing] = useState<{ path: string; content: string } | null>(null)
  const [saveBusy, setSaveBusy] = useState(false)
  const [confirm, setConfirm] = useState<{ title: string; message: string; action: () => Promise<void> } | null>(null)
  const [confirmBusy, setConfirmBusy] = useState(false)

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
      api.get<{ name: string; primary_domain: string; document_root: string; status: string }>(`/v1/organizations/${org.id}/websites/${websiteId}`)
        .then(setSite)
        .catch(() => undefined)
    }
  }, [org?.id, websiteId, path]) // eslint-disable-line react-hooks/exhaustive-deps

  const join = (dir: string, name: string) => (dir === '/' ? '/' + name : dir + '/' + name)
  const openDir = (name: string) => setPath(join(path, name))
  const up = () => setPath(path === '/' ? '/' : path.split('/').slice(0, -1).join('/') || '/')

  const del = (e: Entry) => {
    setConfirm({
      title: e.is_dir ? 'Delete folder' : 'Delete file',
      message: `Delete "${e.name}"? This cannot be undone.`,
      action: async () => {
        await api.del(`${base}?path=${encodeURIComponent(join(path, e.name))}`)
        pushToast('success', `"${e.name}" deleted.`)
        await load(path)
      },
    })
  }

  const rename = async (e: Entry, to: string) => {
    await api.patch(base, { path: join(path, e.name), to: join(path, to) })
    await load(path)
  }

  const upload = async (file: File) => {
    const fd = new FormData()
    fd.append('file', file)
    await fetch(`${base}/upload?path=${encodeURIComponent(path)}`, {
      method: 'POST',
      credentials: 'include',
      headers: { 'X-EpicPanel': '1' },
      body: fd,
    })
    pushToast('success', `${file.name} uploaded.`)
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
      pushToast('success', 'File saved.')
      await load(path)
    } finally {
      setSaveBusy(false)
    }
  }

  const crumbs = path.split('/').filter(Boolean)
  const filtered = (entries ?? []).filter((e) => e.name.toLowerCase().includes(filter.toLowerCase()))

  const createEntry = async (name: string, type: 'dir' | 'file') => {
    await api.post(base, { path: join(path, name), type })
    pushToast('success', type === 'dir' ? `Folder "${name}" created.` : `File "${name}" created.`)
    await load(path)
  }

  return (
    <div className="mx-auto max-w-[1400px] px-4 py-6 lg:px-6">
      {/* Header */}
      <div className="mb-5 flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <Link to="/websites" className="icon-btn !h-[34px] !w-[34px]" title="Back" aria-label="Back"><ArrowLeft size={15} /></Link>
          <div>
            <h1 className="text-[23px] font-bold leading-[1.25] tracking-[-.025em] text-ink">File Manager</h1>
            <p className="mt-[5px] text-[12px] text-muted">
              {site ? `${site.primary_domain || site.name} — ` : ''}Simple file operations for your hosting account.
            </p>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <label className="btn-ghost cursor-pointer">
            <Upload size={15} /> Upload files
            <input type="file" className="hidden" onChange={(e) => e.target.files?.[0] && upload(e.target.files[0])} />
          </label>
        </div>
      </div>

      {/* Breadcrumb */}
      <div className="mb-4">
        <Breadcrumbs
          crumbs={[
            { label: site?.primary_domain || site?.name || 'root', to: undefined },
            ...crumbs.map((c) => ({ label: c, to: undefined })),
          ]}
        />
      </div>

      {error && (
        <div className="mb-4 rounded-[9px] border border-[#ffd0d7] bg-danger-soft px-3 py-2 text-[11px] font-semibold text-danger">
          {error} — check the path or refresh.
        </div>
      )}

      <Card className="overflow-hidden !p-0">
        <div className="toolbar">
          <div className="relative min-w-[180px] flex-1">
            <Search size={14} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-[#98a2b3]" />
            <input
              className="h-[34px] w-full rounded-[8px] border border-line bg-white px-2.5 pl-[31px] text-[11px] outline-none placeholder:text-[#98a2b3] focus:border-[#9db8ff]"
              placeholder="Search files..."
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
            />
          </div>
          <NewButtons onCreate={createEntry} />
        </div>
        {entries === null ? (
          <div className="p-5"><SkeletonRows rows={4} height="h-10" /></div>
        ) : filtered.length === 0 ? (
          <EmptyState icon={<Folder size={22} />} title="Empty folder" subtitle="Upload files or create something new." />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[700px] border-collapse">
              <thead>
                <tr>
                  {['Name', 'Type', 'Size', 'Modified', 'Permissions', ''].map((h) => (
                    <th key={h} className="border-b border-line bg-[#fbfcfe] px-4 py-[11px] text-left text-[9px] font-extrabold uppercase tracking-[.06em] text-[#7a8597]">{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {filtered.map((e) => (
                  <FileRow
                    key={e.name}
                    entry={e}
                    dirPath={path}
                    base={base}
                    onOpen={e.is_dir ? () => openDir(e.name) : undefined}
                    onEdit={e.is_dir ? undefined : () => void openEditor(e)}
                    onDelete={() => del(e)}
                    onRename={(to) => void rename(e, to)}
                  />
                ))}
              </tbody>
            </table>
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
                <Save size={14} /> {saveBusy ? (<><Spinner size={13} /> Saving…</>) : 'Save'}
              </button>
            </div>
          </>
        )}
      </Modal>

      <ConfirmDialog
        open={!!confirm}
        onClose={() => setConfirm(null)}
        onConfirm={async () => {
          if (!confirm) return
          setConfirmBusy(true)
          try {
            await confirm.action()
            setConfirm(null)
          } catch (ex: any) {
            pushToast('error', ex.message ?? 'Delete failed')
            setConfirm(null)
          } finally {
            setConfirmBusy(false)
          }
        }}
        title={confirm?.title ?? ''}
        message={confirm?.message ?? ''}
        confirmLabel="Delete"
        busy={confirmBusy}
      />
    </div>
  )
}

function FileRow({ entry: e, dirPath, base, onOpen, onEdit, onDelete, onRename }: {
  entry: Entry
  dirPath: string
  base: string
  onOpen?: () => void
  onEdit?: () => void
  onDelete: () => void
  onRename: (to: string) => void
}) {
  const [renaming, setRenaming] = useState(false)
  const [to, setTo] = useState(e.name)
  const kind = fileKind(e)

  if (renaming) {
    return (
      <tr>
        <td className="border-b border-line px-4 py-[11px]">
          <div className="flex items-center gap-2">
            {kind.icon}
            <input className="input h-[30px] w-[220px]" value={to} onChange={(ev) => setTo(ev.target.value)} autoFocus
              onKeyDown={(ev) => {
                if (ev.key === 'Enter' && to && to !== e.name) { onRename(to); setRenaming(false) }
                if (ev.key === 'Escape') setRenaming(false)
              }} />
            <button className="btn-brand !min-h-[28px] !px-2 !text-[10px]" onClick={() => { if (to && to !== e.name) onRename(to); setRenaming(false) }}>Save</button>
            <button className="btn-ghost !min-h-[28px] !px-2 !text-[10px]" onClick={() => setRenaming(false)}><X size={12} /></button>
          </div>
        </td>
        <td className="border-b border-line px-4 py-[11px]" colSpan={5} />
      </tr>
    )
  }

  const joinPath = (a: string, b: string) => (a === '/' ? '/' + b : a + '/' + b)

  return (
    <tr className="transition hover:bg-[#fbfcff]">
      <td className="border-b border-line px-4 py-[11px]">
        <div className="flex items-center gap-2.5">
          <div className={`grid h-[28px] w-[28px] flex-none place-items-center rounded-[7px] ${e.is_dir ? 'bg-brand-soft text-brand' : 'bg-surface-2 text-sub'}`}>
            {kind.icon}
          </div>
          <div className="min-w-0">
            {onOpen ? (
              <button className="table-primary hover:text-brand hover:underline" onClick={onOpen}>{e.name}</button>
            ) : (
              <span className="table-primary">{e.name}</span>
            )}
          </div>
        </div>
      </td>
      <td className="border-b border-line px-4 py-[11px] text-[10.5px] text-ink">{kind.label}</td>
      <td className="border-b border-line px-4 py-[11px] text-[10.5px] text-ink">{e.is_dir ? '—' : fmtBytes(e.size)}</td>
      <td className="border-b border-line px-4 py-[11px] text-[10.5px] text-ink">{fmtDate(e.modified)}</td>
      <td className="border-b border-line px-4 py-[11px] font-mono text-[10.5px] text-muted">{e.mode}</td>
      <td className="border-b border-line px-4 py-[11px]">
        <div className="flex justify-end gap-[5px]">
          {onEdit && <button className="icon-btn" title="Edit file" aria-label="Edit file" onClick={onEdit}><FileText size={13} /></button>}
          {!e.is_dir && (
            <a className="icon-btn" title="Download" aria-label="Download" href={`${base}/download?path=${encodeURIComponent(joinPath(dirPath, e.name))}`}>
              <Download size={13} />
            </a>
          )}
          <button className="icon-btn" title="Rename" aria-label="Rename" onClick={() => { setTo(e.name); setRenaming(true) }}><Pencil size={13} /></button>
          <button className="icon-btn hover:!border-[#ffd0d7] hover:!bg-danger-soft hover:!text-danger" title="Delete" aria-label="Delete" onClick={onDelete}><Trash2 size={13} /></button>
        </div>
      </td>
    </tr>
  )
}

function NewButtons({ onCreate }: { onCreate: (name: string, type: 'dir' | 'file') => Promise<void> }) {
  const [open, setOpen] = useState<'dir' | 'file' | null>(null)
  const [name, setName] = useState('')
  const [err, setErr] = useState('')

  const submit = async () => {
    if (!name || !open) return
    try {
      await onCreate(name, open)
      setOpen(null)
      setName('')
    } catch (ex: any) {
      setErr(ex.message)
    }
  }

  return (
    <>
      <div className="flex items-center gap-2">
        <button className="btn-ghost" onClick={() => { setOpen('file'); setErr('') }}><FilePlus size={14} /> New file</button>
        <button className="btn-ghost" onClick={() => { setOpen('dir'); setErr('') }}><FolderPlus size={14} /> New folder</button>
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

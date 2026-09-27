import { useCallback, useEffect, useRef, useState } from 'react'
import { Spinner } from '../loading'
import { api, deploymentsApi, timeAgo } from '@epicpanel/core'
import type { Deployment, Website } from '@epicpanel/core'
import { Card, CardHeader, EmptyState, SkeletonRows, pushToast } from '@epicpanel/ui'
import { Field, ErrorNote } from '@epicpanel/forms'
import { GitBranch, GitCommitHorizontal, Rocket, RotateCcw, Save } from 'lucide-react'

// ============================================================================
// Git Deployments section for the site page: repo/branch config, the
// RUNNING DIRECTORY (web_dir — the directory inside the site's workdir the
// site serves from, e.g. "public" for Laravel, picked from a dropdown of
// the workdir's actual folders), the write-only deploy token for private
// repos, deploy/rollback actions and the deployment history.
// ============================================================================

const depStatusClass: Record<string, string> = {
  successful: 'status-chip status-live',
  failed: 'status-chip status-down',
  running: 'status-chip status-warning',
  pending: 'status-chip status-warning',
}

export function SiteDeploysSection({ site, canManage, canRollback, onChanged }: {
  site: Website
  canManage: boolean
  canRollback: boolean
  onChanged: () => void
}) {
  const orgId = site.organization_id
  const [deps, setDeps] = useState<Deployment[] | null>(null)
  const [form, setForm] = useState({
    repo_url: site.deploy_repo_url ?? '',
    branch: site.deploy_branch ?? '',
    web_dir: site.deploy_web_dir ?? '',
    auto_build: site.deploy_auto_build ?? false,
    token: '',
  })
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [deploying, setDeploying] = useState(false)
  // Running-directory dropdown source: the actual directories inside the
  // site's workdir (null = endpoint unavailable → free-text fallback).
  const [dirOptions, setDirOptions] = useState<{ value: string; label: string }[] | null>(null)
  const pollRef = useRef<number | null>(null)

  const loadDeps = useCallback(() => {
    deploymentsApi.list(orgId, site.id).then((r) => setDeps(r.deployments ?? [])).catch(() => setDeps([]))
    // Refresh the dropdown too — deploys change the workdir's contents.
    deploymentsApi.runningDirOptions(orgId, site.id).then((r) => setDirOptions(r.options ?? [])).catch(() => setDirOptions(null))
  }, [orgId, site.id])

  useEffect(() => {
    loadDeps()
    return () => { if (pollRef.current) window.clearInterval(pollRef.current) }
  }, [loadDeps])

  // After a deploy, poll the history briefly so the row moves
  // pending → running → successful/failed without manual refresh.
  const pollBurst = () => {
    if (pollRef.current) window.clearInterval(pollRef.current)
    let n = 0
    pollRef.current = window.setInterval(() => {
      loadDeps()
      if (++n >= 15 && pollRef.current) window.clearInterval(pollRef.current)
    }, 4000)
  }

  const save = async () => {
    setErr('')
    setBusy(true)
    try {
      const body: { repo_url: string; branch?: string; web_dir?: string; auto_build?: boolean; deploy_token?: string } = {
        repo_url: form.repo_url.trim(),
      }
      if (form.branch.trim()) body.branch = form.branch.trim()
      body.web_dir = form.web_dir.trim()
      body.auto_build = form.auto_build
      if (form.token) body.deploy_token = form.token
      await deploymentsApi.saveConfig(orgId, site.id, body)
      pushToast('success', 'Deployment settings saved.')
      setForm({ ...form, token: '' })
      onChanged()
    } catch (ex: any) {
      setErr(ex.message ?? 'Failed to save deployment settings')
    } finally {
      setBusy(false)
    }
  }

  const deploy = async () => {
    setErr('')
    setDeploying(true)
    try {
      await deploymentsApi.deploy(orgId, site.id)
      pushToast('success', 'Deploy queued — updating workdir now.')
      loadDeps()
      pollBurst()
    } catch (ex: any) {
      pushToast('error', ex.message ?? 'Deploy failed to queue')
    } finally {
      setDeploying(false)
    }
  }

  const rollback = () => {
    deploymentsApi.rollback(orgId, site.id)
      .then(() => { pushToast('success', 'Rollback queued.'); loadDeps(); pollBurst() })
      .catch((ex: any) => pushToast('error', ex.message ?? 'Rollback failed'))
  }

  const configured = !!site.deploy_repo_url
  const lastSuccessful = deps?.find((d) => d.status === 'successful')

  return (
    <Card className="mb-4 overflow-hidden !p-0">
      <CardHeader
        className="mx-4 mt-4 !mb-0"
        title="Git Deployments"
        subtitle="Deploy from a GitHub/GitLab repo — private repos use a deploy token"
        right={
          configured && canManage ? (
            <button className="btn-primary !min-h-[30px] !px-2.5 !text-[10.5px]" onClick={deploy} disabled={deploying}>
              {deploying ? (<><Spinner size={12} /> Queuing…</>) : (<><Rocket size={12} /> Deploy now</>)}
            </button>
          ) : undefined
        }
      />
      <ErrorNote message={err} />

      {/* Config: repo, branch, running directory, token */}
      <div className="grid grid-cols-1 gap-3 p-4 sm:grid-cols-2">
        <Field label="Repository URL" hint="https://github.com/you/your-repo.git">
          <input
            className="input" value={form.repo_url} disabled={!canManage}
            onChange={(e) => setForm({ ...form, repo_url: e.target.value })}
            placeholder="https://github.com/you/your-repo.git"
          />
        </Field>
        <Field label="Branch" hint="default: main">
          <input
            className="input" value={form.branch} disabled={!canManage}
            onChange={(e) => setForm({ ...form, branch: e.target.value })}
            placeholder="main"
          />
        </Field>
        <Field label="Running directory" hint="Inside workdir — 'public' for Laravel; repo root = workdir itself">
          {dirOptions ? (
            <select
              className="input" value={form.web_dir} disabled={!canManage}
              onChange={(e) => setForm({ ...form, web_dir: e.target.value })}
            >
              {(dirOptions.some((o) => o.value === form.web_dir) || !form.web_dir
                ? dirOptions
                : [{ value: form.web_dir, label: form.web_dir }, ...dirOptions]
              ).map((o) => (
                <option key={o.value || '__root'} value={o.value}>{o.label}</option>
              ))}
            </select>
          ) : (
            <input
              className="input" value={form.web_dir} disabled={!canManage}
              onChange={(e) => setForm({ ...form, web_dir: e.target.value })}
              placeholder="public"
            />
          )}
        </Field>
        <Field label="Deploy token" hint="Private repos only — write-only, leave blank to keep the saved one">
          <input
            className="input" type="password" value={form.token} disabled={!canManage}
            onChange={(e) => setForm({ ...form, token: e.target.value })}
            placeholder={site.deploy_repo_url ? '•••••••• (saved)' : 'ghp_… / glpat-…'}
            autoComplete="new-password"
          />
        </Field>
        {canManage && (
          <div className="sm:col-span-2 flex flex-wrap items-center gap-3">
            <label className="flex items-center gap-2 text-[10.5px] font-bold text-[#243047]">
              <input
                type="checkbox"
                checked={form.auto_build}
                onChange={(e) => setForm({ ...form, auto_build: e.target.checked })}
              />
              Run composer/npm build during deploy
            </label>
            <span className="text-[9.5px] text-muted">
              Off (default) = plain fetch: clone &amp; serve, nothing else. Run composer/artisan yourself from Commands.
            </span>
          </div>
        )}
        {canManage && (
          <div className="sm:col-span-2">
            <button className="btn-ghost !min-h-[30px] !px-3 !text-[10.5px]" onClick={save} disabled={busy || !form.repo_url.trim()}>
              {busy ? (<><Spinner size={12} /> Saving…</>) : (<><Save size={12} /> Save settings</>)}
            </button>
            {configured && (
              <span className="ml-3 align-middle text-[9.5px] text-muted">
                Site serves from <span className="font-mono">workdir/{form.web_dir || '(repo root)'}</span> — a deploy reports early if that folder is missing from the repo. Takes effect on your next deploy.
              </span>
            )}
          </div>
        )}
      </div>

      {/* History */}
      {deps === null ? (
        <div className="px-4 pb-4"><SkeletonRows rows={2} height="h-10" /></div>
      ) : deps.length === 0 ? (
        <div className="px-4 pb-4">
          <EmptyState icon={<GitBranch size={20} />} title="No deployments yet" subtitle="Save the settings above, then hit Deploy now." />
        </div>
      ) : (
        <div className="divide-y divide-line border-t border-line">
          {deps.slice(0, 8).map((d) => (
            <div key={d.id} className="flex flex-wrap items-center gap-3 px-4 py-3 transition hover:bg-surface-2">
              <div className="grid h-[30px] w-[30px] flex-none place-items-center rounded-[8px] bg-brand-soft text-brand">
                <GitCommitHorizontal size={14} strokeWidth={1.8} />
              </div>
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="status-chip">{d.branch || 'main'}</span>
                  <span className={depStatusClass[d.status] ?? 'status-chip'}>{d.status}</span>
                  {d.commit_sha && <span className="font-mono text-[9.5px] text-muted">{d.commit_sha.slice(0, 7)}</span>}
                </div>
                <div className="mt-0.5 truncate text-[9.5px] text-muted">
                  {timeAgo(d.created_at)}{d.error ? ` · ${d.error.slice(0, 200)}` : ''}
                </div>
                {(d.error || d.log) && (
                  <details className="mt-1.5">
                    <summary className="cursor-pointer text-[9.5px] font-bold text-brand">Full output</summary>
                    <pre className="mt-1 max-h-48 overflow-auto whitespace-pre-wrap rounded-[8px] bg-surface-2 p-2 font-mono text-[9px] leading-relaxed text-[#44506b]">{d.error || d.log}</pre>
                  </details>
                )}
              </div>
              {canRollback && d.status === 'successful' && lastSuccessful?.id === d.id && (
                <button className="icon-btn" title="Roll back to this release" aria-label="Roll back" onClick={rollback}>
                  <RotateCcw size={13} />
                </button>
              )}
            </div>
          ))}
        </div>
      )}
    </Card>
  )
}

// Phase 13 rule config UI: create / edit / delete alert rules. Rule classes
// mirror the engine: threshold (metric vs threshold, hysteresis via
// duration + recovery margin), state (verbatim offline/down/crash/failed
// conditions), time (SSL expiration windows). Field availability follows the
// PATCH contract: name, class, metric and scope are immutable after create.
import { useCallback, useEffect, useState } from 'react'
import { Pencil, Plus, RefreshCw, Trash2, SlidersHorizontal } from 'lucide-react'
import { Card, EmptyState, SkeletonRows, pushToast } from '@epicpanel/ui'
import { DataTable } from '@epicpanel/tables'
import type { Column } from '@epicpanel/tables'
import { ConfirmDialog, ErrorNote, Field, FormRow, Modal, Select } from '@epicpanel/forms'
import { timeAgo } from '@epicpanel/core'
import { monitoring } from './client'
import { METRICS, SCOPES, SEVERITIES, metricLabel, ruleTrigger } from './data'
import type { RuleClass, RuleDraft, RuleRow } from './data'
import { FailedNote, SectionShell } from './bits'

const CLASS_LABEL: Record<RuleClass, string> = {
  threshold: 'Threshold',
  state: 'State',
  time: 'Time window',
}

function draftFrom(rule: RuleRow): RuleDraft {
  return {
    name: rule.name,
    description: rule.description,
    rule_class: rule.rule_class,
    metric: rule.metric,
    scope: rule.scope,
    scope_id: rule.scope_id,
    comparison: rule.comparison === 'lt' ? 'lt' : 'gt',
    threshold: String(rule.threshold),
    duration_seconds: String(rule.duration_seconds),
    recovery_margin: String(rule.recovery_margin),
    window_days: String(rule.window_days),
    severity: rule.severity,
  }
}

export function RulesSection() {
  const [rules, setRules] = useState<RuleRow[] | null>(null)
  const [failed, setFailed] = useState(false)
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<RuleRow | null>(null)
  const [deleting, setDeleting] = useState<RuleRow | null>(null)
  const [busy, setBusy] = useState(false)

  const load = useCallback(() => {
    monitoring
      .rules()
      .then((r) => {
        setRules(r.rules ?? [])
        setFailed(false)
      })
      .catch(() => setFailed(true))
  }, [])

  useEffect(() => {
    load()
  }, [load])

  const remove = async () => {
    if (!deleting) return
    setBusy(true)
    try {
      await monitoring.deleteRule(deleting.id)
      pushToast('success', `Rule "${deleting.name}" deleted`)
      setDeleting(null)
      load()
    } catch (ex) {
      pushToast('error', ex instanceof Error ? ex.message : 'Delete failed')
    } finally {
      setBusy(false)
    }
  }

  const columns: Column<RuleRow>[] = [
    {
      key: 'rule',
      header: 'Rule',
      render: (r) => (
        <div className="max-w-[300px]">
          <div className="table-primary">{r.name}</div>
          {r.description && <div className="table-secondary">{r.description}</div>}
        </div>
      ),
      filter: (r) => `${r.name} ${r.description}`,
    },
    { key: 'class', header: 'Class', render: (r) => <span className="badge-neutral">{CLASS_LABEL[r.rule_class] ?? r.rule_class}</span> },
    { key: 'metric', header: 'Metric', render: (r) => <span className="table-primary">{metricLabel(r.metric)}</span>, filter: (r) => metricLabel(r.metric) },
    {
      key: 'scope',
      header: 'Scope',
      render: (r) => (
        <div>
          <div className="table-primary capitalize">{r.scope}</div>
          <div className="table-secondary font-mono">{r.scope_id || 'every subject'}</div>
        </div>
      ),
      filter: (r) => `${r.scope} ${r.scope_id}`,
    },
    { key: 'trigger', header: 'Trigger', render: (r) => <span className="text-sub">{ruleTrigger(r)}</span> },
    {
      key: 'severity',
      header: 'Severity',
      render: (r) => {
        const cls = r.severity === 'critical' ? 'status-chip status-down' : r.severity === 'warning' ? 'status-chip status-warning' : 'badge-neutral'
        return <span className={cls}>{r.severity}</span>
      },
    },
    { key: 'updated', header: 'Updated', render: (r) => <span className="text-muted">{timeAgo(r.updated_at)}</span> },
    {
      key: 'actions',
      header: '',
      render: (r) => (
        <div className="flex justify-end gap-[5px]">
          <button className="icon-btn" title="Edit rule" onClick={() => setEditing(r)}>
            <Pencil size={13} />
          </button>
          <button className="icon-btn" title="Delete rule" onClick={() => setDeleting(r)}>
            <Trash2 size={13} />
          </button>
        </div>
      ),
    },
  ]

  return (
    <SectionShell
      crumb="Rules"
      title="Alert rules"
      subtitle="Threshold, state and time rules evaluated asynchronously on stream events and periodic sweeps — never in the request path."
      actions={
        <>
          <button className="btn-ghost" onClick={load} aria-label="Refresh rules">
            <RefreshCw size={15} /> Refresh
          </button>
          <button className="btn-brand" onClick={() => setCreating(true)}>
            <Plus size={16} /> New rule
          </button>
        </>
      }
    >
      {failed && <FailedNote what="Rules" onRetry={load} />}
      <Card className="!p-0">
        <DataTable
          columns={columns}
          rows={rules}
          rowKey={(r) => r.id}
          searchText={(r) => `${r.name} ${r.description} ${r.metric} ${r.scope} ${r.scope_id}`}
          loading={rules === null}
          minWidth={860}
          empty={
            failed ? (
              <EmptyState icon={<SlidersHorizontal size={20} />} title="Rules unavailable" subtitle="The rules request failed. Use refresh to retry." />
            ) : (
              <EmptyState
                icon={<SlidersHorizontal size={20} />}
                title="No alert rules"
                subtitle="Create the first rule — a threshold breach, a state condition or an SSL expiration window — and alerts start flowing into the feed."
                action={
                  <button className="btn-brand" onClick={() => setCreating(true)}>
                    <Plus size={15} /> New rule
                  </button>
                }
              />
            )
          }
        />
      </Card>

      <RuleModal open={creating} onClose={() => setCreating(false)} onSaved={load} draft={null} />
      <RuleModal open={!!editing} onClose={() => setEditing(null)} onSaved={load} draft={editing ? draftFrom(editing) : null} ruleId={editing?.id} />

      <ConfirmDialog
        open={!!deleting}
        onClose={() => setDeleting(null)}
        onConfirm={remove}
        busy={busy}
        title="Delete alert rule"
        message={`Delete rule "${deleting?.name ?? ''}"? Open alerts it raised stay in the feed; no new ones will be generated.`}
        confirmLabel="Delete rule"
      />
    </SectionShell>
  )
}

/**
 * Create (draft=null) or edit (draft+ruleId). The PATCH endpoint only accepts
 * description/comparison/threshold/duration/recovery/window/severity — name,
 * class, metric and scope are shown read-only while editing. Editing always
 * re-enables the rule (backend contract); there is no disable toggle yet.
 */
function RuleModal({ open, onClose, onSaved, draft, ruleId }: {
  open: boolean
  onClose: () => void
  onSaved: () => void
  draft: RuleDraft | null
  ruleId?: string
}) {
  const isEdit = !!ruleId
  const [form, setForm] = useState<RuleDraft>(draft ?? emptyLocal())
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  function emptyLocal(): RuleDraft {
    return {
      name: '',
      description: '',
      rule_class: 'threshold',
      metric: 'cpu',
      scope: 'fleet',
      scope_id: '',
      comparison: 'gt',
      threshold: '90',
      duration_seconds: '120',
      recovery_margin: '5',
      window_days: '30',
      severity: 'warning',
    }
  }

  useEffect(() => {
    if (open) {
      setForm(draft ?? emptyLocal())
      setErr('')
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, draft])

  const cls = form.rule_class
  const metricOptions = (METRICS[cls] ?? []).map((m) => ({ value: m.value, label: m.label }))
  const unit = METRICS[cls]?.find((m) => m.value === form.metric)?.unit ?? ''

  const set = (patch: Partial<RuleDraft>) => setForm((f) => ({ ...f, ...patch }))

  const save = async () => {
    setErr('')
    const name = form.name.trim()
    if (!isEdit && !name) {
      setErr('Name is required.')
      return
    }
    const threshold = Number(form.threshold)
    const duration = Number(form.duration_seconds)
    const recovery = Number(form.recovery_margin)
    const windowDays = Number(form.window_days)
    if (cls === 'threshold' && (!Number.isFinite(threshold) || form.threshold === '')) {
      setErr('Threshold must be a number.')
      return
    }
    if (cls === 'time' && (!Number.isInteger(windowDays) || windowDays <= 0)) {
      setErr('SSL expiration needs a positive window in days.')
      return
    }
    setBusy(true)
    try {
      if (isEdit) {
        await monitoring.updateRule(ruleId!, {
          description: form.description,
          comparison: form.comparison,
          threshold: cls === 'threshold' ? threshold : 0,
          duration_seconds: cls === 'threshold' ? (Number.isFinite(duration) ? duration : 0) : 0,
          recovery_margin: cls === 'threshold' ? (Number.isFinite(recovery) ? recovery : 0) : 0,
          window_days: cls === 'time' ? windowDays : 0,
          severity: form.severity,
        })
        pushToast('success', 'Rule updated')
      } else {
        await monitoring.createRule({
          name,
          description: form.description,
          rule_class: cls,
          metric: form.metric,
          scope: form.scope,
          scope_id: form.scope_id.trim(),
          comparison: cls === 'threshold' ? form.comparison : 'gt',
          threshold: cls === 'threshold' ? threshold : 0,
          duration_seconds: cls === 'threshold' ? (Number.isFinite(duration) ? duration : 0) : 0,
          recovery_margin: cls === 'threshold' ? (Number.isFinite(recovery) ? recovery : 0) : 0,
          window_days: cls === 'time' ? windowDays : 0,
          severity: form.severity,
        })
        pushToast('success', `Rule "${name}" created`)
      }
      onSaved()
      onClose()
    } catch (ex) {
      setErr(ex instanceof Error ? ex.message : 'Save failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={isEdit ? 'Edit rule' : 'New alert rule'}
      subtitle={isEdit ? 'Class, metric and scope are fixed after creation.' : 'Evaluated on stream events + periodic sweeps — never inside requests.'}
      width="max-w-[560px]"
    >
      <ErrorNote message={err} />
      {isEdit ? (
        <FormRow cols={2}>
          <Field label="Name">
            <input className="input opacity-60" value={form.name} disabled />
          </Field>
          <Field label="Class / metric">
            <input className="input opacity-60" value={`${CLASS_LABEL[form.rule_class]} · ${metricLabel(form.metric)}`} disabled />
          </Field>
        </FormRow>
      ) : (
        <>
          <Field label="Name">
            <input className="input" value={form.name} onChange={(e) => set({ name: e.target.value })} placeholder="Node CPU saturation" autoFocus />
          </Field>
          <FormRow cols={2}>
            <Field label="Rule class">
              <Select
                value={cls}
                onChange={(v) => set({ rule_class: v as RuleClass, metric: (METRICS[v as RuleClass][0] ?? { value: '' }).value })}
                options={[
                  { value: 'threshold', label: 'Threshold (metric vs limit)' },
                  { value: 'state', label: 'State (offline / failed / crashed)' },
                  { value: 'time', label: 'Time window (SSL expiration)' },
                ]}
              />
            </Field>
            <Field label="Metric">
              <Select value={form.metric} onChange={(v) => set({ metric: v })} options={metricOptions} />
            </Field>
          </FormRow>
        </>
      )}
      <Field label="Description" hint="shown next to the rule in the feed">
        <input className="input" value={form.description} onChange={(e) => set({ description: e.target.value })} placeholder="What this rule watches and why" />
      </Field>
      {!isEdit && (
        <FormRow cols={2}>
          <Field label="Scope">
            <Select value={form.scope} onChange={(v) => set({ scope: v })} options={SCOPES} />
          </Field>
          <Field label="Subject id" hint="optional — empty matches every subject of the scope">
            <input className="input font-mono" value={form.scope_id} onChange={(e) => set({ scope_id: e.target.value })} placeholder="server / website id" />
          </Field>
        </FormRow>
      )}
      {cls === 'threshold' && (
        <>
          <FormRow cols={2}>
            <Field label="Comparison">
              <Select
                value={form.comparison}
                onChange={(v) => set({ comparison: v })}
                options={[
                  { value: 'gt', label: 'Greater than' },
                  { value: 'lt', label: 'Less than' },
                ]}
              />
            </Field>
            <Field label={`Threshold${unit ? ` (${unit})` : ''}`}>
              <input className="input" value={form.threshold} onChange={(e) => set({ threshold: e.target.value.replace(/[^0-9.]/g, '') })} />
            </Field>
          </FormRow>
          <FormRow cols={2}>
            <Field label="Breach duration (s)" hint="alert after this much continuous breach">
              <input className="input" value={form.duration_seconds} onChange={(e) => set({ duration_seconds: e.target.value.replace(/[^0-9]/g, '') })} />
            </Field>
            <Field label="Recovery margin" hint="hysteresis: value must recover past threshold minus this before the alert clears">
              <input className="input" value={form.recovery_margin} onChange={(e) => set({ recovery_margin: e.target.value.replace(/[^0-9.]/g, '') })} />
            </Field>
          </FormRow>
        </>
      )}
      {cls === 'time' && (
        <Field label="Expiration window (days)" hint="alert when the certificate expires within this many days (e.g. 30 / 14 / 7)">
          <input className="input" value={form.window_days} onChange={(e) => set({ window_days: e.target.value.replace(/[^0-9]/g, '') })} />
        </Field>
      )}
      <Field label="Severity">
        <Select value={form.severity} onChange={(v) => set({ severity: v })} options={SEVERITIES} />
      </Field>
      <div className="mt-4 flex justify-end gap-2">
        <button className="btn-ghost" onClick={onClose} disabled={busy}>Cancel</button>
        <button className="btn-brand" onClick={save} disabled={busy}>
          {busy ? 'Saving…' : isEdit ? 'Save changes' : 'Create rule'}
        </button>
      </div>
    </Modal>
  )
}

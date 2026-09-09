// Workload drill-downs — Minecraft (TPS / MSPT / Players) and Discord
// (CPU / RAM / Uptime), from GET /v1/admin/observability/workloads filtered
// by kind. The backend workload projection carries no RAM percent (no limit
// in the stream sample), so the RAM column renders an honest "—" until the
 // stream exposes it; every other value is real telemetry.
import { Gamepad2, Bot, RefreshCw } from 'lucide-react'
import { Card, EmptyState } from '@epicpanel/ui'
import { DataTable } from '@epicpanel/tables'
import type { Column } from '@epicpanel/tables'
import { monitoring } from './client'
import type { ObsWorkload } from './data'
import { fmtUptime, liveState, shortId } from './data'
import { FailedNote, SectionShell, StateChip, usePolled } from './bits'
import type { UIState } from './bits'

function workloadName(w: ObsWorkload): string {
  const site = w.ref.split(':')[1] ?? ''
  return site ? `site ${shortId(site)}` : w.ref
}

function WorkloadTable({ kind, icon, emptyTitle, emptySubtitle, columns, minWidth }: {
  kind: string
  icon: React.ReactNode
  emptyTitle: string
  emptySubtitle: string
  columns: Column<ObsWorkload>[]
  minWidth: number
}) {
  const { data, failed, reload } = usePolled(() => monitoring.obsWorkloads(), [])
  const rows = (data?.workloads ?? []).filter((w) => w.kind === kind)

  return (
    <SectionShell
      crumb={kind === 'minecraft' ? 'Minecraft' : 'Discord'}
      title={kind === 'minecraft' ? 'Minecraft' : 'Discord'}
      subtitle={kind === 'minecraft'
        ? 'Game workload telemetry — TPS, MSPT and player counts per instance.'
        : 'Bot workload telemetry — CPU, memory and uptime per bot instance.'}
      actions={<button className="btn-ghost" onClick={reload} aria-label="Refresh"><RefreshCw size={15} /> Refresh</button>}
    >
      {failed && <FailedNote what="Workload observability" onRetry={reload} />}
      <Card className="!p-0">
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(r) => r.ref}
          searchText={(r) => `${r.ref} ${r.state}`}
          loading={data === null && !failed}
          minWidth={minWidth}
          empty={
            failed ? (
              <EmptyState icon={icon} title="Workload observability unavailable" subtitle="The request failed. Use refresh to retry." />
            ) : (
              <EmptyState icon={icon} title={emptyTitle} subtitle={emptySubtitle} />
            )
          }
        />
      </Card>
    </SectionShell>
  )
}

const stateColumn: Column<ObsWorkload> = {
  key: 'state',
  header: 'Health',
  render: (w) => <StateChip state={liveState(w.state) as UIState} />,
}

const refColumn: Column<ObsWorkload> = {
  key: 'ref',
  header: 'Instance',
  render: (w) => (
    <div className="max-w-[220px]">
      <div className="table-primary">{workloadName(w)}</div>
      <div className="table-secondary font-mono">{w.ref}</div>
    </div>
  ),
  filter: (w) => w.ref,
}

export function MinecraftSection() {
  const columns: Column<ObsWorkload>[] = [
    refColumn,
    stateColumn,
    {
      key: 'tps',
      header: 'TPS',
      render: (w) =>
        w.tps > 0 ? <TpsCell tps={w.tps} /> : <span className="badge-neutral" title="not reported (RCON telemetry unavailable)">not reported</span>,
    },
    {
      key: 'mspt',
      header: 'MSPT',
      render: (w) => (w.mspt > 0 ? <span className="font-semibold text-ink">{w.mspt.toFixed(1)} ms</span> : <span className="text-muted">—</span>),
    },
    { key: 'players', header: 'Players', render: (w) => <span className="font-semibold text-ink">{w.players}</span> },
    {
      key: 'cpu',
      header: 'CPU',
      render: (w) => <span className={`font-semibold ${w.cpu_percent >= 90 ? 'text-danger' : 'text-ink'}`}>{Math.round(w.cpu_percent)}%</span>,
    },
  ]
  return (
    <WorkloadTable
      kind="minecraft"
      icon={<Gamepad2 size={20} />}
      emptyTitle="No Minecraft instances reporting"
      emptySubtitle="Instances appear here once their node streams app samples."
      columns={columns}
      minWidth={760}
    />
  )
}

function TpsCell({ tps }: { tps: number }) {
  const cls = tps >= 19 ? 'text-ok' : tps >= 15 ? 'text-warn' : 'text-danger'
  return <span className={`font-semibold ${cls}`}>{tps.toFixed(1)}</span>
}

export function DiscordSection() {
  const columns: Column<ObsWorkload>[] = [
    refColumn,
    stateColumn,
    {
      key: 'cpu',
      header: 'CPU',
      render: (w) => <span className={`font-semibold ${w.cpu_percent >= 90 ? 'text-danger' : 'text-ink'}`}>{Math.round(w.cpu_percent)}%</span>,
    },
    {
      key: 'ram',
      header: 'RAM',
      render: () => <span className="text-muted" title="the stream sample carries no memory limit — percent not computable">—</span>,
    },
    { key: 'uptime', header: 'Uptime', render: (w) => <span className="font-semibold text-ink">{fmtUptime(w.uptime_s)}</span> },
    {
      key: 'restarts',
      header: 'Restarts',
      render: (w) => (
        <span className={`font-semibold ${w.restarts > 0 ? 'text-warn' : 'text-ink'}`}>{w.restarts}</span>
      ),
    },
  ]
  return (
    <WorkloadTable
      kind="discord"
      icon={<Bot size={20} />}
      emptyTitle="No Discord bots reporting"
      emptySubtitle="Bots appear here once their node streams app samples."
      columns={columns}
      minWidth={760}
    />
  )
}

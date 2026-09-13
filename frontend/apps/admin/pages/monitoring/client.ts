// Phase 13 monitoring API client: alerts feed + rule CRUD + observability
// drill-downs, all under /v1/admin (platform-admin session; enforced and
// audited server-side). Built on the shared @epicpanel/core api client.
import { api } from '@epicpanel/core'
import type {
  AlertRow,
  AlertState,
  ObsCustomer,
  ObsNode,
  ObsService,
  RuleRow,
} from './data'

export const monitoring = {
  alerts: (state: AlertState, limit = 200) =>
    api.get<{ alerts: AlertRow[] }>(`/v1/admin/alerts?state=${state}&limit=${limit}`),
  ackAlert: (id: string) => api.post<null>(`/v1/admin/alerts/${id}/ack`),
  resolveAlert: (id: string) => api.post<null>(`/v1/admin/alerts/${id}/resolve`),

  rules: () => api.get<{ rules: RuleRow[] }>('/v1/admin/alert-rules'),
  createRule: (body: unknown) => api.post<RuleRow>('/v1/admin/alert-rules', body),
  updateRule: (id: string, body: unknown) => api.patch<RuleRow>(`/v1/admin/alert-rules/${id}`, body),
  deleteRule: (id: string) => api.del<null>(`/v1/admin/alert-rules/${id}`),

  obsNodes: () => api.get<{ nodes: ObsNode[] }>('/v1/admin/observability/nodes'),
  obsServices: () => api.get<{ services: ObsService[] }>('/v1/admin/observability/services'),
  obsCustomers: () => api.get<{ customers: ObsCustomer[] }>('/v1/admin/observability/customers'),
}

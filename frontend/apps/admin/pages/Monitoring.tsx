// Phase 13 monitoring entry (kept at the P6 path so the existing import in
// routes.admin.tsx keeps resolving). The real implementation lives in
// pages/monitoring/. MonitoringOutlet handles both /monitoring (hub) and
// /monitoring/:section (alerts | rules | nodes | services | customers;
// unknown values fall back to the hub).
export { MonitoringOutlet as MonitoringPage } from './monitoring/Monitoring'

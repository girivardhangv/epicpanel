package adminview

// Fleet overview aggregates for the WHM dashboard (Phase 6). Counts come
// from PostgreSQL; instantaneous consumption, node health and service
// states come ONLY from the in-memory live store (Phase 3 contract: the
// live dashboard never reads the historical DB).

import (
	"context"
	"time"
)

type Overview struct {
	Accounts     map[string]any   `json:"accounts"`
	Customers    map[string]any   `json:"customers"`
	Domains      map[string]any   `json:"domains"`
	Databases    int64            `json:"databases"`
	Backups      map[string]any   `json:"backups"`
	Jobs         map[string]any   `json:"jobs"`
	Alerts       map[string]any   `json:"alerts"`
	Nodes        map[string]any   `json:"nodes"`
	Fleet        map[string]any   `json:"fleet"`
	Distribution map[string]any   `json:"distribution"`
	Ports        map[string]any   `json:"ports"`
	Services     []ServiceState   `json:"services"`
	FailedJobs   []map[string]any `json:"failed_jobs"`
}

// ServiceState is the fleet-wide health of one platform service
// (nginx/apache/ols/php-fpm/mariadb/docker...) across reporting nodes.
type ServiceState struct {
	Name string `json:"name"`
	Ok   int    `json:"ok"`
	Down int    `json:"down"`
}

func (s *Service) Overview(ctx context.Context) (*Overview, error) {
	o := &Overview{
		Accounts:  map[string]any{},
		Customers: map[string]any{},
		Domains:   map[string]any{},
		Backups:   map[string]any{},
		Jobs:      map[string]any{},
		Alerts:    map[string]any{},
		Nodes:     map[string]any{},
		Fleet: map[string]any{
			"cpu_pct": 0, "mem_used_bytes": 0, "mem_total_bytes": 0,
			"disk_used_bytes": 0, "disk_total_bytes": 0, "rx_bps": 0, "tx_bps": 0,
			"sampled_nodes": 0,
		},
		Distribution: map[string]any{},
		Ports:        map[string]any{},
		Services:     []ServiceState{},
		FailedJobs:   []map[string]any{},
	}

	scan := func(q string, dest ...any) error { return s.Pool.QueryRow(ctx, q).Scan(dest...) }

	var accountsTotal, accountsActive, accountsSuspended, accountsFailed int64
	if err := scan(`SELECT count(*),
			count(*) FILTER (WHERE status = 'ready'),
			count(*) FILTER (WHERE status = 'suspended'),
			count(*) FILTER (WHERE status = 'failed')
		FROM websites WHERE status NOT IN ('deleted','deleting')`,
		&accountsTotal, &accountsActive, &accountsSuspended, &accountsFailed); err != nil {
		return nil, err
	}
	o.Accounts["total"] = accountsTotal
	o.Accounts["active"] = accountsActive
	o.Accounts["suspended"] = accountsSuspended
	o.Accounts["failed"] = accountsFailed

	var orgs, users int64
	if err := scan(`SELECT (SELECT count(*) FROM organizations), (SELECT count(*) FROM users)`, &orgs, &users); err != nil {
		return nil, err
	}
	o.Customers["orgs"] = orgs
	o.Customers["users"] = users

	var domainsTotal, sslActive, sslExpiring int64
	if err := scan(`SELECT count(*),
			count(*) FILTER (WHERE ssl_mode = 'letsencrypt' AND ssl_state = 'active'),
			count(*) FILTER (WHERE ssl_mode = 'letsencrypt' AND ssl_state = 'active' AND ssl_expires_at < now() + interval '30 days')
		FROM domains`, &domainsTotal, &sslActive, &sslExpiring); err != nil {
		return nil, err
	}
	o.Domains["total"] = domainsTotal
	o.Domains["ssl_active"] = sslActive
	o.Domains["ssl_expiring_30d"] = sslExpiring

	if err := scan(`SELECT count(*) FROM databases WHERE status <> 'deleting'`, &o.Databases); err != nil {
		return nil, err
	}

	var backupsTotal, backups7d, backupsFailed7d int64
	if err := scan(`SELECT count(*),
			count(*) FILTER (WHERE created_at > now() - interval '7 days'),
			count(*) FILTER (WHERE created_at > now() - interval '7 days' AND status = 'failed')
		FROM backups`, &backupsTotal, &backups7d, &backupsFailed7d); err != nil {
		return nil, err
	}
	o.Backups["total"] = backupsTotal
	o.Backups["last_7d"] = backups7d
	o.Backups["failed_7d"] = backupsFailed7d

	var jobsPending, jobsRunning, jobsFailed, jobsSuccess24h int64
	if err := scan(`SELECT
			count(*) FILTER (WHERE status = 'pending'),
			count(*) FILTER (WHERE status = 'running'),
			count(*) FILTER (WHERE status = 'failed'),
			count(*) FILTER (WHERE status = 'success' AND finished_at > now() - interval '24 hours')
		FROM jobs`, &jobsPending, &jobsRunning, &jobsFailed, &jobsSuccess24h); err != nil {
		return nil, err
	}
	o.Jobs["pending"] = jobsPending
	o.Jobs["running"] = jobsRunning
	o.Jobs["failed"] = jobsFailed
	o.Jobs["success_24h"] = jobsSuccess24h

	var alertsOpen int64
	if err := scan(`SELECT count(*) FROM alerts WHERE resolved_at IS NULL`, &alertsOpen); err != nil {
		return nil, err
	}
	o.Alerts["unresolved"] = alertsOpen

	var nodesTotal, nodesMaintenance int64
	if err := scan(`SELECT count(*), count(*) FILTER (WHERE maintenance_mode) FROM servers`, &nodesTotal, &nodesMaintenance); err != nil {
		return nil, err
	}

	// --- live fleet (in-memory only) ---
	frames := s.LiveFrames()
	svc := map[string]*ServiceState{}
	nodeStates := map[string]int{"total": int(nodesTotal), "maintenance": int(nodesMaintenance)}
	sampleAge := time.Duration(-1)
	now := time.Now().UTC()
	var fleetCPU, fleetRx, fleetTx float64
	var memUsed, memTotal, diskUsed, diskTotal int64
	sampled := 0
	for i := range frames {
		f := frames[i]
		st := f.NodeState
		if st == "" {
			st = "OFFLINE"
		}
		nodeStates[st]++
		if st != "ONLINE" || f.Sample == nil {
			continue
		}
		sampled++
		node := f.Sample.Node
		fleetCPU += node.CPUPercent
		memUsed += node.MemoryUsed
		memTotal += node.MemoryTotal
		fleetRx += node.Net.RxBPS
		fleetTx += node.Net.TxBPS
		for _, d := range node.Disks {
			diskUsed += d.UsedBytes
			diskTotal += d.TotalBytes
		}
		age := now.Sub(f.CollectedAt)
		if sampleAge < 0 || age < sampleAge {
			sampleAge = age
		}
		for _, sv := range node.Services {
			st := svc[sv.Name]
			if st == nil {
				st = &ServiceState{Name: sv.Name}
				svc[sv.Name] = st
			}
			// systemctl states: active (= ok), everything else counted down.
			if sv.State == "active" || sv.State == "running" {
				st.Ok++
			} else {
				st.Down++
			}
		}
	}
	if sampled > 0 {
		o.Fleet["cpu_pct"] = fleetCPU / float64(sampled)
	}
	o.Fleet["mem_used_bytes"] = memUsed
	o.Fleet["mem_total_bytes"] = memTotal
	o.Fleet["disk_used_bytes"] = diskUsed
	o.Fleet["disk_total_bytes"] = diskTotal
	o.Fleet["rx_bps"] = fleetRx
	o.Fleet["tx_bps"] = fleetTx
	o.Fleet["sampled_nodes"] = sampled
	if sampleAge >= 0 && sampled > 0 {
		o.Fleet["freshness"] = map[string]any{
			"state":  freshnessOf(sampleAge),
			"age_ms": sampleAge.Milliseconds(),
		}
	} else {
		o.Fleet["freshness"] = map[string]any{"state": "OFFLINE", "age_ms": nil}
	}
	o.Nodes = map[string]any{
		"total": nodeStates["total"], "maintenance": nodeStates["maintenance"],
		"online": nodeStates["ONLINE"], "stale": nodeStates["STALE"], "offline": nodeStates["OFFLINE"],
	}
	for _, st := range svc {
		o.Services = append(o.Services, *st)
	}
	if o.Services == nil {
		o.Services = []ServiceState{}
	}

	// --- distribution (accounts per plan / status / node) ---
	byPlan, err := s.groupedCounts(ctx, `
		SELECT coalesce(p.name, 'Unassigned') AS k, count(*)
		FROM websites w
		JOIN organizations o ON o.id = w.organization_id
		LEFT JOIN hosting_packages p ON p.id = o.package_id
		WHERE w.status NOT IN ('deleted','deleting')
		GROUP BY k ORDER BY count(*) DESC`)
	if err != nil {
		return nil, err
	}
	byStatus, err := s.groupedCounts(ctx, `
		SELECT status::text, count(*) FROM websites
		WHERE status NOT IN ('deleted','deleting') GROUP BY status ORDER BY count(*) DESC`)
	if err != nil {
		return nil, err
	}
	byNode, err := s.groupedCounts(ctx, `
		SELECT sv.name, count(*) FROM websites w JOIN servers sv ON sv.id = w.server_id
		WHERE w.status NOT IN ('deleted','deleting') GROUP BY sv.name ORDER BY count(*) DESC`)
	if err != nil {
		return nil, err
	}
	o.Distribution["by_plan"] = byPlan
	o.Distribution["by_status"] = byStatus
	o.Distribution["by_node"] = byNode

	// --- port pools summary ---
	allocs, err := s.PortPools(ctx)
	if err != nil {
		return nil, err
	}
	o.Ports = map[string]any{
		"apache": allocs["apache"], "ols": allocs["ols"],
	}

	// --- recent failed jobs (dead-letter head) ---
	frows, err := s.Pool.Query(ctx, `
		SELECT j.id::text, j.type::text, coalesce(sv.name, ''), coalesce(w.name, ''), coalesce(j.error, ''), j.created_at
		FROM jobs j
		JOIN servers sv ON sv.id = j.server_id
		LEFT JOIN websites w ON w.id = j.website_id
		WHERE j.status = 'failed'
		ORDER BY j.created_at DESC LIMIT 8`)
	if err != nil {
		return nil, err
	}
	defer frows.Close()
	for frows.Next() {
		var id, typ, server, website, errMsg string
		var created time.Time
		if err := frows.Scan(&id, &typ, &server, &website, &errMsg, &created); err != nil {
			return nil, err
		}
		o.FailedJobs = append(o.FailedJobs, map[string]any{
			"id": id, "type": typ, "server": server, "website": website,
			"error": errMsg, "created_at": iso(created),
		})
	}
	if err := frows.Err(); err != nil {
		return nil, err
	}
	return o, nil
}

func (s *Service) groupedCounts(ctx context.Context, q string) ([]map[string]any, error) {
	rows, err := s.Pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var k string
		var n int64
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"key": k, "count": n})
	}
	return out, rows.Err()
}

// freshnessOf mirrors agentproto.Freshness thresholds (LIVE <= 15s,
// STALE <= 120s) without importing the wire package.
func freshnessOf(age time.Duration) string {
	switch {
	case age <= 15*time.Second:
		return "LIVE"
	case age <= 120*time.Second:
		return "STALE"
	default:
		return "OFFLINE"
	}
}

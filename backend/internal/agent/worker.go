package agent

import (
	"context"
	"encoding/json"
	"log/slog"
)

// PollAndExecute claims one job and executes it. Returns whether a job was
// processed so the caller can loop without waiting for the next tick when the
// queue is backed up.
func PollAndExecute(ctx context.Context, c *Client, e *Executor, cfg Config) (bool, error) {
	job, err := c.Claim(ctx)
	if err != nil {
		return false, err
	}
	if job == nil {
		return false, nil
	}

	slog.Info("job claimed", "id", job.ID, "type", job.Type, "attempt", job.Attempts)

	var resultJSON json.RawMessage
	var execErr error

	switch job.Type {
	case "provision_website":
		var p ProvisionPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.ProvisionWebsite(ctx, p)
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
			}
		}
	case "delete_website":
		var p ProvisionPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			execErr = e.DeleteWebsite(ctx, p)
		}
	case "install_runtime":
		var p InstallJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			prog := func(percent int, step string) { c.ReportProgress(ctx, job.ID, percent, step) }
			c.ReportProgress(ctx, job.ID, 5, "Starting install of "+p.Type+" "+p.Version+"…")
			execErr = e.InstallRuntime(ctx, p.RuntimeID, p.Type, p.Version, prog)
		}
	case "install_extension":
		var p ExtensionJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			c.ReportProgress(ctx, job.ID, 30, "Installing "+p.Type+" "+p.Version+" extension "+p.Name+"…")
			execErr = e.InstallPHPExtension(ctx, p.Type, p.Version, p.Name)
		}
	case "remove_extension":
		var p ExtensionJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			c.ReportProgress(ctx, job.ID, 30, "Removing "+p.Type+" "+p.Version+" extension "+p.Name+"…")
			execErr = e.RemovePHPExtension(ctx, p.Type, p.Version, p.Name)
		}
	case "detect_software":
		items := e.DetectSoftware(ctx)
		if b, err := json.Marshal(items); err == nil {
			resultJSON = b
		}
	case "site_usage":
		var p SiteUsageJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.SiteUsage(ctx, p.WebsiteID)
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
			}
		}
	case "remove_runtime":
		var p InstallJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			execErr = e.RemoveRuntime(ctx, p.RuntimeID, p.Type, p.Version)
		}
	case "create_database":
		var p DatabaseJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			password, err := e.CreateDatabase(ctx, p.Engine, p.Name, p.DbUser)
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(DatabaseOutcome{Password: password}); err == nil {
				resultJSON = b
			}
		}
	case "delete_database":
		var p DatabaseJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			execErr = e.DeleteDatabase(ctx, p.Engine, p.Name, p.DbUser)
		}
	case "issue_certificate":
		var p CertificateJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.EnsureCertificate(ctx, p.Domain, p.Mode, cfg.ACMEEmail, cfg.ACMEDirectory)
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
			}
		}
	case "verify_domain":
		var p VerifyDomainJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			matches, resolved, err := e.VerifyDNS(ctx, p.Domain)
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(DNSOutcome{Matches: matches, Resolved: resolved}); err == nil {
				resultJSON = b
			}
		}
	case "deploy_website":
		var p DeployJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.DeployGit(ctx, deploySpecFromPayload(p))
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
				e.reloadFPMIfPHP(ctx, p.Runtime, p.RuntimeVersion)
			}
		}
	case "rollback_website":
		var p RollbackJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.RollbackGit(ctx, p.WebsiteID, p.TargetReleaseDir, p.TargetCommitSHA)
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
			}
		}
	case "clone_staging":
		var p StagingJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.CloneStaging(ctx, p.SourceWebsiteID, p.TargetWebsiteID, toDBClones(p.Databases))
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
			}
		}
	case "promote_staging":
		var p StagingJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.PromoteStaging(ctx, p.SourceWebsiteID, p.TargetWebsiteID, toDBClones(p.Databases))
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
			}
		}
	case "create_backup":
		var p BackupJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.CreateBackup(ctx, p.BackupID, p.WebsiteID, toDBClones(p.Databases))
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
			}
		}
	case "build_app":
		var p AppSpec
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.DeployApp(ctx, p)
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
			}
		}
	case "start_app":
		var p AppSpec
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.StartApp(ctx, p)
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
			}
		}
	case "stop_app":
		var p struct {
			WebsiteID string `json:"website_id"`
		}
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			execErr = e.StopApp(ctx, p.WebsiteID)
		}
	case "restart_app":
		var p AppSpec
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.RestartApp(ctx, p)
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
			}
		}
	case "app_status":
		var p struct {
			WebsiteID    string `json:"website_id"`
			InternalPort int    `json:"internal_port"`
		}
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.AppStatus(ctx, p.WebsiteID, p.InternalPort)
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
			}
		}
	case "app_logs":
		var p struct {
			WebsiteID string `json:"website_id"`
			Lines     int    `json:"lines"`
		}
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			logs, err := e.AppLogs(ctx, p.WebsiteID, p.Lines)
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(map[string]string{"logs": logs}); err == nil {
				resultJSON = b
			}
		}
	case "sync_crontab":
		var p struct {
			WebsiteID string      `json:"website_id"`
			Entries   []CronEntry `json:"entries"`
		}
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.SyncCrontab(ctx, p.WebsiteID, p.Entries)
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
			}
		}
	case "install_wordpress":
		var p WPPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.InstallWordPress(ctx, p)
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
			}
		}
	case "install_laravel":
		var p LaravelPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.InstallLaravel(ctx, p)
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
			}
		}
	case "site_command":
		var p CommandPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.RunSiteCommand(ctx, p)
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
			}
		}
	case "sync_ssh_keys":
		var p struct {
			WebsiteID string   `json:"website_id"`
			Keys      []string `json:"keys"`
		}
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			execErr = e.SyncSSHKeys(ctx, p.WebsiteID, p.Keys)
		}
	case "install_database_tools":
		var p struct {
			PHPVersion string `json:"php_version"`
		}
		_ = json.Unmarshal(job.Payload, &p) // empty payload = auto (highest installed)
		ip, ssoKey, err := e.InstallDatabaseTools(ctx, p.PHPVersion)
		if err != nil {
			execErr = err
		} else if b, err := json.Marshal(DBToolsOutcome{IP: ip, SSOKey: ssoKey}); err == nil {
			resultJSON = b
		}
	case "apply_quota":
		var p QuotaJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.ApplyQuota(ctx, p)
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
			}
		}
	case "restore_backup":
		var p RestoreJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			execErr = e.RestoreBackup(ctx, p.BackupID, p.WebsiteID, toDBClones(p.Databases))
			if execErr == nil {
				e.reloadFPMIfPHP(ctx, p.Runtime, p.RuntimeVersion)
			}
		}
	case "sync_ftp_accounts":
		var p FTPSyncPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.SyncFTPAccounts(ctx, p)
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
			}
		}
	case "sync_dns_zone":
		var p DNSZonePayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.SyncDNSZone(ctx, p)
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
			}
		}
	case "suspend_website":
		var p LifecycleJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.SuspendWebsite(ctx, p)
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
			}
		}
	case "resume_website":
		var p LifecycleJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.ResumeWebsite(ctx, p)
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
			}
		}
	case "terminate_website":
		var p LifecycleJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.TerminateWebsite(ctx, p)
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
			}
		}
	case "enforce_limits":
		var p EnforceJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.EnforceLimits(ctx, p)
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
			}
		}
	case "recalc_bandwidth":
		var p RecalcBandwidthPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			execErr = err
		} else {
			outcome, err := e.RecalcBandwidth(ctx, p)
			if err != nil {
				execErr = err
			} else if b, err := json.Marshal(outcome); err == nil {
				resultJSON = b
			}
		}
	default:
		// Phase 11 backup registry (coordinator-merged dispatch).
		if fn, ok := BackupOps2[job.Type]; ok {
			resultJSON, execErr = fn(ctx, e, c, cfg, job.Payload)
		} else {
			execErr = errUnknownJobType(job.Type)
		}
	}

	req := ResultRequest{Success: execErr == nil, Error: "", Result: resultJSON}
	if execErr != nil {
		req.Error = execErr.Error()
		slog.Warn("job failed", "id", job.ID, "type", job.Type, "err", execErr)
	} else {
		slog.Info("job succeeded", "id", job.ID, "type", job.Type)
	}

	if err := c.ReportResult(ctx, job.ID, req); err != nil {
		slog.Error("report result failed", "id", job.ID, "err", err)
		return true, err
	}
	return true, nil
}

type unknownJobTypeError string

func (e unknownJobTypeError) Error() string { return "unknown job type: " + string(e) }

func errUnknownJobType(t string) error { return unknownJobTypeError(t) }

// InstallJobPayload matches the runtimes.InstallPayload the control plane enqueues.
type InstallJobPayload struct {
	RuntimeID string `json:"runtime_id"`
	Type      string `json:"type"`
	Version   string `json:"version"`
}

// DatabaseJobPayload matches databases.CreatePayload.
type DatabaseJobPayload struct {
	DatabaseID string `json:"database_id"`
	Engine     string `json:"engine"`
	Name       string `json:"name"`
	DbUser     string `json:"db_user"`
}

// CertificateJobPayload matches domains.CertPayload.
type CertificateJobPayload struct {
	DomainID string `json:"domain_id"`
	Domain   string `json:"domain"`
	Mode     string `json:"mode"`
}

// VerifyDomainJobPayload matches domains.VerifyDNSPayload.
type VerifyDomainJobPayload struct {
	DomainID string `json:"domain_id"`
	Domain   string `json:"domain"`
}

type DNSOutcome struct {
	Matches  bool     `json:"matches"`
	Resolved []string `json:"resolved"`
}

// DatabaseOutcome carries the generated credential back in the job result.
type DatabaseOutcome struct {
	Password string `json:"password"`
}

// DeployJobPayload matches deployments.DeployPayload.
type DeployJobPayload struct {
	DeploymentID    string `json:"deployment_id"`
	WebsiteID       string `json:"website_id"`
	RepoURL         string `json:"repo_url"`
	Branch          string `json:"branch"`
	WebDir          string `json:"web_dir,omitempty"`
	AutoBuild       bool   `json:"auto_build,omitempty"`
	TokenEncrypted  string `json:"token_encrypted,omitempty"`
	Runtime         string `json:"runtime,omitempty"`
	RuntimeVersion  string `json:"runtime_version,omitempty"`
	BuildCommand    string `json:"build_command,omitempty"`
	UnixUser        string `json:"unix_user,omitempty"`
	StartupCommand  string `json:"app_startup_command,omitempty"`
	AppPort         int    `json:"app_port,omitempty"`
	AppDesiredState string `json:"app_desired_state,omitempty"`
	AppEnvEnc       string `json:"app_env_enc,omitempty"`
}

// deploySpecFromPayload adapts the job payload to the DeploySpec the deploy
// engine builds against.
func deploySpecFromPayload(p DeployJobPayload) DeploySpec {
	return DeploySpec{
		WebsiteID:       p.WebsiteID,
		RepoURL:         p.RepoURL,
		Branch:          p.Branch,
		WebDir:          p.WebDir,
		AutoBuild:       p.AutoBuild,
		TokenCipherB64:  p.TokenEncrypted,
		Runtime:         p.Runtime,
		RuntimeVersion:  p.RuntimeVersion,
		BuildCommand:    p.BuildCommand,
		UnixUser:        p.UnixUser,
		StartupCommand:  p.StartupCommand,
		AppPort:         p.AppPort,
		AppDesiredState: p.AppDesiredState,
		AppEnvEnc:       p.AppEnvEnc,
	}
}

// RollbackJobPayload matches deployments.RollbackPayload. TargetCommitSHA
// drives workdir-model sites (reset --hard); TargetReleaseDir stays for
// legacy sites still on the release symlink.
type RollbackJobPayload struct {
	DeploymentID     string `json:"deployment_id"`
	WebsiteID        string `json:"website_id"`
	TargetReleaseDir string `json:"target_release_dir"`
	TargetCommitSHA  string `json:"target_commit_sha,omitempty"`
}

// StagingJobPayload drives clone_staging (source=prod, target=staging) and
// promote_staging (source=staging, target=prod).
type StagingJobPayload struct {
	SourceWebsiteID string           `json:"source_website_id"`
	TargetWebsiteID string           `json:"target_website_id"`
	Databases       []DBClonePayload `json:"databases,omitempty"`
}

// BackupJobPayload matches the create_backup enqueue payload.
type BackupJobPayload struct {
	BackupID  string           `json:"backup_id"`
	WebsiteID string           `json:"website_id"`
	Databases []DBClonePayload `json:"databases,omitempty"`
}

// RestoreJobPayload matches the restore_backup enqueue payload.
type RestoreJobPayload struct {
	BackupID       string           `json:"backup_id"`
	WebsiteID      string           `json:"website_id"`
	Databases      []DBClonePayload `json:"databases,omitempty"`
	Runtime        string           `json:"runtime,omitempty"`
	RuntimeVersion string           `json:"runtime_version,omitempty"`
}

// DBClonePayload is the wire form of agent.DBClone.
type DBClonePayload struct {
	Engine     string `json:"engine"`
	SourceName string `json:"source_name"`
	TargetName string `json:"target_name"`
	User       string `json:"user"`
}

func toDBClones(list []DBClonePayload) []DBClone {
	out := make([]DBClone, 0, len(list))
	for _, d := range list {
		out = append(out, DBClone{Engine: d.Engine, SourceName: d.SourceName, TargetName: d.TargetName, User: d.User})
	}
	return out
}

// reloadFPMIfPHP refreshes fpm after content changes for php sites (opcache).
func (e *Executor) reloadFPMIfPHP(ctx context.Context, runtime, version string) {
	if runtime == "php" && version != "" {
		_ = e.run(ctx, "systemctl", "reload", "php"+version+"-fpm")
	}
}

// QuotaJobPayload matches the apply_quota enqueue payload.
type QuotaJobPayload struct {
	WebsiteID     string  `json:"website_id"`
	MaxDiskMB     int     `json:"max_disk_mb"`
	MemoryLimitMB int     `json:"memory_limit_mb"`
	CPUCores      float64 `json:"cpu_cores"`
}

// DatabaseToolsJobPayload is empty for now (server-local install).
type DatabaseToolsJobPayload struct{}

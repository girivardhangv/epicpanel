// Package software is the panel side of the Software Manager. Component
// state is detected live from the agent (no local cache to go stale);
// installs/removals run as background jobs because they can take minutes.
package software

import (
	"context"
	"errors"
	"log/slog"

	"github.com/epicbyte/epicpanel/backend/internal/agentclient"
	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/servers"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Actor identifies who triggered an operation.
type Actor struct {
	Label string
	IP    string
}

type Deps struct {
	Pool    *pgxpool.Pool
	Log     *slog.Logger
	Agent   *agentclient.Client
	Servers *servers.Service
	Jobs    *jobs.Store
	Audit   *audit.Service
}

type Service struct{ deps Deps }

func New(deps Deps) *Service { return &Service{deps} }

// List returns the live software inventory for a server.
func (s *Service) List(ctx context.Context, serverID string) (*agentclient.SoftwareListResult, error) {
	target, err := s.deps.Servers.OpsTarget(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if !target.Manageable {
		return nil, apierror.New(409, "SERVER_NOT_MANAGEABLE", "server has no management channel")
	}
	return s.deps.Agent.SoftwareList(ctx, target.AgentURL, target.OpsToken)
}

// Install queues an install job for a component on a server.
func (s *Service) Install(ctx context.Context, actor Actor, serverID, name string) (*jobs.Job, error) {
	return s.queue(ctx, actor, serverID, name, jobs.TypeInstallSoftware, "software.install_requested")
}

// Remove queues a removal job for a component on a server.
func (s *Service) Remove(ctx context.Context, actor Actor, serverID, name string) (*jobs.Job, error) {
	return s.queue(ctx, actor, serverID, name, jobs.TypeRemoveSoftware, "software.remove_requested")
}

func (s *Service) queue(ctx context.Context, actor Actor, serverID, name, jobType, action string) (*jobs.Job, error) {
	if name == "" {
		return nil, apierror.BadRequest("component name is required")
	}
	target, err := s.deps.Servers.OpsTarget(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if !target.Manageable {
		return nil, apierror.New(409, "SERVER_NOT_MANAGEABLE", "server has no management channel")
	}
	sid := serverID
	job, err := s.deps.Jobs.Create(ctx, jobType, &sid, nil, map[string]any{"name": name})
	if err != nil {
		return nil, err
	}
	s.audit(ctx, actor, action, serverID, map[string]any{"component": name, "job_id": job.ID})
	return job, nil
}

// ServiceControl starts/stops/restarts a component's service (quick op).
func (s *Service) ServiceControl(ctx context.Context, actor Actor, serverID, name, action string) error {
	target, err := s.deps.Servers.OpsTarget(ctx, serverID)
	if err != nil {
		return err
	}
	if !target.Manageable {
		return apierror.New(409, "SERVER_NOT_MANAGEABLE", "server has no management channel")
	}
	if err := s.deps.Agent.SoftwareService(ctx, target.AgentURL, target.OpsToken, name, action); err != nil {
		return err
	}
	s.audit(ctx, actor, "software.service_"+action, serverID, map[string]any{"component": name})
	return nil
}

// ---------------------------------------------------------------------------
// Job handlers
// ---------------------------------------------------------------------------

func (s *Service) RegisterHandlers(runner *jobs.Runner) {
	runner.Register(jobs.TypeInstallSoftware, s.handleInstall)
	runner.Register(jobs.TypeRemoveSoftware, s.handleRemove)
}

func (s *Service) handleInstall(ctx context.Context, job *jobs.Job, progress jobs.ProgressFunc) error {
	name, target, err := s.resolve(ctx, job)
	if err != nil {
		return err
	}
	progress(10, "Installing "+name)
	if err := s.deps.Agent.SoftwareInstall(ctx, target.AgentURL, target.OpsToken, name); err != nil {
		return err
	}
	progress(100, name + " installed")
	return nil
}

func (s *Service) handleRemove(ctx context.Context, job *jobs.Job, progress jobs.ProgressFunc) error {
	name, target, err := s.resolve(ctx, job)
	if err != nil {
		return err
	}
	progress(10, "Removing "+name)
	if err := s.deps.Agent.SoftwareRemove(ctx, target.AgentURL, target.OpsToken, name); err != nil {
		return err
	}
	progress(100, name + " removed")
	return nil
}

func (s *Service) resolve(ctx context.Context, job *jobs.Job) (string, *servers.OpsTarget, error) {
	name, _ := job.Payload["name"].(string)
	if name == "" {
		return "", nil, errors.New("software job missing component name")
	}
	if job.ServerID == nil {
		return "", nil, errors.New("software job missing server")
	}
	target, err := s.deps.Servers.OpsTarget(ctx, *job.ServerID)
	if err != nil {
		return "", nil, err
	}
	if !target.Manageable {
		return "", nil, errors.New("server has no management channel")
	}
	return name, target, nil
}

func (s *Service) audit(ctx context.Context, actor Actor, action, serverID string, meta map[string]any) {
	entry := audit.Entry{
		ActorType: "user", Label: actor.Label,
		Action: action, Resource: "server", ResourceID: serverID, Metadata: meta,
	}
	if actor.Label == "" {
		entry.ActorType = "system"
		entry.Label = "software manager"
	}
	s.deps.Audit.Log(ctx, entry)
}

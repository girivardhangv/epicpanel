// Package files implements the panel-side File Manager. All operations are
// proxied to the agent's constrained filesystem surface (paths must resolve
// inside the agent's sites root), so the panel never touches the host
// filesystem directly.
package files

import (
	"context"
	"encoding/base64"
	"path"
	"strings"

	"github.com/epicbyte/epicpanel/backend/internal/agentclient"
	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/servers"
	"github.com/epicbyte/epicpanel/backend/internal/websites"
)

const maxWriteBytes = 4 << 20 // 4 MiB — matches the agent's write cap

type Deps struct {
	Agent    *agentclient.Client
	Servers  *servers.Service
	Websites *websites.Service
	Audit    *audit.Service
}

type Service struct{ deps Deps }

func New(deps Deps) *Service { return &Service{deps} }

// Scope is a resolvable file-manager root: a website's document root.
type Scope struct {
	Path  string // agent-local absolute path (document root)
	Label string // human label for the browser title
}

// ScopeForWebsite resolves the file-manager scope for a website, verifying the
// server is manageable and deriving the agent-local document root.
func (s *Service) ScopeForWebsite(ctx context.Context, websiteID string) (*Scope, *servers.OpsTarget, error) {
	w, err := s.deps.Websites.Get(ctx, websiteID)
	if err != nil {
		return nil, nil, err
	}
	if w.DocumentRoot == "" {
		return nil, nil, apierror.BadRequest("website has no document root")
	}
	target, err := s.deps.Servers.OpsTarget(ctx, w.ServerID)
	if err != nil {
		return nil, nil, err
	}
	if !target.Manageable {
		return nil, nil, apierror.New(409, "SERVER_NOT_MANAGEABLE",
			"server has no management channel")
	}
	return &Scope{Path: w.DocumentRoot, Label: w.Domain}, target, nil
}

// List returns the entries of a directory inside the site scope.
func (s *Service) List(ctx context.Context, websiteID, dir string) (*agentclient.FSListResult, error) {
	scope, target, err := s.ScopeForWebsite(ctx, websiteID)
	if err != nil {
		return nil, err
	}
	full := join(scope.Path, dir)
	return s.deps.Agent.FSList(ctx, target.AgentURL, target.OpsToken, full)
}

// Read returns the (bounded) contents of a file inside the site scope.
func (s *Service) Read(ctx context.Context, websiteID, file string, maxBytes int64) (*agentclient.FSReadResult, error) {
	scope, target, err := s.ScopeForWebsite(ctx, websiteID)
	if err != nil {
		return nil, err
	}
	return s.deps.Agent.FSRead(ctx, target.AgentURL, target.OpsToken, join(scope.Path, file), maxBytes)
}

// Write stores a file inside the site scope (binary-safe via base64).
func (s *Service) Write(ctx context.Context, actor Actor, websiteID, file string, content []byte) error {
	if len(content) > maxWriteBytes {
		return apierror.BadRequest("file exceeds the 4 MiB upload limit")
	}
	scope, target, err := s.ScopeForWebsite(ctx, websiteID)
	if err != nil {
		return err
	}
	if err := s.deps.Agent.FSWrite(ctx, target.AgentURL, target.OpsToken,
		join(scope.Path, file), content); err != nil {
		return err
	}
	s.audit(ctx, actor, "files.write", websiteID, map[string]any{"path": file})
	return nil
}

// Mkdir creates a directory inside the site scope.
func (s *Service) Mkdir(ctx context.Context, actor Actor, websiteID, dir string) error {
	scope, target, err := s.ScopeForWebsite(ctx, websiteID)
	if err != nil {
		return err
	}
	if err := s.deps.Agent.FSMkdir(ctx, target.AgentURL, target.OpsToken,
		[]string{join(scope.Path, dir)}); err != nil {
		return err
	}
	s.audit(ctx, actor, "files.mkdir", websiteID, map[string]any{"path": dir})
	return nil
}

// Remove deletes a file or directory inside the site scope.
func (s *Service) Remove(ctx context.Context, actor Actor, websiteID, file string) error {
	scope, target, err := s.ScopeForWebsite(ctx, websiteID)
	if err != nil {
		return err
	}
	full := join(scope.Path, file)
	if full == scope.Path || full == "" {
		return apierror.BadRequest("refusing to remove the site root")
	}
	if err := s.deps.Agent.FSRemove(ctx, target.AgentURL, target.OpsToken, full); err != nil {
		return err
	}
	s.audit(ctx, actor, "files.remove", websiteID, map[string]any{"path": file})
	return nil
}

// Rename moves/renames a file or directory inside the site scope.
func (s *Service) Rename(ctx context.Context, actor Actor, websiteID, oldPath, newPath string) error {
	scope, target, err := s.ScopeForWebsite(ctx, websiteID)
	if err != nil {
		return err
	}
	if err := s.deps.Agent.FSRename(ctx, target.AgentURL, target.OpsToken,
		join(scope.Path, oldPath), join(scope.Path, newPath)); err != nil {
		return err
	}
	s.audit(ctx, actor, "files.rename", websiteID, map[string]any{"from": oldPath, "to": newPath})
	return nil
}

// DecodeContentBase64 decodes the upload payload (a JSON base64 string) into
// raw bytes with a size cap.
func DecodeContentBase64(encoded string) ([]byte, error) {
	if encoded == "" {
		return []byte{}, nil
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, apierror.BadRequest("content_base64 is not valid base64")
	}
	if len(raw) > maxWriteBytes {
		return nil, apierror.BadRequest("file exceeds the 4 MiB upload limit")
	}
	return raw, nil
}

// join combines the agent-local site root with a panel-supplied relative path,
// normalizing separators and preventing trivial traversal. The agent performs
// the authoritative escape check on its own sites root.
func join(root, rel string) string {
	if rel == "" || rel == "/" || rel == "." {
		return root
	}
	rel = strings.ReplaceAll(rel, "\\", "/")
	rel = strings.TrimPrefix(rel, "/")
	clean := path.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return root
	}
	return root + "/" + clean
}

// Actor identifies who triggered a file operation (for audit).
type Actor struct {
	Label string
	IP    string
}

func (s *Service) audit(ctx context.Context, actor Actor, action, websiteID string, meta map[string]any) {
	entry := audit.Entry{
		ActorType: "user", Label: actor.Label,
		Action: action, Resource: "website", ResourceID: websiteID,
		Metadata: meta,
	}
	if actor.Label == "" {
		entry.ActorType = "system"
		entry.Label = "file manager"
	}
	s.deps.Audit.Log(ctx, entry)
}

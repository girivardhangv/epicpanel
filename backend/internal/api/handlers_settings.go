package api

import (
	"net/http"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/httpx"
	"github.com/epicbyte/epicpanel/backend/internal/settings"
)

// Editable operator settings surfaced by the minimal settings API. Values
// are JSON-typed; the map is the source of truth for the UI.
var editableSettings = map[string]string{
	// Monitoring thresholds
	settings.KeyACMEMode:              "string",
	settings.KeyACMEEmail:             "string",
	settings.KeyACMEAutoRenewDays:     "int",
	settings.KeyACMEStagingDir:        "string",
	settings.KeyACMEProductionDir:     "string",
	settings.KeyServerOfflineMinutes:  "int",
	"monitoring.threshold_cpu_warn":   "int",
	"monitoring.threshold_cpu_crit":   "int",
	"monitoring.threshold_memory_warn": "int",
	"monitoring.threshold_memory_crit": "int",
	"monitoring.threshold_disk_warn":  "int",
	"monitoring.threshold_disk_crit":  "int",
	"monitoring.retention_raw_days":   "int",
	"monitoring.retention_hourly_days": "int",
	"monitoring.retention_daily_days": "int",
}

// settingsView reads the editable keys into a typed map.
func (s *Server) settingsView(r *http.Request) map[string]any {
	ctx := r.Context()
	out := map[string]any{}
	for key, typ := range editableSettings {
		switch typ {
		case "int":
			out[key] = s.deps.Settings.Int(ctx, key, 0, -1<<30, 1<<30)
		default:
			out[key] = s.deps.Settings.String(ctx, key, "")
		}
	}
	return out
}

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	JSON(w, r, http.StatusOK, map[string]any{"settings": s.settingsView(r)})
}

type updateSettingsRequest struct {
	Settings map[string]any `json:"settings"`
}

func (s *Server) handleSettingsPatch(w http.ResponseWriter, r *http.Request) {
	var req updateSettingsRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	if len(req.Settings) == 0 {
		Error(w, r, apierror.BadRequest("settings object is required"))
		return
	}
	ctx := r.Context()
	for key, value := range req.Settings {
		typ, ok := editableSettings[key]
		if !ok {
			Error(w, r, apierror.BadRequest("unknown setting key: "+key))
			return
		}
		switch typ {
		case "int":
			v, ok := value.(float64)
			if !ok {
				Error(w, r, apierror.BadRequest("setting "+key+" must be an integer"))
				return
			}
			if err := s.deps.Settings.Set(ctx, key, int(v)); err != nil {
				Error(w, r, err)
				return
			}
		default:
			str, ok := value.(string)
			if !ok {
				Error(w, r, apierror.BadRequest("setting "+key+" must be a string"))
				return
			}
			if err := s.deps.Settings.Set(ctx, key, str); err != nil {
				Error(w, r, err)
				return
			}
		}
	}
	if idt := auth.IdentityFrom(r.Context()); idt != nil {
		s.deps.Audit.Log(ctx, auditEntryForIdentity(idt, "settings.updated", "settings", "panel",
			httpx.ClientIP(r, s.cfg.Server.TrustedProxy), r.UserAgent()))
	}
	JSON(w, r, http.StatusOK, map[string]any{"settings": s.settingsView(r)})
}

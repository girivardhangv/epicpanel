package databases

import (
	"strings"
	"testing"

	"github.com/epicbyte/epicpanel/backend/internal/agentclient"
)

func TestValidateDBName(t *testing.T) {
	valid := []string{"app", "my_app", "wp_01", "a1", strings.Repeat("a", 63)}
	for _, v := range valid {
		if err := ValidateDBName(v); err != nil {
			t.Errorf("ValidateDBName(%q) unexpected: %v", v, err)
		}
	}
	invalid := []string{"", "1abc", "_abc", "Abc", "a-b", "a.b", "a b", "a;drop",
		"../../etc", strings.Repeat("a", 64), "a$b"}
	for _, v := range invalid {
		if err := ValidateDBName(v); err == nil {
			t.Errorf("ValidateDBName(%q) = nil, want error", v)
		}
	}
}

func TestValidateUserName(t *testing.T) {
	if err := ValidateUserName("appuser"); err != nil {
		t.Errorf("valid user rejected: %v", err)
	}
	for _, v := range []string{"", "1u", "U", "a-b", "a b", strings.Repeat("u", 33)} {
		if err := ValidateUserName(v); err == nil {
			t.Errorf("ValidateUserName(%q) = nil, want error", v)
		}
	}
}

func TestEngineAvailable(t *testing.T) {
	e := &agentclient.DBEnginesResult{
		MySQL:    agentclient.DBEngineStatus{Configured: true, Available: true},
		Postgres: agentclient.DBEngineStatus{Configured: false, Available: false},
	}
	if !engineAvailable(e, EngineMySQL) {
		t.Error("mysql should be available")
	}
	if engineAvailable(e, EnginePostgres) {
		t.Error("postgres should be unavailable")
	}
	if engineAvailable(nil, EngineMySQL) {
		t.Error("nil result must report unavailable")
	}
}

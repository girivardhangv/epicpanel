package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

const AgentVersion = "0.2.0" // 0.2.0: agentproto v1 stream
const envFilePath = "/etc/epicpanel/agent.env"

// EnvFilePath exposes the config location for CLI output.

// Enroll exchanges a one-time registration token for a persistent agent
// token, persists the connection settings to /etc/epicpanel/agent.env, and
// returns the agent token.
func Enroll(baseURL, registrationToken string) (agentToken, serverID string, err error) {
	host, _ := os.Hostname()
	osInfo := osReleasePretty()

	payload := map[string]string{
		"registration_token": registrationToken,
		"hostname":           host,
		"os_info":            osInfo,
		"agent_version":      AgentVersion,
	}
	body, _ := json.Marshal(payload)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(strings.TrimSuffix(baseURL, "/")+"/v1/agent/enroll", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("reach control plane: %w", err)
	}
	defer resp.Body.Close()

	var out struct {
		ServerID   string `json:"server_id"`
		AgentToken string `json:"agent_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", fmt.Errorf("decode enroll response: %w", err)
	}
	if resp.StatusCode != http.StatusCreated || out.AgentToken == "" {
		return "", "", fmt.Errorf("enroll failed (HTTP %d): check that the token is valid and unused", resp.StatusCode)
	}

	if err := persistEnv(baseURL, out.AgentToken); err != nil {
		return out.AgentToken, out.ServerID, fmt.Errorf("enrolled, but could not persist config: %w", err)
	}
	return out.AgentToken, out.ServerID, nil
}

// persistEnv writes the connection settings for future `epicpanel-agent run`
// invocations and systemd units.
func persistEnv(baseURL, agentToken string) error {
	if err := os.MkdirAll("/etc/epicpanel", 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf("EPICPANEL_CONTROL_PLANE_URL=%s\nEPICPANEL_AGENT_TOKEN=%s\n", baseURL, agentToken)
	return os.WriteFile(envFilePath, []byte(content), 0o600)
}

// LoadEnvFile reads key=value lines from the persisted agent env file.
func EnvFilePath() string { return envFilePath }

func LoadEnvFile() (baseURL, token string) {
	b, err := os.ReadFile(envFilePath)
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, "="); ok {
			switch k {
			case "EPICPANEL_CONTROL_PLANE_URL":
				baseURL = v
			case "EPICPANEL_AGENT_TOKEN":
				token = v
			}
		}
	}
	return
}

func osReleasePretty() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return runtime.GOOS + "/" + runtime.GOARCH
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
		}
	}
	return runtime.GOOS + "/" + runtime.GOARCH
}

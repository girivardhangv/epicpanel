// Backup seams for Phase 11: what a bot backup contains and what it must
// NEVER contain. The bot tree is plain files (safe to archive); the env blob
// is ciphertext-only — plaintext secrets have no file on disk except the
// 0600 EnvironmentFile, which is REGENERATED at every start and must be
// excluded from any downloadable artifact.
package discord

import "time"

// BackupArtifact describes one path in a bot backup.
type BackupArtifact struct {
	// Path relative to the bot root ("/" prefix = whole tree).
	Path string `json:"path"`
	// Exclude marks paths that MUST NOT be archived (secrets).
	Exclude bool `json:"exclude,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// BackupManifest is the bot-scoped backup contract Phase 11 consumes.
type BackupManifest struct {
	BotID      string           `json:"bot_id"`
	Kind       string           `json:"kind"` // "discord_bot"
	Includes   []BackupArtifact `json:"includes"`
	EnvBlobEnc string           `json:"-"` // ciphertext only, never serialized
	CreatedAt  time.Time        `json:"created_at"`
}

// BackupManifestFor builds the manifest for one bot row. The env ciphertext
// rides as a sealed blob (restore = re-seal into the new row); no plaintext
// secret is ever part of a backup.
func BackupManifestFor(row *BotRow) *BackupManifest {
	m := &BackupManifest{
		BotID:      row.Bot.ID.String(),
		Kind:       "discord_bot",
		EnvBlobEnc: row.EnvEnc,
		CreatedAt:  time.Now().UTC(),
	}
	m.Includes = []BackupArtifact{
		{Path: "/", Reason: "bot source files, data/ tree, package manifests"},
		{Path: "bot.env", Exclude: true, Reason: "0600 EnvironmentFile holds decrypted secrets; regenerated at start"},
		{Path: "venv/", Exclude: true, Reason: "rebuildable dependency tree (pip install re-runs)"},
		{Path: "node_modules/", Exclude: true, Reason: "rebuildable dependency tree (npm ci re-runs)"},
	}
	return m
}

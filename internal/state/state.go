package state

import "time"

const SchemaVersion = 1

// State is persisted to ~/.mutapod/state/<name>.json between invocations.
type State struct {
	SchemaVersion int    `json:"schema_version"`
	Name          string `json:"name"`
	ProviderType  string `json:"provider_type"`

	Instance     InstanceState      `json:"instance"`
	SSH          SSHState           `json:"ssh"`
	Sync         SyncState          `json:"sync"`
	Profiles     []ProfileSyncState `json:"profiles,omitempty"`
	DevContainer DevContainerState  `json:"devcontainer"`
	Idle         IdleState          `json:"idle"`

	UpdatedAt time.Time `json:"updated_at"`
}

type InstanceState struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	TargetScope       string    `json:"target_scope,omitempty"`
	ConfigFingerprint string    `json:"config_fingerprint,omitempty"`
	LastKnownIP       string    `json:"last_known_ip"`
	Status            string    `json:"status"` // last observed InstanceState string
	CreatedAt         time.Time `json:"created_at,omitempty"`
	LastStartedAt     time.Time `json:"last_started_at,omitempty"`
}

type SSHState struct {
	Host         string `json:"host"`
	Port         int    `json:"port"`
	User         string `json:"user"`
	IdentityFile string `json:"identity_file,omitempty"`
	ProxyCommand string `json:"proxy_command,omitempty"`
}

type SyncState struct {
	Backend         string    `json:"backend"` // "mutagen" | "oc_rsync"
	SessionName     string    `json:"session_name"`
	SessionID       string    `json:"session_id,omitempty"`
	LocalPath       string    `json:"local_path"`
	RemotePath      string    `json:"remote_path"`
	SessionConfig   string    `json:"session_config,omitempty"`
	IgnoreSignature string    `json:"ignore_signature,omitempty"`
	LastSyncAt      time.Time `json:"last_sync_at,omitempty"`
	// ForwardSessions holds mutagen forward session names keyed by port number.
	ForwardSessions map[string]string `json:"forward_sessions,omitempty"`
	// ReverseForwardSessions holds reverse Mutagen forward session names keyed by port number.
	ReverseForwardSessions map[string]string `json:"reverse_forward_sessions,omitempty"`
}

type ProfileSyncState struct {
	Name          string `json:"name"`
	SessionName   string `json:"session_name"`
	LocalPath     string `json:"local_path"`
	RemotePath    string `json:"remote_path"`
	MountPath     string `json:"mount_path,omitempty"`
	SessionConfig string `json:"session_config,omitempty"`
}

type DevContainerState struct {
	ContainerID     string `json:"container_id,omitempty"`
	RemoteUser      string `json:"remote_user,omitempty"`
	WorkspaceFolder string `json:"workspace_folder,omitempty"`
}

type IdleState struct {
	LastActivityAt time.Time `json:"last_activity_at,omitempty"`
}

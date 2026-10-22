package launcher

// PluginView is the frontend-safe description of one registered plugin.
type PluginView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Author      string `json:"author"`
	Description string `json:"description"`
	Path        string `json:"path"`
	UIPath      string `json:"uiPath,omitempty"`
	HasUI       bool   `json:"hasUI"`
	Enabled     bool   `json:"enabled"`
	Managed     bool   `json:"managed"`
	Missing     bool   `json:"missing"`
	Loaded      bool   `json:"loaded"`
}

// UpdateView describes the last update check.
type UpdateView struct {
	Checking       bool   `json:"checking"`
	Available      bool   `json:"available"`
	LatestVersion  string `json:"latestVersion,omitempty"`
	ReleaseURL     string `json:"releaseURL,omitempty"`
	DownloadedPath string `json:"downloadedPath,omitempty"`
	Message        string `json:"message,omitempty"`
}

// State is the complete snapshot sent to the frontend.
type State struct {
	Version        string       `json:"version"`
	AppDir         string       `json:"appDir"`
	ConfigPath     string       `json:"configPath"`
	PluginsDir     string       `json:"pluginsDir"`
	LogPath        string       `json:"logPath"`
	GamePath       string       `json:"gamePath"`
	GamePathValid  bool         `json:"gamePathValid"`
	BootstrapPath  string       `json:"bootstrapPath"`
	BootstrapValid bool         `json:"bootstrapValid"`
	AutoInject     bool         `json:"autoInject"`
	GameRunning    bool         `json:"gameRunning"`
	GamePID        uint32       `json:"gamePID,omitempty"`
	Injected       bool         `json:"injected"`
	Plugins        []PluginView `json:"plugins"`
	Update         UpdateView   `json:"update"`
	Status         string       `json:"status"`
}

// ImportItem is one outcome in an import report.
type ImportItem struct {
	Source  string `json:"source"`
	Action  string `json:"action"`
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	Message string `json:"message,omitempty"`
}

// ImportReport tells the frontend what happened and which paths need a
// confirmation before they can be retried with force=true.
type ImportReport struct {
	Paths        []string     `json:"paths"`
	Force        bool         `json:"force"`
	Items        []ImportItem `json:"items"`
	Message      string       `json:"message"`
	NeedsConfirm bool         `json:"needsConfirm"`
}

// Empty reports whether nothing was imported or examined.
func (r ImportReport) Empty() bool {
	return len(r.Items) == 0 && len(r.Paths) == 0
}

package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/buildinfo"
	log "github.com/sirupsen/logrus"
)

type UpdateInfo struct {
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version"`
	HasUpdate      bool   `json:"has_update"`
	DownloadURL    string `json:"download_url,omitempty"`
	ReleaseURL     string `json:"release_url,omitempty"`
	ReleaseNotes   string `json:"release_notes,omitempty"`
}

type githubRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
	PublishedAt time.Time `json:"published_at"`
}

// GetUpdateInfoHandler handles GET /v0/management/check-update requests to check for available updates.
// The owner parameter defaults to "13210541230" but can be overridden via CHECK_UPDATE_OWNER env.
// Response includes current version, latest version from GitHub, and download URL if an update is available.
func GetUpdateInfoHandler(c *gin.Context) {
	owner := os.Getenv("CHECK_UPDATE_OWNER")
	if owner == "" {
		owner = "13210541230"
	}
	repo := "CLIProxyAPI"
	releaseURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)

	updateInfo := UpdateInfo{
		CurrentVersion: buildinfo.Version,
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", releaseURL, nil)
	if err != nil {
		c.JSON(http.StatusOK, updateInfo)
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "CLIProxyAPI-UpdateChecker")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.WithError(err).Debug("failed to fetch latest release")
		c.JSON(http.StatusOK, updateInfo)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.WithField("status", resp.StatusCode).Debug("github api returned non-200")
		c.JSON(http.StatusOK, updateInfo)
		return
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		c.JSON(http.StatusOK, updateInfo)
		return
	}

	updateInfo.LatestVersion = release.TagName
	if strings.TrimPrefix(release.TagName, "v") != strings.TrimPrefix(buildinfo.Version, "v") {
		updateInfo.HasUpdate = true
	}
	updateInfo.ReleaseURL = release.HTMLURL
	updateInfo.ReleaseNotes = release.Body

	// Find appropriate asset based on OS and architecture
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	suffix := fmt.Sprintf("_%s_%s", goos, goarch)
	if goos == "windows" {
		suffix = ".exe"
	}

	for _, asset := range release.Assets {
		name := strings.ToLower(asset.Name)
		if strings.Contains(name, "cli-proxy-api") && strings.Contains(name, suffix) {
			updateInfo.DownloadURL = asset.BrowserDownloadURL
			break
		}
	}

	c.JSON(http.StatusOK, updateInfo)
}

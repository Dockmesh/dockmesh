package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// UpdatePreview is a non-destructive lookup of what "Update" would fetch:
// remote tag metadata plus any Changelog/Release-Notes we can dig up from
// Docker Hub + GitHub. Nothing on the host changes.
type UpdatePreview struct {
	Image             string         `json:"image"`
	CurrentDigest     string         `json:"current_digest,omitempty"`
	CurrentCreated    *time.Time     `json:"current_created,omitempty"`
	RemoteLastUpdated *time.Time     `json:"remote_last_updated,omitempty"`
	RemoteSize        int64          `json:"remote_size,omitempty"`
	DockerHubURL      string         `json:"docker_hub_url,omitempty"`
	GitHubURL         string         `json:"github_url,omitempty"`
	LatestRelease     *GitHubRelease `json:"latest_release,omitempty"`
	Warnings          []string       `json:"warnings,omitempty"`
}

type GitHubRelease struct {
	Tag       string     `json:"tag"`
	Name      string     `json:"name"`
	URL       string     `json:"url"`
	Body      string     `json:"body"`
	Published *time.Time `json:"published,omitempty"`
}

// Preview collects the information we can fetch without changing anything
// on disk. All network calls are best-effort: failures are logged in
// Warnings and the caller still gets partial data.
func (s *Service) Preview(ctx context.Context, containerID string) (*UpdatePreview, error) {
	if s.docker == nil {
		return nil, ErrDockerUnavailable
	}
	cli := s.docker.Raw()
	info, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("inspect: %w", err)
	}
	ref := normalizeImageRef(info.Config.Image)

	preview := &UpdatePreview{Image: ref}

	// Local image metadata.
	if img, _, err := cli.ImageInspectWithRaw(ctx, info.Image); err == nil {
		if len(img.RepoDigests) > 0 {
			preview.CurrentDigest = img.RepoDigests[0]
		} else {
			preview.CurrentDigest = img.ID
		}
		if t, perr := time.Parse(time.RFC3339Nano, img.Created); perr == nil {
			preview.CurrentCreated = &t
		}
	}

	// Only Docker Hub is understood — other registries fall through.
	namespace, repo, tag, ok := parseDockerHubRef(ref)
	if !ok {
		preview.Warnings = append(preview.Warnings, "registry not recognized (only docker.io is supported)")
		return preview, nil
	}

	client := &http.Client{Timeout: 8 * time.Second}

	// Tag metadata.
	if tagMeta, err := fetchDockerHubTag(ctx, client, namespace, repo, tag); err != nil {
		preview.Warnings = append(preview.Warnings, "docker hub tag lookup failed: "+err.Error())
	} else if tagMeta != nil {
		preview.RemoteLastUpdated = tagMeta.LastUpdated
		preview.RemoteSize = tagMeta.FullSize
	}

	// Public Docker Hub URL.
	if namespace == "library" {
		preview.DockerHubURL = "https://hub.docker.com/_/" + repo
	} else {
		preview.DockerHubURL = "https://hub.docker.com/r/" + namespace + "/" + repo
	}

	// Repository description → try to extract a GitHub URL for releases.
	if desc, err := fetchDockerHubDescription(ctx, client, namespace, repo); err == nil && desc != "" {
		if owner, repoName, ok := extractGitHubRepo(desc); ok {
			preview.GitHubURL = "https://github.com/" + owner + "/" + repoName
			if rel, err := fetchGitHubLatestRelease(ctx, client, owner, repoName); err != nil {
				preview.Warnings = append(preview.Warnings, "github releases lookup failed: "+err.Error())
			} else if rel != nil {
				preview.LatestRelease = rel
			}
		}
	}

	return preview, nil
}

// -----------------------------------------------------------------------------
// Docker Hub
// -----------------------------------------------------------------------------

type dockerHubTag struct {
	LastUpdated *time.Time `json:"last_updated"`
	FullSize    int64      `json:"full_size"`
}

func fetchDockerHubTag(ctx context.Context, client *http.Client, namespace, repo, tag string) (*dockerHubTag, error) {
	url := fmt.Sprintf("https://hub.docker.com/v2/repositories/%s/%s/tags/%s/", namespace, repo, tag)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	var t dockerHubTag
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, err
	}
	return &t, nil
}

func fetchDockerHubDescription(ctx context.Context, client *http.Client, namespace, repo string) (string, error) {
	url := fmt.Sprintf("https://hub.docker.com/v2/repositories/%s/%s/", namespace, repo)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return "", err
	}
	var r struct {
		FullDescription string `json:"full_description"`
		Description     string `json:"description"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return "", err
	}
	if r.FullDescription != "" {
		return r.FullDescription, nil
	}
	return r.Description, nil
}

// parseDockerHubRef splits an image ref like "nginx:alpine" or
// "bitnami/postgres:16" into (namespace, repo, tag). Returns false for
// refs that belong to other registries (ghcr.io, quay.io, etc.).
func parseDockerHubRef(ref string) (namespace, repo, tag string, ok bool) {
	// Split off the tag.
	lastColon := strings.LastIndex(ref, ":")
	lastSlash := strings.LastIndex(ref, "/")
	if lastColon < 0 || lastColon < lastSlash {
		tag = "latest"
	} else {
		tag = ref[lastColon+1:]
		ref = ref[:lastColon]
	}
	// If the first path segment contains a dot or colon it's a registry host.
	if i := strings.Index(ref, "/"); i >= 0 {
		host := ref[:i]
		if strings.ContainsAny(host, ".:") {
			return "", "", "", false
		}
		namespace = ref[:i]
		repo = ref[i+1:]
		return namespace, repo, tag, true
	}
	// No slash → official library image.
	return "library", ref, tag, true
}

// -----------------------------------------------------------------------------
// GitHub
// -----------------------------------------------------------------------------

// gitHubRepoRe finds GitHub owner/repo strings in arbitrary text. It only
// accepts the canonical shape so things like "my-github-token" don't match.
var gitHubRepoRe = regexp.MustCompile(`github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)`)

func extractGitHubRepo(desc string) (owner, repo string, ok bool) {
	m := gitHubRepoRe.FindStringSubmatch(desc)
	if len(m) != 3 {
		return "", "", false
	}
	// Trim anything that's clearly not part of the repo name.
	repo = strings.TrimSuffix(m[2], ".git")
	repo = strings.TrimRight(repo, ").,;:!?")
	return m[1], repo, true
}

func fetchGitHubLatestRelease(ctx context.Context, client *http.Client, owner, repo string) (*GitHubRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, nil
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	var raw struct {
		TagName     string    `json:"tag_name"`
		Name        string    `json:"name"`
		HTMLURL     string    `json:"html_url"`
		Body        string    `json:"body"`
		PublishedAt time.Time `json:"published_at"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&raw); err != nil {
		return nil, err
	}
	// Cap body length — release notes can be huge.
	if len(raw.Body) > 20000 {
		raw.Body = raw.Body[:20000] + "\n\n…(truncated)"
	}
	return &GitHubRelease{
		Tag:       raw.TagName,
		Name:      raw.Name,
		URL:       raw.HTMLURL,
		Body:      raw.Body,
		Published: &raw.PublishedAt,
	}, nil
}

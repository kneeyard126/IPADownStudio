package ipatool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ipadownstudio/internal/syswin"
)

// BinaryInfo describes the discovered ipatool engine and its runtime dependencies.
type BinaryInfo struct {
	Found          bool   `json:"ipatoolFound"`
	BinaryPath     string `json:"ipatoolPath"`
	Version        string `json:"ipatoolVersion"`
	UnicornFound   bool   `json:"unicornFound"`
	UnicornPath    string `json:"unicornPath"`
	CoreFPReady    bool   `json:"coreFpReady"`
	CoreFPCacheDir string `json:"coreFpCacheDir"`
	UnicornCached  bool   `json:"unicornCached"`
}

// DownloadOpts configures an `ipatool download` subprocess.
type DownloadOpts struct {
	AppID             int64
	BundleID          string
	ExternalVersionID string
	Platform          string
	OutputPath        string
	Purchase          bool
}

// Client wraps invocations of the ipatool CLI with per-profile state isolation.
type Client struct {
	mu         sync.RWMutex
	customPath string
}

// NewClient creates a new ipatool client.
func NewClient(customPath string) *Client {
	return &Client{customPath: strings.TrimSpace(customPath)}
}

// SetCustomPath updates the user-configured override path to ipatool.
func (c *Client) SetCustomPath(customPath string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.customPath = strings.TrimSpace(customPath)
}

// Info inspects the resolved ipatool binary, libunicorn.dll, and Apple CoreFP cache readiness.
func (c *Client) Info() BinaryInfo {
	c.mu.RLock()
	custom := c.customPath
	c.mu.RUnlock()
	return ResolveBinary(custom)
}

// ResolveBinary locates ipatool.exe according to priority order and probes its version & runtime assets.
func ResolveBinary(customPath string) BinaryInfo {
	var candidates []string
	if strings.TrimSpace(customPath) != "" {
		candidates = append(candidates, customPath)
	}
	if envPath := strings.TrimSpace(os.Getenv("IPATOOL_PATH")); envPath != "" {
		candidates = append(candidates, envPath)
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, "Tools", "ipatool.exe"),
			filepath.Join(exeDir, "..", "current", "Tools", "ipatool.exe"),
			filepath.Join(exeDir, "..", "..", "current", "Tools", "ipatool.exe"),
			filepath.Join(exeDir, "ipatool.exe"),
		)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(cwd, "Tools", "ipatool.exe"),
			filepath.Join(cwd, "current", "Tools", "ipatool.exe"),
			filepath.Join(cwd, "..", "current", "Tools", "ipatool.exe"),
			filepath.Join(cwd, "ipatool.exe"),
		)
	}
	if found, err := exec.LookPath("ipatool"); err == nil {
		candidates = append(candidates, found)
	}

	info := BinaryInfo{}
	for _, cand := range candidates {
		abs, err := filepath.Abs(cand)
		if err != nil {
			continue
		}
		st, err := os.Stat(abs)
		if err != nil || st.IsDir() {
			continue
		}
		info.Found = true
		info.BinaryPath = abs
		break
	}

	if info.Found {
		unicorn := filepath.Join(filepath.Dir(info.BinaryPath), "libunicorn.dll")
		if st, err := os.Stat(unicorn); err == nil && !st.IsDir() {
			info.UnicornFound = true
			info.UnicornPath = unicorn
		}
		info.Version = probeVersion(info.BinaryPath)
	}

	if cacheRoot, err := os.UserCacheDir(); err == nil {
		ipCache := filepath.Join(cacheRoot, "ipatool")
		info.CoreFPCacheDir = ipCache
		info.CoreFPReady, info.UnicornCached = inspectCache(ipCache)
	}
	return info
}

func probeVersion(binPath string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, "--version")
	cmd.Dir = filepath.Dir(binPath)
	cmd.SysProcAttr = syswin.HideWindowAttr()
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return ""
	}
	ver := strings.TrimSpace(string(out))
	ver = strings.TrimPrefix(ver, "ipatool version ")
	return strings.TrimSpace(ver)
}

func inspectCache(cacheRoot string) (coreFPReady bool, unicornCached bool) {
	if _, err := os.Stat(cacheRoot); err != nil {
		return false, false
	}
	_ = filepath.WalkDir(cacheRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := strings.ToLower(d.Name())
		if name == "corefp" || name == "corefp.icxs" {
			coreFPReady = true
		}
		if name == "unicorn" && d.IsDir() {
			unicornCached = true
		}
		return nil
	})
	return coreFPReady, unicornCached
}

func (c *Client) resolveBinPath() (string, error) {
	info := c.Info()
	if !info.Found {
		return "", &EngineError{Kind: KindBinaryNotFound, Message: "ipatool.exe not found; configure path in Settings", Cause: ErrBinaryNotFound}
	}
	return info.BinaryPath, nil
}

func buildIsolatedEnv(profileID, absStateDir string) []string {
	base := os.Environ()
	env := make([]string, 0, len(base)+2)
	for _, entry := range base {
		upper := strings.ToUpper(entry)
		if strings.HasPrefix(upper, "IPATOOL_STATE_DIR=") || strings.HasPrefix(upper, "IPATOOL_PROFILE_ID=") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env,
		"IPATOOL_PROFILE_ID="+profileID,
		"IPATOOL_STATE_DIR="+absStateDir,
	)
	return env
}

func (c *Client) runJSON(ctx context.Context, profileID, stateDir string, stdinData []byte, args ...string) (string, error) {
	binPath, err := c.resolveBinPath()
	if err != nil {
		return "", err
	}
	absState, err := filepath.Abs(stateDir)
	if err != nil {
		return "", fmt.Errorf("resolve state dir: %w", err)
	}
	if err := os.MkdirAll(absState, 0o755); err != nil {
		return "", fmt.Errorf("create state dir: %w", err)
	}

	fullArgs := append([]string{}, args...)
	fullArgs = append(fullArgs, "--format", "json", "--non-interactive")

	cmd := exec.CommandContext(ctx, binPath, fullArgs...)
	cmd.Dir = filepath.Dir(binPath)
	cmd.SysProcAttr = syswin.HideWindowAttr()
	cmd.Env = buildIsolatedEnv(profileID, absState)
	if stdinData != nil {
		cmd.Stdin = bytes.NewReader(stdinData)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	combined := stdout.String()
	if stderr.Len() > 0 {
		if combined != "" && !strings.HasSuffix(combined, "\n") {
			combined += "\n"
		}
		combined += stderr.String()
	}
	return combined, runErr
}

// AuthLogin authenticates an Apple ID via --credentials-stdin so secrets never appear on command lines.
func (c *Client) AuthLogin(ctx context.Context, profileID, stateDir, email, password, authCode string) (*AuthResult, error) {
	payload, err := json.Marshal(struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		AuthCode string `json:"authCode"`
	}{
		Email:    email,
		Password: password,
		AuthCode: authCode,
	})
	if err != nil {
		return nil, err
	}
	payload = append(payload, '\n')

	raw, runErr := c.runJSON(ctx, profileID, stateDir, payload, "auth", "login", "--credentials-stdin")
	res, parseErr := ParseAuthOutput(raw)
	if parseErr == nil {
		return res, nil
	}
	if runErr != nil {
		objs, plain := ParseNDJSONLines(raw)
		if extracted := ExtractError(objs, plain, runErr); extracted != nil {
			return nil, extracted
		}
	}
	return nil, parseErr
}

// AuthInfo verifies whether the isolated profile holds an active session.
func (c *Client) AuthInfo(ctx context.Context, profileID, stateDir string) (*AuthResult, error) {
	raw, runErr := c.runJSON(ctx, profileID, stateDir, nil, "auth", "info")
	res, parseErr := ParseAuthOutput(raw)
	if parseErr == nil {
		return res, nil
	}
	if runErr != nil {
		objs, plain := ParseNDJSONLines(raw)
		if extracted := ExtractError(objs, plain, runErr); extracted != nil {
			return nil, extracted
		}
	}
	return nil, parseErr
}

// AuthRevoke clears credentials for the isolated profile.
func (c *Client) AuthRevoke(ctx context.Context, profileID, stateDir string) error {
	raw, runErr := c.runJSON(ctx, profileID, stateDir, nil, "auth", "revoke")
	if runErr != nil {
		objs, plain := ParseNDJSONLines(raw)
		if extracted := ExtractError(objs, plain, runErr); extracted != nil {
			return extracted
		}
		return runErr
	}
	return nil
}

func appendTargetFlags(args []string, appID int64, bundleID, platform string) []string {
	if appID > 0 {
		args = append(args, "-i", fmt.Sprintf("%d", appID))
	} else if strings.TrimSpace(bundleID) != "" {
		args = append(args, "-b", strings.TrimSpace(bundleID))
	}
	if strings.TrimSpace(platform) != "" {
		args = append(args, "--platform", strings.TrimSpace(platform))
	}
	return args
}

// ListVersions retrieves all external version identifiers for an app.
func (c *Client) ListVersions(ctx context.Context, profileID, stateDir string, appID int64, bundleID, platform string) (*VersionListResult, error) {
	args := appendTargetFlags([]string{"list-versions"}, appID, bundleID, platform)
	raw, runErr := c.runJSON(ctx, profileID, stateDir, nil, args...)
	res, parseErr := ParseListVersionsOutput(raw)
	if parseErr == nil {
		return res, nil
	}
	if runErr != nil {
		objs, plain := ParseNDJSONLines(raw)
		if extracted := ExtractError(objs, plain, runErr); extracted != nil {
			return nil, extracted
		}
	}
	return nil, parseErr
}

// GetVersionMetadata fetches display version, size, release date, and min OS for one external version ID.
func (c *Client) GetVersionMetadata(ctx context.Context, profileID, stateDir string, appID int64, bundleID, externalVersionID, platform string) (*VersionMetadataResult, error) {
	args := appendTargetFlags([]string{"get-version-metadata"}, appID, bundleID, platform)
	args = append(args, "--external-version-id", strings.TrimSpace(externalVersionID))
	raw, runErr := c.runJSON(ctx, profileID, stateDir, nil, args...)
	res, parseErr := ParseVersionMetadataOutput(raw)
	if parseErr == nil {
		if res.ExternalVersionID == "" {
			res.ExternalVersionID = externalVersionID
		}
		return res, nil
	}
	if runErr != nil {
		objs, plain := ParseNDJSONLines(raw)
		if extracted := ExtractError(objs, plain, runErr); extracted != nil {
			return nil, extracted
		}
	}
	return nil, parseErr
}

// ListPurchases lists owned apps for the active Apple ID.
func (c *Client) ListPurchases(ctx context.Context, profileID, stateDir string, page, maxResults int, platform string) (*PurchasesResult, error) {
	if page < 1 {
		page = 1
	}
	if maxResults < 1 {
		maxResults = 20
	}
	args := []string{"list-purchases", "--page", fmt.Sprintf("%d", page), "--max-results", fmt.Sprintf("%d", maxResults)}
	if strings.TrimSpace(platform) != "" {
		args = append(args, "--platform", strings.TrimSpace(platform))
	}
	raw, runErr := c.runJSON(ctx, profileID, stateDir, nil, args...)
	res, parseErr := ParsePurchasesOutput(raw)
	if parseErr == nil {
		return res, nil
	}
	if runErr != nil {
		objs, plain := ParseNDJSONLines(raw)
		if extracted := ExtractError(objs, plain, runErr); extracted != nil {
			return nil, extracted
		}
	}
	return nil, parseErr
}

// Purchase obtains a free App Store license for an app.
func (c *Client) Purchase(ctx context.Context, profileID, stateDir string, appID int64, bundleID, platform string) (*PurchaseResult, error) {
	args := appendTargetFlags([]string{"purchase"}, appID, bundleID, platform)
	raw, runErr := c.runJSON(ctx, profileID, stateDir, nil, args...)
	res, parseErr := ParsePurchaseOutput(raw)
	if parseErr == nil {
		return res, nil
	}
	if runErr != nil {
		objs, plain := ParseNDJSONLines(raw)
		if extracted := ExtractError(objs, plain, runErr); extracted != nil {
			return nil, extracted
		}
	}
	return nil, parseErr
}

// StartDownload spawns `ipatool download` asynchronously and returns the running Cmd plus stdout/stderr pipes.
func (c *Client) StartDownload(ctx context.Context, profileID, stateDir string, opts DownloadOpts) (*exec.Cmd, io.ReadCloser, io.ReadCloser, error) {
	binPath, err := c.resolveBinPath()
	if err != nil {
		return nil, nil, nil, err
	}
	absState, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve state dir: %w", err)
	}
	if err := os.MkdirAll(absState, 0o755); err != nil {
		return nil, nil, nil, fmt.Errorf("create state dir: %w", err)
	}
	absOut, err := filepath.Abs(opts.OutputPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve output path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absOut), 0o755); err != nil {
		return nil, nil, nil, fmt.Errorf("create output dir: %w", err)
	}

	args := appendTargetFlags([]string{"download"}, opts.AppID, opts.BundleID, opts.Platform)
	if ext := strings.TrimSpace(opts.ExternalVersionID); ext != "" && !strings.EqualFold(ext, "latest") {
		args = append(args, "--external-version-id", ext)
	}
	if opts.Purchase {
		args = append(args, "--purchase")
	}
	args = append(args, "-o", absOut, "--format", "json", "--non-interactive")

	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Dir = filepath.Dir(binPath)
	cmd.SysProcAttr = syswin.HideWindowAttr()
	cmd.Env = buildIsolatedEnv(profileID, absState)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, fmt.Errorf("start ipatool download: %w", err)
	}
	return cmd, stdout, stderr, nil
}

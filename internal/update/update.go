package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRepository = "opentokenz/mcpx"
	defaultAPIBaseURL = "https://api.github.com"
	maxReleaseBytes   = 2 << 20
	maxChecksumBytes  = 4 << 20
	maxArchiveBytes   = 128 << 20
	maxBinaryBytes    = 128 << 20
)

// Options controls one self-update attempt.
type Options struct {
	CurrentVersion string
	TargetVersion  string
	CheckOnly      bool
	Repository     string
	APIBaseURL     string
	HTTPClient     *http.Client
	Token          string
	GOOS           string
	GOARCH         string
	ExecutablePath string
	Progress       func(string)
	Install        func(currentPath, newPath string) error
	VerifyBinary   func(path, version string) error
}

// Result describes the selected release and whether it was installed.
type Result struct {
	CurrentVersion string
	TargetVersion  string
	AssetName      string
	InstalledPath  string
	UpToDate       bool
	CheckedOnly    bool
}

type githubRelease struct {
	TagName string        `json:"tag_name"`
	Draft   bool          `json:"draft"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Run checks GitHub Releases and, unless CheckOnly is set, installs the selected release.
func Run(ctx context.Context, options Options) (Result, error) {
	options = withDefaults(options)
	current := normalizeVersion(options.CurrentVersion)
	release, err := fetchRelease(ctx, options)
	if err != nil {
		return Result{}, err
	}
	if release.Draft {
		return Result{}, fmt.Errorf("refusing draft release %q", release.TagName)
	}
	target := normalizeVersion(release.TagName)
	if target == "" {
		return Result{}, fmt.Errorf("GitHub release has no version tag")
	}
	if requested := normalizeVersion(options.TargetVersion); requested != "" && requested != target {
		return Result{}, fmt.Errorf("requested version %s resolved to release %s", requested, target)
	}

	result := Result{CurrentVersion: current, TargetVersion: target, CheckedOnly: options.CheckOnly}
	if compare, compareErr := compareVersions(current, target); compareErr == nil {
		if compare == 0 {
			result.UpToDate = true
			return result, nil
		}
		if compare > 0 {
			return Result{}, fmt.Errorf("current version %s is newer than requested release %s", current, target)
		}
	} else if current == target {
		result.UpToDate = true
		return result, nil
	}
	if options.CheckOnly {
		return result, nil
	}

	assetName := releaseAssetName(target, options.GOOS, options.GOARCH)
	archiveAsset, ok := findAsset(release.Assets, assetName)
	if !ok {
		return Result{}, fmt.Errorf("release %s has no asset for %s/%s (%s)", target, options.GOOS, options.GOARCH, assetName)
	}
	checksumAsset, ok := findAsset(release.Assets, "checksums.txt")
	if !ok {
		return Result{}, fmt.Errorf("release %s is missing checksums.txt", target)
	}
	result.AssetName = assetName
	progress(options, fmt.Sprintf("Updating mcpx %s → %s", displayVersion(current), target))
	progress(options, "Downloading "+assetName)

	checksums, err := download(ctx, options, checksumAsset.BrowserDownloadURL, maxChecksumBytes)
	if err != nil {
		return Result{}, fmt.Errorf("download checksums.txt: %w", err)
	}
	archive, err := download(ctx, options, archiveAsset.BrowserDownloadURL, maxArchiveBytes)
	if err != nil {
		return Result{}, fmt.Errorf("download %s: %w", assetName, err)
	}
	if err := verifyChecksum(assetName, archive, checksums); err != nil {
		return Result{}, err
	}
	progress(options, "Verified SHA-256")

	tempDir, err := os.MkdirTemp("", "mcpx-update-*")
	if err != nil {
		return Result{}, fmt.Errorf("create update temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)
	binaryPath, err := extractBinary(assetName, archive, tempDir, options.GOOS)
	if err != nil {
		return Result{}, err
	}
	if err := options.VerifyBinary(binaryPath, target); err != nil {
		return Result{}, fmt.Errorf("verify downloaded mcpx: %w", err)
	}
	currentPath := options.ExecutablePath
	if currentPath == "" {
		currentPath, err = os.Executable()
		if err != nil {
			return Result{}, fmt.Errorf("resolve current executable: %w", err)
		}
		if resolved, resolveErr := filepath.EvalSymlinks(currentPath); resolveErr == nil {
			currentPath = resolved
		}
	}
	if err := options.Install(currentPath, binaryPath); err != nil {
		return Result{}, fmt.Errorf("install update at %s: %w", currentPath, err)
	}
	result.InstalledPath = currentPath
	return result, nil
}

func withDefaults(options Options) Options {
	if strings.TrimSpace(options.Repository) == "" {
		options.Repository = defaultRepository
	}
	if strings.TrimSpace(options.APIBaseURL) == "" {
		options.APIBaseURL = defaultAPIBaseURL
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: 90 * time.Second}
	}
	if options.Token == "" {
		options.Token = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	}
	if options.GOOS == "" {
		options.GOOS = runtime.GOOS
	}
	if options.GOARCH == "" {
		options.GOARCH = runtime.GOARCH
	}
	if options.Install == nil {
		options.Install = replaceExecutable
	}
	if options.VerifyBinary == nil {
		options.VerifyBinary = verifyExecutable
	}
	return options
}

func fetchRelease(ctx context.Context, options Options) (githubRelease, error) {
	base := strings.TrimRight(options.APIBaseURL, "/") + "/repos/" + strings.Trim(options.Repository, "/") + "/releases/"
	endpoint := base + "latest"
	if target := normalizeVersion(options.TargetVersion); target != "" {
		endpoint = base + "tags/" + url.PathEscape("v"+target)
	}
	body, err := requestBytes(ctx, options, endpoint, maxReleaseBytes)
	if err != nil {
		return githubRelease{}, fmt.Errorf("query GitHub release: %w", err)
	}
	var release githubRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return githubRelease{}, fmt.Errorf("decode GitHub release: %w", err)
	}
	return release, nil
}

func download(ctx context.Context, options Options, downloadURL string, limit int64) ([]byte, error) {
	if strings.TrimSpace(downloadURL) == "" {
		return nil, fmt.Errorf("release asset has no download URL")
	}
	return requestBytes(ctx, options, downloadURL, limit)
}

func requestBytes(ctx context.Context, options Options, requestURL string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "mcpx-update/"+displayVersion(options.CurrentVersion))
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if options.Token != "" {
		request.Header.Set("Authorization", "Bearer "+options.Token)
	}
	response, err := options.HTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
		return nil, fmt.Errorf("HTTP %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return body, nil
}

func releaseAssetName(version, goos, goarch string) string {
	extension := "tar.gz"
	if goos == "windows" {
		extension = "zip"
	}
	return fmt.Sprintf("mcpx_%s_%s_%s.%s", normalizeVersion(version), goos, goarch, extension)
}

func findAsset(assets []githubAsset, name string) (githubAsset, bool) {
	for _, asset := range assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return githubAsset{}, false
}

func verifyChecksum(assetName string, archive, checksums []byte) error {
	wanted := ""
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if name == assetName {
			wanted = strings.ToLower(fields[0])
			break
		}
	}
	if wanted == "" {
		return fmt.Errorf("checksums.txt has no entry for %s", assetName)
	}
	if _, err := hex.DecodeString(wanted); err != nil || len(wanted) != sha256.Size*2 {
		return fmt.Errorf("invalid SHA-256 for %s in checksums.txt", assetName)
	}
	sum := sha256.Sum256(archive)
	actual := hex.EncodeToString(sum[:])
	if actual != wanted {
		return fmt.Errorf("SHA-256 mismatch for %s: expected %s, got %s", assetName, wanted, actual)
	}
	return nil
}

func extractBinary(assetName string, archive []byte, tempDir, goos string) (string, error) {
	binaryName := "mcpx"
	if goos == "windows" {
		binaryName += ".exe"
	}
	destination := filepath.Join(tempDir, binaryName)
	switch {
	case strings.HasSuffix(assetName, ".tar.gz"):
		if err := extractTarGzipBinary(archive, binaryName, destination); err != nil {
			return "", err
		}
	case strings.HasSuffix(assetName, ".zip"):
		if err := extractZipBinary(archive, binaryName, destination); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("unsupported release archive %s", assetName)
	}
	if err := os.Chmod(destination, 0o755); err != nil {
		return "", fmt.Errorf("make downloaded binary executable: %w", err)
	}
	return destination, nil
}

func extractTarGzipBinary(archive []byte, binaryName, destination string) error {
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("open tar.gz release asset: %w", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar.gz release asset: %w", err)
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(filepath.Clean(header.Name)) != binaryName {
			continue
		}
		if header.Size < 0 || header.Size > maxBinaryBytes {
			return fmt.Errorf("binary in release archive has invalid size %d", header.Size)
		}
		return writeExtractedBinary(destination, tarReader, header.Size)
	}
	return fmt.Errorf("release archive does not contain %s", binaryName)
}

func extractZipBinary(archive []byte, binaryName, destination string) error {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return fmt.Errorf("open zip release asset: %w", err)
	}
	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() || filepath.Base(filepath.Clean(entry.Name)) != binaryName {
			continue
		}
		if entry.UncompressedSize64 > maxBinaryBytes {
			return fmt.Errorf("binary in release archive exceeds %d bytes", maxBinaryBytes)
		}
		input, err := entry.Open()
		if err != nil {
			return fmt.Errorf("open %s in zip: %w", binaryName, err)
		}
		err = writeExtractedBinary(destination, input, int64(entry.UncompressedSize64))
		input.Close()
		return err
	}
	return fmt.Errorf("release archive does not contain %s", binaryName)
}

func writeExtractedBinary(destination string, source io.Reader, size int64) error {
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		return fmt.Errorf("create extracted binary: %w", err)
	}
	defer output.Close()
	written, err := io.Copy(output, io.LimitReader(source, maxBinaryBytes+1))
	if err != nil {
		return fmt.Errorf("extract binary: %w", err)
	}
	if written > maxBinaryBytes || (size >= 0 && written != size) {
		return fmt.Errorf("extracted binary size mismatch: expected %d, got %d", size, written)
	}
	return output.Sync()
}

func verifyExecutable(path, version string) error {
	command := exec.Command(path, "-version")
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s -version failed: %w: %s", path, err, strings.TrimSpace(string(output)))
	}
	if !strings.Contains(string(output), "mcpx "+normalizeVersion(version)) {
		return fmt.Errorf("unexpected version output: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func replaceExecutable(currentPath, newPath string) error {
	info, err := os.Stat(currentPath)
	if err != nil {
		return err
	}
	currentPath = filepath.Clean(currentPath)
	staged, err := os.CreateTemp(filepath.Dir(currentPath), ".mcpx-update-*")
	if err != nil {
		return fmt.Errorf("create staged executable beside current binary: %w", err)
	}
	stagedPath := staged.Name()
	defer os.Remove(stagedPath)
	input, err := os.Open(newPath)
	if err != nil {
		staged.Close()
		return err
	}
	_, copyErr := io.Copy(staged, input)
	closeInputErr := input.Close()
	if copyErr == nil {
		copyErr = closeInputErr
	}
	if copyErr == nil {
		copyErr = staged.Chmod(info.Mode().Perm())
	}
	if copyErr == nil {
		copyErr = staged.Sync()
	}
	closeErr := staged.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return fmt.Errorf("stage updated executable: %w", copyErr)
	}
	if err := os.Rename(stagedPath, currentPath); err != nil {
		return fmt.Errorf("replace running executable: %w; ensure %s is writable", err, currentPath)
	}
	if directory, openErr := os.Open(filepath.Dir(currentPath)); openErr == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

type semVersion struct {
	major int
	minor int
	patch int
	pre   []string
}

func compareVersions(left, right string) (int, error) {
	leftVersion, err := parseVersion(left)
	if err != nil {
		return 0, err
	}
	rightVersion, err := parseVersion(right)
	if err != nil {
		return 0, err
	}
	for _, pair := range [][2]int{{leftVersion.major, rightVersion.major}, {leftVersion.minor, rightVersion.minor}, {leftVersion.patch, rightVersion.patch}} {
		if pair[0] < pair[1] {
			return -1, nil
		}
		if pair[0] > pair[1] {
			return 1, nil
		}
	}
	return comparePrerelease(leftVersion.pre, rightVersion.pre), nil
}

func parseVersion(value string) (semVersion, error) {
	value = normalizeVersion(value)
	value = strings.SplitN(value, "+", 2)[0]
	core := value
	pre := ""
	if index := strings.IndexByte(value, '-'); index >= 0 {
		core, pre = value[:index], value[index+1:]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semVersion{}, fmt.Errorf("invalid semantic version %q", value)
	}
	numbers := make([]int, 3)
	for index, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return semVersion{}, fmt.Errorf("invalid semantic version %q", value)
		}
		numbers[index] = number
	}
	version := semVersion{major: numbers[0], minor: numbers[1], patch: numbers[2]}
	if pre != "" {
		version.pre = strings.Split(pre, ".")
	}
	return version, nil
}

func comparePrerelease(left, right []string) int {
	if len(left) == 0 && len(right) == 0 {
		return 0
	}
	if len(left) == 0 {
		return 1
	}
	if len(right) == 0 {
		return -1
	}
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	for index := 0; index < limit; index++ {
		leftNumber, leftErr := strconv.Atoi(left[index])
		rightNumber, rightErr := strconv.Atoi(right[index])
		switch {
		case leftErr == nil && rightErr == nil:
			if leftNumber < rightNumber {
				return -1
			}
			if leftNumber > rightNumber {
				return 1
			}
		case leftErr == nil:
			return -1
		case rightErr == nil:
			return 1
		default:
			if left[index] < right[index] {
				return -1
			}
			if left[index] > right[index] {
				return 1
			}
		}
	}
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return 0
}

func normalizeVersion(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "v")
}

func displayVersion(value string) string {
	value = normalizeVersion(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func progress(options Options, message string) {
	if options.Progress != nil {
		options.Progress(message)
	}
}

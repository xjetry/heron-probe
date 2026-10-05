package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/xjetry/heron-probe/internal/githubtransport"
	"github.com/xjetry/heron-probe/internal/releasesig"
)

const (
	officialAPI       = "https://api.github.com/repos/xjetry/heron-probe/releases"
	officialDownloads = "https://github.com/xjetry/heron-probe/releases/download/"
	maxMetadata       = 4 << 20
	// maxServiceFile 是归档里单个服务文件的大小上限，与清单文件的 releasesig.MaxSums 数值相同但语义不同。
	maxServiceFile = 1 << 20
	maxArchive     = 128 << 20
	maxBinary      = 256 << 20
)

// OfficialSource 只从固定官方仓库的下载目录取回产物字节，不做任何接受判定（见 Accept），
// 不接收外部下载地址或摘要。
type OfficialSource struct{ http *http.Client }

func NewOfficialSource() *OfficialSource {
	return &OfficialSource{http: githubtransport.NewClient(2 * time.Minute)}
}

type officialRelease struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

func (s *OfficialSource) release(ctx context.Context, suffix string) (officialRelease, error) {
	var release officialRelease
	body, err := s.get(ctx, officialAPI+suffix, "application/vnd.github+json", maxMetadata)
	if err != nil {
		return release, err
	}
	if err := json.Unmarshal(body, &release); err != nil {
		return release, fmt.Errorf("invalid official release metadata: %w", err)
	}
	if !ValidVersion(release.Tag) || release.Draft || release.Prerelease {
		return release, fmt.Errorf("official release is not a published stable version")
	}
	return release, nil
}

// Latest 返回 GitHub 标记为最新的正式版，不把预发布标签转换成正式版本。
func (s *OfficialSource) Latest(ctx context.Context) (string, error) {
	release, err := s.release(ctx, "/latest")
	if err != nil {
		return "", err
	}
	return release.Tag, nil
}

// Fetch 从固定官方下载目录取回某版本的清单、签名与归档，不做任何接受判定（见 Accept）。
// 地址只由固定仓库、版本与本地产物矩阵拼出，不调 releases API：草稿 release 没有公开下载地址，
// 预发布 tag 过不了 ValidVersion，资产是否存在由下载结果本身回答。task 只用到版本。
func (s *OfficialSource) Fetch(ctx context.Context, task Request, role, arch string) (Artifacts, error) {
	asset, err := archiveName(role, arch)
	if err != nil {
		return Artifacts{}, err
	}
	if !ValidVersion(task.Version) {
		return Artifacts{}, errors.New("invalid stable version")
	}
	base := officialDownloads + task.Version + "/"
	var a Artifacts
	if a.Sums, err = s.get(ctx, base+"SHA256SUMS", "application/octet-stream", releasesig.MaxSums); err != nil {
		return Artifacts{}, err
	}
	if a.Signature, err = s.get(ctx, base+"SHA256SUMS.sig", "application/octet-stream", releasesig.MaxFile); err != nil {
		return Artifacts{}, err
	}
	if a.Archive, err = s.get(ctx, base+asset, "application/octet-stream", maxArchive); err != nil {
		return Artifacts{}, err
	}
	return a, nil
}

func archiveName(role, arch string) (string, error) {
	switch role {
	case "hub":
		if arch != "amd64" && arch != "arm64" {
			return "", fmt.Errorf("unsupported hub architecture")
		}
	case "agent":
		switch arch {
		case "amd64", "arm64", "armv7", "386", "riscv64":
		default:
			return "", fmt.Errorf("unsupported agent architecture")
		}
	default:
		return "", fmt.Errorf("unsupported update role")
	}
	return "heron-" + role + "_linux_" + arch + ".tar.gz", nil
}

func (s *OfficialSource) get(ctx context.Context, endpoint, accept string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "heron-official-updater")
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download official release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("official release returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return nil, fmt.Errorf("official response exceeds size limit")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read official release: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("official response exceeds size limit")
	}
	return body, nil
}

func checksum(body []byte, asset string) ([32]byte, error) {
	var sum [32]byte
	found := false
	seen := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSuffix(string(body), "\n"), "\n") {
		if len(line) < 67 || line[64] != ' ' || line[65] != ' ' && line[65] != '*' {
			return sum, fmt.Errorf("invalid official checksum line")
		}
		raw, err := hex.DecodeString(line[:64])
		name := line[66:]
		if err != nil || name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\r\x00") || seen[name] {
			return sum, fmt.Errorf("invalid or duplicate official checksum entry")
		}
		seen[name] = true
		if name == asset {
			copy(sum[:], raw)
			found = true
		}
	}
	if !found {
		return sum, fmt.Errorf("official checksum missing target asset")
	}
	return sum, nil
}

func extractBinary(archive []byte, role string) ([]byte, error) {
	compressed := bytes.NewReader(archive)
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return nil, fmt.Errorf("open official archive: %w", err)
	}
	defer gz.Close()
	gz.Multistream(false)
	// 二进制及两个已知服务文件之外只允许 tar 零填充；全流限额也覆盖扩展头与尾部。
	limited := &io.LimitedReader{R: gz, N: maxBinary + 3*maxServiceFile + 1}
	tr := tar.NewReader(limited)
	binaryName := "heron-" + role
	seen := make(map[string]bool)
	var binary []byte
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read official archive: %w", err)
		}
		allowed := h.Name == binaryName || h.Name == binaryName+".service" || role == "agent" && h.Name == binaryName+".openrc"
		if !allowed || seen[h.Name] || h.Typeflag != tar.TypeReg || h.Linkname != "" || len(h.PAXRecords) != 0 {
			return nil, fmt.Errorf("unexpected, duplicate or non-regular official archive entry %q", h.Name)
		}
		seen[h.Name] = true
		limit := int64(maxServiceFile)
		if h.Name == binaryName {
			limit = maxBinary
		}
		if h.Size < 0 || h.Size > limit || h.Name == binaryName && h.Size == 0 {
			return nil, fmt.Errorf("invalid official archive entry size")
		}
		if h.Name == binaryName {
			binary, err = io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read official binary: %w", err)
			}
		}
	}
	// tar 的结束标记不保证 gzip 校验和已被读取，继续读完才能拒绝截断和损坏的压缩流。
	buffer := make([]byte, 32<<10)
	for {
		n, readErr := limited.Read(buffer)
		for _, b := range buffer[:n] {
			if b != 0 {
				return nil, fmt.Errorf("unexpected data after official tar archive")
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("finish official archive: %w", readErr)
		}
	}
	if limited.N <= 0 || compressed.Len() != 0 {
		return nil, fmt.Errorf("official archive exceeds limits or contains trailing data")
	}
	if len(binary) == 0 {
		return nil, fmt.Errorf("official archive has no binary")
	}
	return binary, nil
}

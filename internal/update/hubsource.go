package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/agentconfig"
	"github.com/xjetry/heron-probe/internal/hubclient"
	"github.com/xjetry/heron-probe/internal/releasesig"
)

// maxRelayResponse 是 GetRelease 响应正文的上限：三份文件各自上限之和，加 protobuf 与 Connect 的编码余量。
const maxRelayResponse = maxArchive + releasesig.MaxSums + releasesig.MaxFile + 64<<10

// HubSource 经 hub 的 GetRelease 取产物（spec §4.10），不做接受判定（见 Accept）。hub 地址、token 与明文许可在
// 每个任务现读 agent 配置：节点 token 轮换后无须同步。解析与地址规则和 agent 同一实现（agentconfig），连接与 agent
// 同一构造（hubclient），所以 https 要求、不跟随重定向、响应上限对两者同时成立（spec §5.7）。
type HubSource struct {
	// readConfig 返回 agent 配置原文；正式实现按服务用户属主等检查打开并限量读取（serve 里构造）。
	readConfig func() ([]byte, error)
}

func NewHubSource(readConfig func() ([]byte, error)) *HubSource {
	return &HubSource{readConfig: readConfig}
}

func (s *HubSource) Fetch(ctx context.Context, task Request, role, arch string) (Artifacts, error) {
	if role != "agent" {
		return Artifacts{}, errors.New("hub source serves only agent updates")
	}
	raw, err := s.readConfig()
	if err != nil {
		return Artifacts{}, fmt.Errorf("read agent config: %w", err)
	}
	cfg, err := agentconfig.Decode(bytes.NewReader(raw), agentConfigPath)
	if err != nil {
		return Artifacts{}, err
	}
	if err := agentconfig.CheckHub(cfg.Hub, cfg.InsecureHTTP); err != nil {
		return Artifacts{}, err
	}
	req := connect.NewRequest(&heronv1.GetReleaseRequest{TaskId: task.ID, Arch: arch})
	req.Header().Set("Authorization", "Bearer "+cfg.Token)
	resp, err := hubclient.New(cfg.Hub, downloadTimeout, maxRelayResponse).GetRelease(ctx, req)
	if err != nil {
		return Artifacts{}, fmt.Errorf("fetch release from hub: %w", err)
	}
	return Artifacts{Sums: resp.Msg.GetSums(), Signature: resp.Msg.GetSignature(), Archive: resp.Msg.GetArchive()}, nil
}

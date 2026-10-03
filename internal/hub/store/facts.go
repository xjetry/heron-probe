package store

import (
	"context"
	"database/sql"
	"strings"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/agentwire"
	"google.golang.org/protobuf/encoding/protojson"
)

func (s *Store) factsTx(nodeID int64, hash uint64, f *heronv1.Facts) func(*sql.Tx) error {
	return func(tx *sql.Tx) error {
		if err := agentwire.ValidateNetwork(f.GetNetwork()); err != nil {
			return err
		}
		if err := agentwire.ValidateDiagnostics(f.GetDiagnostics()); err != nil {
			return err
		}
		exists, err := nodeExistsTx(tx, nodeID)
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		network, err := protojson.Marshal(f.GetNetwork())
		if err != nil {
			return err
		}
		diagnostics := []byte("null")
		if f.GetDiagnostics() != nil {
			diagnostics, err = protojson.Marshal(f.GetDiagnostics())
			if err != nil {
				return err
			}
		}
		_, err = tx.Exec(`INSERT INTO node_facts (node_id, facts_hash, hostname, os, kernel, arch, virtualization, cpu_model, cpu_cores, agent_version, icmp_available, updated_at, network, diagnostics)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (node_id) DO UPDATE SET facts_hash = excluded.facts_hash, hostname = excluded.hostname, os = excluded.os,
			kernel = excluded.kernel, arch = excluded.arch, virtualization = excluded.virtualization, cpu_model = excluded.cpu_model,
			cpu_cores = excluded.cpu_cores, agent_version = excluded.agent_version, icmp_available = excluded.icmp_available, updated_at = excluded.updated_at, network = excluded.network, diagnostics = excluded.diagnostics`,
			nodeID, int64(hash), f.GetHostname(), f.GetOs(), f.GetKernel(), f.GetArch(), f.GetVirtualization(),
			f.GetCpuModel(), f.GetCpuCores(), f.GetAgentVersion(), f.GetIcmpAvailable(), s.clk.Now().Unix(), string(network), string(diagnostics))
		return err
	}
}

// 解码和结构校验共用同一入口，读库与快照恢复不能绕过上报的状态、地址族和时间约束。
func decodeNetwork(text string) (*heronv1.NetworkInfo, error) {
	network := &heronv1.NetworkInfo{}
	if err := protojson.Unmarshal([]byte(text), network); err != nil {
		return nil, err
	}
	if err := agentwire.ValidateNetwork(network); err != nil {
		return nil, err
	}
	if network.Ipv4 == nil && network.Ipv6 == nil {
		return nil, nil
	}
	return network, nil
}

// null 表示旧版尚未提供诊断，与已报告但字段皆为空的对象不同；读库和恢复共用严格解码。
func decodeDiagnostics(text string) (*heronv1.AgentDiagnostics, error) {
	if strings.Trim(text, " \t\r\n") == "null" {
		return nil, nil
	}
	diagnostics := &heronv1.AgentDiagnostics{}
	if err := protojson.Unmarshal([]byte(text), diagnostics); err != nil {
		return nil, err
	}
	if err := agentwire.ValidateDiagnostics(diagnostics); err != nil {
		return nil, err
	}
	return diagnostics, nil
}

func (s *Store) UpsertFacts(ctx context.Context, nodeID int64, hash uint64, f *heronv1.Facts) error {
	return s.write(ctx, s.factsTx(nodeID, hash, f))
}

// UpsertFactsAsync 供上报路径使用：投递即返回，上报不等待数据库。
func (s *Store) UpsertFactsAsync(nodeID int64, hash uint64, f *heronv1.Facts, done func(error)) {
	s.writeAsync(s.factsTx(nodeID, hash, f), done)
}

// FactsHashes 存的是 int64，读回按位转回 uint64；fixed64 的全部取值都能往返。
func (s *Store) FactsHashes(ctx context.Context) (map[int64]uint64, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT node_id, facts_hash FROM node_facts")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]uint64{}
	for rows.Next() {
		var id, h int64
		if err := rows.Scan(&id, &h); err != nil {
			return nil, err
		}
		out[id] = uint64(h)
	}
	return out, rows.Err()
}

func (s *Store) QueryFacts(ctx context.Context, nodeID int64, hostname, os *string) error {
	return s.r.QueryRowContext(ctx, "SELECT hostname, os FROM node_facts WHERE node_id = ?", nodeID).Scan(hostname, os)
}

// CpuCores 返回节点上报的 CPU 核数；没有 facts 行与 cpu_cores = 0 同义，都返回 0，
// 调用方（按核负载告警）据此把该节点视为无读数，不需要区分两种缺失。
func (s *Store) CpuCores(ctx context.Context, nodeID int64) (int64, error) {
	var cores int64
	err := s.r.QueryRowContext(ctx, "SELECT cpu_cores FROM node_facts WHERE node_id = ?", nodeID).Scan(&cores)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return cores, err
}

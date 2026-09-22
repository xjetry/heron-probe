package store

import (
	"context"
	"database/sql"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
)

func (s *Store) factsTx(nodeID int64, hash uint64, f *probev1.Facts) func(*sql.Tx) error {
	return func(tx *sql.Tx) error {
		exists, err := nodeExistsTx(tx, nodeID)
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		_, err = tx.Exec(`INSERT INTO node_facts (node_id, facts_hash, hostname, os, kernel, arch, virtualization, cpu_model, cpu_cores, agent_version, icmp_available, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (node_id) DO UPDATE SET facts_hash = excluded.facts_hash, hostname = excluded.hostname, os = excluded.os,
			kernel = excluded.kernel, arch = excluded.arch, virtualization = excluded.virtualization, cpu_model = excluded.cpu_model,
			cpu_cores = excluded.cpu_cores, agent_version = excluded.agent_version, icmp_available = excluded.icmp_available, updated_at = excluded.updated_at`,
			nodeID, int64(hash), f.GetHostname(), f.GetOs(), f.GetKernel(), f.GetArch(), f.GetVirtualization(),
			f.GetCpuModel(), f.GetCpuCores(), f.GetAgentVersion(), f.GetIcmpAvailable(), s.clk.Now().Unix())
		return err
	}
}

func (s *Store) UpsertFacts(ctx context.Context, nodeID int64, hash uint64, f *probev1.Facts) error {
	return s.write(ctx, s.factsTx(nodeID, hash, f))
}

// UpsertFactsAsync 供上报路径使用：投递即返回，上报不等待数据库。
func (s *Store) UpsertFactsAsync(nodeID int64, hash uint64, f *probev1.Facts, done func(error)) {
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

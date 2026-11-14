// @author: kevinkelly
// @license: Apache Licence
// @file: manager.go
// @time: 2026/10/28

// Package collect 采集集群架构、总览指标与趋势。
package collect

import (
	"context"
	"sync"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/secure"
	"starrocks-ops/internal/sr"
)

// Manager 缓存每个集群的连接池；集群信息更新（UpdatedAt 变化）后自动重建。
type Manager struct {
	box     *secure.Box
	mu      sync.Mutex
	clients map[string]entry
	Sampler *Sampler
}

type entry struct {
	c   *sr.Client
	ver time.Time
}

func NewManager(box *secure.Box) *Manager {
	m := &Manager{box: box, clients: map[string]entry{}}
	m.Sampler = newSampler(m)
	return m
}

// ConnInfo 解密集群密码，组装连接参数。
func (m *Manager) ConnInfo(c model.Cluster) (sr.ConnInfo, error) {
	pw, err := m.box.Decrypt(c.PasswordEnc)
	if err != nil {
		return sr.ConnInfo{}, err
	}
	return sr.ConnInfo{Hosts: c.FEHosts, QueryPort: c.QueryPort, HTTPPort: c.HTTPPort, User: c.User, Password: pw}, nil
}

// Client 返回（必要时新建）集群连接。
func (m *Manager) Client(ctx context.Context, c model.Cluster) (*sr.Client, error) {
	m.mu.Lock()
	e, ok := m.clients[c.ID]
	m.mu.Unlock()
	if ok && e.ver.Equal(c.UpdatedAt) {
		return e.c, nil
	}
	ci, err := m.ConnInfo(c)
	if err != nil {
		return nil, err
	}
	cl, err := sr.Open(ctx, ci)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if cur, ok := m.clients[c.ID]; ok && cur.ver.Equal(c.UpdatedAt) {
		// 另一个请求同时建好了同版本的连接：用它，丢弃自己刚建的，避免关掉对方正在使用的连接
		m.mu.Unlock()
		cl.Close()
		return cur.c, nil
	}
	if old, ok := m.clients[c.ID]; ok { // 集群信息已更新：旧连接可能仍有请求在用，延迟关闭
		go func(c *sr.Client) { time.Sleep(30 * time.Second); c.Close() }(old.c)
	}
	m.clients[c.ID] = entry{c: cl, ver: c.UpdatedAt}
	m.mu.Unlock()
	return cl, nil
}

// Forget 关闭并移除集群连接（删除 / 编辑集群时调用）。
func (m *Manager) Forget(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.clients[id]; ok {
		e.c.Close()
		delete(m.clients, id)
	}
}

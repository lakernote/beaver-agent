package server

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/lakernote/beaver-agent/internal/store"
	"github.com/lakernote/beaver-agent/internal/weixin"
)

// weixinManager coordinates worker lifecycles. Login, inbox and task behavior
// live in focused sibling files; protocol and persistence stay in their packages.
type weixinManager struct {
	server      *Server
	gateway     weixin.Gateway
	lifecycleMu sync.Mutex
	mu          sync.Mutex
	logins      map[string]*weixinLogin
	pollers     map[string]weixinPoller
	// delivery 每个成员只允许一个活跃投递代。停用成员时撤销
	// 该代的 context 并删除所有权；旧协程即使稍后才被调度，也无法
	// 越过 generation 校验发送消息或清理新代。
	delivery           map[string]*weixinDelivery
	deliveryGeneration map[string]uint64
	deliveryStopped    map[string]bool
	deliveryGate       map[string]*sync.Mutex
	channelStopped     bool
}

type weixinPoller struct {
	ctx    context.Context
	cancel context.CancelFunc
	token  string
}

type weixinDelivery struct {
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	generation uint64
	wake       bool
}

func newWeixinManager(server *Server, gateway weixin.Gateway) *weixinManager {
	return &weixinManager{
		server: server, gateway: gateway,
		logins: make(map[string]*weixinLogin), pollers: make(map[string]weixinPoller),
		delivery: make(map[string]*weixinDelivery), deliveryGeneration: make(map[string]uint64),
		deliveryStopped: make(map[string]bool), deliveryGate: make(map[string]*sync.Mutex),
	}
}

// accountDeliveryGate 只串行同一成员的“提交新轮次”和“冻结待发送结果”。
// 网络发送不持有这把锁；发送期间到达的新轮次由数据库 CAS 保护。
func (manager *weixinManager) accountDeliveryGate(accountID string) *sync.Mutex {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.deliveryGate == nil {
		manager.deliveryGate = make(map[string]*sync.Mutex)
	}
	gate := manager.deliveryGate[accountID]
	if gate == nil {
		gate = &sync.Mutex{}
		manager.deliveryGate[accountID] = gate
	}
	return gate
}

func (manager *weixinManager) start() {
	manager.lifecycleMu.Lock()
	defer manager.lifecycleMu.Unlock()
	manager.startLocked()
}

// startLocked reconciles workers from the persisted state. lifecycleMu must be held.
func (manager *weixinManager) startLocked() {
	settings, err := manager.server.store.GetWeixinSettings()
	if err != nil || !settings.Enabled {
		return
	}
	accounts, err := manager.server.store.ListWeixinAccounts()
	if err != nil {
		log.Printf("微信远程：读取绑定失败: %v", err)
		return
	}
	manager.mu.Lock()
	manager.channelStopped = false
	for _, account := range accounts {
		if account.Enabled {
			delete(manager.deliveryStopped, account.ID)
		}
	}
	manager.mu.Unlock()
	for _, account := range accounts {
		if account.Enabled {
			manager.startPoller(account.ID)
			manager.resumeDelivery(account.ID)
		}
	}
}

func (manager *weixinManager) stopAll() {
	manager.lifecycleMu.Lock()
	defer manager.lifecycleMu.Unlock()
	manager.stopAllLocked()
}

// stopAllLocked cancels every current channel generation. lifecycleMu must be held.
func (manager *weixinManager) stopAllLocked() {
	manager.mu.Lock()
	manager.channelStopped = true
	for id, poller := range manager.pollers {
		poller.cancel()
		delete(manager.pollers, id)
	}
	for id := range manager.delivery {
		manager.cancelDeliveryLocked(id)
	}
	manager.mu.Unlock()
}

// setEnabled serializes the persisted switch and its in-memory lifecycle as one
// transition. Without this boundary, concurrent enable/disable requests can
// leave the database and channelStopped describing opposite final states.
func (manager *weixinManager) setEnabled(enabled bool, now time.Time) (bool, error) {
	manager.lifecycleMu.Lock()
	defer manager.lifecycleMu.Unlock()
	current, err := manager.server.store.GetWeixinSettings()
	if err != nil {
		return false, err
	}
	changed := current.Enabled != enabled
	if enabled && !current.Enabled {
		current.IgnoreBefore = now
	} else if !enabled {
		if err := manager.server.store.SuppressPendingWeixinDeliveries(now); err != nil {
			return false, err
		}
	}
	current.Enabled = enabled
	if _, err := manager.server.store.SaveWeixinSettings(current); err != nil {
		return false, err
	}
	if enabled {
		manager.startLocked()
	} else {
		manager.stopAllLocked()
	}
	return changed, nil
}

func (manager *weixinManager) restartAccount(id string) {
	manager.lifecycleMu.Lock()
	defer manager.lifecycleMu.Unlock()
	manager.stopAccountLocked(id)
	settings, err := manager.server.store.GetWeixinSettings()
	if err != nil || !settings.Enabled {
		return
	}
	account, err := manager.server.store.GetWeixinAccount(id)
	if err != nil || !account.Enabled {
		return
	}
	manager.mu.Lock()
	// lifecycleMu 保证这里读到的持久化开关不会被并发启停请求穿越；
	// 因此可以安全地清除该成员的停止标记并创建新代。
	manager.channelStopped = false
	delete(manager.deliveryStopped, id)
	manager.mu.Unlock()
	manager.startPoller(id)
	manager.resumeDelivery(id)
}

func (manager *weixinManager) stopAccount(id string) {
	manager.lifecycleMu.Lock()
	defer manager.lifecycleMu.Unlock()
	manager.stopAccountLocked(id)
}

// stopAccountLocked requires lifecycleMu. The poller context is canceled while
// manager.mu is still held, so no new generation can clear deliveryStopped in
// the small window before an old inbound reply observes its parent context.
func (manager *weixinManager) stopAccountLocked(id string) {
	manager.mu.Lock()
	if manager.deliveryStopped == nil {
		manager.deliveryStopped = make(map[string]bool)
	}
	manager.deliveryStopped[id] = true
	poller := manager.pollers[id]
	delete(manager.pollers, id)
	if poller.cancel != nil {
		poller.cancel()
	}
	manager.cancelDeliveryLocked(id)
	manager.mu.Unlock()
}

func (manager *weixinManager) deleteAccount(id string) error {
	manager.lifecycleMu.Lock()
	defer manager.lifecycleMu.Unlock()
	manager.stopAccountLocked(id)
	return manager.server.store.DeleteWeixinAccount(id)
}

// cancelDeliveryLocked 立即使当前投递代失效。调用方必须持有 manager.mu。
func (manager *weixinManager) cancelDeliveryLocked(id string) {
	if manager.deliveryGeneration == nil {
		manager.deliveryGeneration = make(map[string]uint64)
	}
	manager.deliveryGeneration[id]++
	if current := manager.delivery[id]; current != nil {
		current.cancel()
		delete(manager.delivery, id)
	}
}

func (manager *weixinManager) startPoller(id string) {
	manager.mu.Lock()
	if manager.channelStopped || manager.deliveryStopped[id] {
		manager.mu.Unlock()
		return
	}
	if _, exists := manager.pollers[id]; exists {
		manager.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(manager.server.ctx)
	token := newID()
	manager.pollers[id] = weixinPoller{ctx: ctx, cancel: cancel, token: token}
	manager.mu.Unlock()
	if !manager.server.beginBackground() {
		manager.mu.Lock()
		if current, ok := manager.pollers[id]; ok && current.token == token {
			delete(manager.pollers, id)
		}
		manager.mu.Unlock()
		cancel()
		return
	}
	go func() {
		defer manager.server.completeBackground()
		defer cancel()
		defer func() {
			manager.mu.Lock()
			if current, ok := manager.pollers[id]; ok && current.token == token {
				delete(manager.pollers, id)
			}
			manager.mu.Unlock()
		}()
		manager.poll(ctx, id)
	}()
}

// outboundContext returns the context owned by the account's current poller.
// Automation sends use it so disabling, deleting, or rebinding an account also
// cancels an already in-flight notification instead of letting it borrow a
// later account generation.
func (manager *weixinManager) outboundContext(id string) (context.Context, bool) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.channelStopped || manager.deliveryStopped[id] {
		return nil, false
	}
	poller, ok := manager.pollers[id]
	if !ok || poller.ctx == nil || poller.ctx.Err() != nil {
		return nil, false
	}
	return poller.ctx, true
}

func gatewayAccount(account store.WeixinAccount) weixin.Account {
	return weixin.Account{ID: account.ID, Token: account.Token, BaseURL: account.BaseURL}
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

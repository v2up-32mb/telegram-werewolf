package observability

import "sync"

// 预定义计数器/仪表名，与《技术选型》§11.3 轻量计数器清单一一对应。
const (
	MetricActiveRooms       = "active_rooms"
	MetricOutboxQueueLength = "outbox_queue_length"
	MetricTelegramSent      = "telegram_messages_sent"
	MetricTelegramFailed    = "telegram_messages_failed"
	MetricRateLimited429    = "telegram_429_count"
	MetricRetried           = "telegram_retry_count"
	MetricPhaseTimeout      = "phase_timeout_count"
	MetricStaleRejected     = "stale_command_rejected_count"
)

// Metrics 持有线程安全的轻量计数器与仪表。
// MVP 不绑定 Prometheus/ELK；Snapshot 供运行状态页面或后续采集器读取。
type Metrics struct {
	mu       sync.RWMutex
	counters map[string]int64
	gauges   map[string]int64
}

// NewMetrics 创建空的计数器集合。
func NewMetrics() *Metrics {
	return &Metrics{
		counters: make(map[string]int64),
		gauges:   make(map[string]int64),
	}
}

// IncCounter 将命名计数器加一。
func (m *Metrics) IncCounter(name string) {
	m.AddCounter(name, 1)
}

// AddCounter 将命名计数器增加 delta（可为负）。
func (m *Metrics) AddCounter(name string, delta int64) {
	m.mu.Lock()
	m.counters[name] += delta
	m.mu.Unlock()
}

// SetGauge 将命名仪表设为 value。
func (m *Metrics) SetGauge(name string, value int64) {
	m.mu.Lock()
	m.gauges[name] = value
	m.mu.Unlock()
}

// Snapshot 返回 counters 与 gauges 的一致性副本，供读取方安全使用。
func (m *Metrics) Snapshot() (counters, gauges map[string]int64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	counters = make(map[string]int64, len(m.counters))
	gauges = make(map[string]int64, len(m.gauges))
	for name, v := range m.counters {
		counters[name] = v
	}
	for name, v := range m.gauges {
		gauges[name] = v
	}
	return counters, gauges
}

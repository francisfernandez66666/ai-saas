// 企微会话存档观测位（E8-4，2026-09-24）
//
// 这个探针盯的是一类很安静的失败：商家在后台开了「会话存档」，管理员以为留痕在跑，
// 实际上一条都没落库——可能因为二进制里没编官方 C SDK（当前默认形态）、
// 可能因为拉取器接上了但私钥没配、也可能因为游标每轮都撞在同一个错误码上。
// 这三种在 Admin 页面上长得一模一样（"存档已开启，暂无消息"），
// 只有把「几个通道开了 / 拉取器在不在 / 库里几行 / 上一轮为什么失败」四个数摆在一起才分得开。
//
// 依赖方向：internal/channel 已经依赖 internal/metrics，观测位**不能再 import channel**（成环）。
// 因此取数由 cmd/server/main.go 在启动时经 SetArchiveProbe 注入闭包，本文件只认自己的结构体。
// 没装配（单测、gateway 等不跑存档的进程）时判 ok/not_wired，绝不 panic 也不误报故障。
//
// 缓存纪律与数据层卫生探针一致：/status/detail 与告警 tick 会反复调本函数，
// TTL 内复用上次结果；取数失败沿用旧值并标 stale（宁可不新，也不要每次为展示去扫表）。
package metrics

import (
	"strconv"
	"sync"
	"time"
)

// ArchiveProbe 会话存档平台总览（字段与 channel 侧同名结构体一一对应，
// 但不共用类型：metrics 不得反向依赖 channel）。
// 由 hygieneChecks 追加进健康清单，故与数据层卫生探针同处观测面。
type ArchiveProbe struct {
	EnabledChannels int64     // 启用存档且 status=active 的通道数
	FetcherReady    bool      // 官方存档拉取器（C SDK 封装）是否已注入
	Stored          int64     // 已落库的存档行数
	Failed          int64     // 解密失败留痕行数（有信封没正文）
	SyncRan         bool      // 本进程是否跑过存档同步 ticker
	SyncRanAt       time.Time // 上一轮同步时间
	SyncErrors      string    // 上一轮失败原因码聚合（"archive_key_missing=2" 形式）
}

// 探针接缝：单测注入固定值，不依赖真库（真库形态由冒烟 §三十七覆盖）
var (
	// archiveProbeFn 取数闭包：error 必填——"零值"与"取数失败"必须分得开，
	// 否则管理员关掉全部存档时探针会把上一次的数字当成现状继续显示。
	archiveProbeFn  func() (ArchiveProbe, error)
	archiveProbeTTL = 5 * time.Minute

	// archiveProbeNow 时钟接缝（单测推进 TTL 不起真等待）
	archiveProbeNow func() time.Time = time.Now

	archiveProbeMu     sync.Mutex
	archiveProbeAt     time.Time
	archiveProbeCached ArchiveProbe
	archiveProbeStale  bool
)

// SetArchiveProbe 装配存档取数闭包（由 main.go 调用；传 nil 即卸载）。
func SetArchiveProbe(fn func() (ArchiveProbe, error)) {
	archiveProbeMu.Lock()
	archiveProbeFn = fn
	archiveProbeAt = time.Time{}
	archiveProbeStale = false
	archiveProbeMu.Unlock()
}

// ResetArchiveProbe 清空装配与缓存（测试用；生产只在启动时装配一次）。
func ResetArchiveProbe() { SetArchiveProbe(nil) }

// archiveProbeSnapshot 取存档总览（TTL 缓存）。
// 返回 (探针值, 是否已装配, 是否沿用旧值)——未装配时第一项无意义。
func archiveProbeSnapshot() (ArchiveProbe, bool, bool) {
	archiveProbeMu.Lock()
	defer archiveProbeMu.Unlock()
	if archiveProbeFn == nil {
		return ArchiveProbe{}, false, false
	}
	now := archiveProbeNow()
	// TTL 内复用上次结果，**但"没有任何通道开存档"的那一次不复用**：
	// 零通道时库里根本不产生昂贵查询（只是一次走索引的 Pluck），现算几乎免费；
	// 而"管理员刚在后台开了存档"正是运维必须当场看见的转折——
	// 缓存它 5 分钟会让探针在开着存档的通道旁边继续写"disabled"，
	// 冒烟 §三十七 也会在 §三十一 那次 /status/detail 之后稳定读到这个旧状态。
	if !archiveProbeAt.IsZero() && archiveProbeCached.EnabledChannels > 0 &&
		now.Sub(archiveProbeAt) < archiveProbeTTL {
		return archiveProbeCached, true, archiveProbeStale
	}
	next, err := archiveProbeFn()
	if err != nil {
		// 取数失败沿用旧值并标 stale：观测位不得因一次抖动假装"没开存档"。
		archiveProbeStale = true
		return archiveProbeCached, true, archiveProbeStale
	}
	archiveProbeAt, archiveProbeCached, archiveProbeStale = now, next, false
	return next, true, false
}

// ChatArchiveHealth 对外暴露「企微会话存档」观测项，供 /status/detail 直出。
//
// 为什么要单独导出而不是只躺在 snap.Checks 里：/status/detail 的 readiness 数组来自
// ComputeReadiness()（配置开关体检），而 snap.Checks（ComputeHealth）此前只在公开 /status
// 里被折成一个 ok/warn/crit 总字。"开了存档却一条都没落库"这种形态必须在详情端点看得见具体原因码，
// 否则它只影响状态字的一个 warn，运维翻不出是哪儿——与 orphan_messages 直出为字段同口径。
func ChatArchiveHealth() HealthCheck { return archiveCheck() }

// archiveCheck 组装「企微会话存档」健康检查（由 hygieneChecks 追加进健康清单）。
//
// 分级口径（刻意不判 crit：存档是合规留痕能力，缺数据要人主动去看，但不该半夜刷群）：
//   - not_wired      本进程没装配（gateway/单测）——ok，不是故障
//   - disabled       没有任何通道开存档——ok，这是出厂默认
//   - sdk_not_built  开了通道但拉取器没接——warn，"留痕其实一条都不会有"必须看得见
//   - 原因码         上一轮有失败通道或压根没跑过轮——warn，附稳定码
//   - 失败占比       解密失败行数占已落库比例达阈值——warn（默认 30%）
func archiveCheck() HealthCheck {
	probe, wired, stale := archiveProbeSnapshot()
	desc := "企微会话存档（合规留痕）：已启用通道 / 已落库 / 解密失败"
	if stale {
		desc += "（沿用 " + archiveProbeTTL.String() + " 内的上一次取数，本轮查询失败）"
	}
	// WarnAt/CritAt 留空 "-"：本项是"形态"判级而非单一数值阈值
	ch := HealthCheck{Name: "chat_archive", Status: StatusOK, WarnAt: "-", CritAt: "-", Desc: desc}
	if !wired {
		ch.Value = "not_wired"
		ch.Desc = "本进程未装配存档取数（不跑存档 ticker，非故障）"
		return ch
	}
	if probe.EnabledChannels == 0 {
		ch.Value = "disabled"
		ch.Desc = "无通道启用会话存档（出厂默认关闭，开了才拉、才落库）"
		return ch
	}
	numbers := strconv.FormatInt(probe.EnabledChannels, 10) + " 通道 / 落库 " +
		strconv.FormatInt(probe.Stored, 10) + " 行 / 解密失败 " + strconv.FormatInt(probe.Failed, 10) + " 行"
	switch {
	case !probe.FetcherReady:
		// 这是当前代码库的真实形态：数据层与解密链已就位，取数要等官方 SDK 编进来
		ch.Value = numbers + " · sdk_not_built"
		ch.Status = StatusWarn
		ch.Desc = "已开存档但官方拉取器未接入（C SDK 需构建期注入）：游标不会前进，一条留痕也不会有"
	case probe.SyncErrors != "":
		ch.Value = numbers + " · " + probe.SyncErrors
		ch.Status = StatusWarn
		ch.Desc = "存档同步上一轮有失败通道（原因码见 value，日志按 channel 逐条）"
	case !probe.SyncRan:
		ch.Value = numbers + " · ticker_not_run"
		ch.Status = StatusWarn
		ch.Desc = "拉取器已接但本进程还没跑过存档同步轮（ticker 未装配或刚启动）"
	case probe.Stored > 0 && probe.Failed*100/probe.Stored >= monitorCfgInt("monitor_archive_failed_warn_pct", 30):
		ch.Value = numbers
		ch.Status = StatusWarn
		ch.Desc = "解密失败占比达阈值：多为公钥版本与企微后台不一致或私钥不配对"
	default:
		ch.Value = numbers
	}
	return ch
}

// Package model 业务领域模型与 GORM 表结构定义（SaaS 多租户，tenant_id 隔离，写入经自动盖章回调）。
package model

import "time"

// ============================================================
// 获客活码（2026-09-23 获客批）
//
// 这批要回答的问题很具体：销售在抖音、小红书、门店立牌、朋友圈各放了一个入口，
// 钱花出去了，系统里却只有一句写死的"来源=外部体验"——没人能回答"哪个渠道值得继续投钱"。
// 活码就是把这句话回答出来的最小闭环：**一个码 = 一个渠道位**，扫码进来的人被打上这个码，
// 之后的开口、留资、到店、成交全记在它名下。
//
// 为什么是两张表：
//   - acquisition_codes 是**配置**（码本身、渠道位、启停），一行可以活很多年；
//   - acquisition_scans 是**事件**（每次扫码一行），只增不改。
//     分开的决定理由是"扫了但一句话没说"这一层：只往客户表打一列，
//     就永远看不出"这个码带来 500 次扫码、只有 3 个人开口"——而这恰恰是最该停投的信号。
//
// 为什么码只启停、不给删除：码印上海报就收不回来。删行会让历史归因指向空气，
// 更糟的是字符串被回收后再建同名码，会把两拨不相干的客户合并成一笔统计。
// **错账比缺账难查**，所以宁可留一行停用记录。
//
// 与 migrations/022 的关系：建表真源在这里（AutoMigrate 先建列），
// 022 补的是 AutoMigrate 建不出来的三条**部分索引**与非空才索引的归因列。
// ============================================================

// 活码状态（列 size:20；新增取值请开新值，勿复用旧字面量——历史行按它读）
const (
	// AcquisitionStatusActive 启用中：可扫码、可归因
	AcquisitionStatusActive = "active"
	// AcquisitionStatusDisabled 已停用：扫码不再归因，历史统计照读不误
	AcquisitionStatusDisabled = "disabled"
)

// AcquisitionCode 一个获客活码（一个渠道位一行，只启停不删除）
type AcquisitionCode struct {
	ID       uint `gorm:"primaryKey" json:"id"`
	TenantID uint `gorm:"index:idx_acq_code_tenant,priority:1;not null;default:0" json:"tenant_id"` // 租户ID（归属，不可跨租户读写）
	// Code 公开短码，**全库唯一而非租户内唯一**：公开解析链路只拿得到码本身、拿不到租户，
	// 必须单键定位到唯一行；这也是"别人家的码"能被识别并拒掉的前提（见 acquisition.ApplyToGuest）。
	Code string `gorm:"uniqueIndex:ux_acq_code_global;size:16;not null" json:"code"`
	Name string `gorm:"size:100;not null;default:''" json:"name"` // 用途名，如"门店前台立牌"
	// Channel 渠道位（抖音/小红书/微信/百度/门店/其它）。与 Customer.Source 同源取值，
	// 这样扫码客户的来源列能和既有客户列表、贡献度口径一起读，不生第二套渠道枚举。
	Channel string `gorm:"size:30;not null;default:''" json:"channel"`
	// OwnerUserID 建码人。**刻意只是统计维度，绝不参与客户分配**——
	// "未留资的新客不给顾问"是线索两分支铁律，员工个人码也不该开后门。
	OwnerUserID uint      `gorm:"not null;default:0" json:"owner_user_id"`
	Status      string    `gorm:"index:idx_acq_code_tenant,priority:2;size:20;not null;default:'active'" json:"status"`
	Remark      string    `gorm:"size:255;not null;default:''" json:"remark"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TableName 显式表名（与 022 迁移一致，勿依赖复数推断）
func (AcquisitionCode) TableName() string { return "acquisition_codes" }

// AcquisitionScan 一次扫码事件（只增不改；漏斗最上面那一层）
//
// customer_id=0 且 visitor_key=” 的行 = 落地页被打开但访客还没领身份。
// 这类行没有去重依据（没有任何能代表"同一个人"的东西），只按 IP 限流兜底，
// 所以它计入"打开次数"时天然带噪——渠道之间比较时看**开口率/留资率**，别只看这一格。
type AcquisitionScan struct {
	ID       uint `gorm:"primaryKey" json:"id"`
	TenantID uint `gorm:"index:idx_acq_scan_code_time,priority:1;not null;default:0" json:"tenant_id"`
	// CodeID 指向 acquisition_codes.id。这里用 ID 而不是字符串（与 customers.acquisition_code
	// 相反）：事件是"当时发生的事实"，按 ID 聚合最快且码停用也不影响历史事件归属。
	CodeID     uint      `gorm:"index:idx_acq_scan_code_time,priority:2;not null" json:"code_id"`
	CustomerID uint      `gorm:"not null;default:0" json:"customer_id"` // 0=只扫码未建客
	VisitorKey string    `gorm:"size:64;not null;default:''" json:"-"`  // 同一访客去重锚（不下发前端）
	CreatedAt  time.Time `json:"created_at"`
}

// TableName 显式表名
func (AcquisitionScan) TableName() string { return "acquisition_scans" }

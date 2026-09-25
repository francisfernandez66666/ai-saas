// 行业包"同编码只有一个上架版本"的写侧单点（2026-09-25 残项收口批·残项5）。
//
// 要修的事实：industry_packs 表上 **code 只有普通索引、从来没有唯一约束**，
// 而把包置为 active 的写入点有三处，没有一处做过"同 code 兄弟行让位"：
//  1. SuperPackStatus（超管点"上架"）——只改自己那一行；
//  2. AutoRegisterLocalPacks（启动期扫 data/packs/*.aipack）——**每个文件都强制 active**，
//     于是同一代的三个版本文件同时上架；这是现场 8 个编码多版本并存的主因；
//  3. SuperPackUpload——新建行默认 disabled，本身不制造冲突（保持不变）。
//
// 危害不是"列表里多几行"这种观感问题：
//   - 租户侧可选包列表（TenantPackList）按 code 摊开，"汽车"出现三行、名字一模一样，
//     顾问分不清哪行是现役，选错版本直接物化错内容；
//   - 继承链回溯（openActivePackByCode）在 multi-active 下只能靠 id DESC 猜"哪个是新版本"，
//     而历史行是被误标层级的（见 G-22 注释），猜错就把老内容物化给新租户；
//   - 包质量归因（D9）按 pack_code+version 统计，同 code 双上架会让两个版本各吃一半样本，
//     判优晋升的置信度算出来谁都不信。
//
// 口径：**一个 code 至多一个 active 版本，换版本 = 原子换位的下架+上架**。
// 不变式由迁移 027 的部分唯一索引在 DB 层兜底；本文件负责让所有写入点**提前满足**它，
// 而不是撞 23505 再把错误抛给超管。二者缺一都不成立：只改代码没有索引，人工 SQL 或
// 旧版本进程仍能塞进第二行；只有索引没有代码，超管点"上架"会看到一条数据库错误。
package industrypack

import (
	"errors"
	"strconv"
	"strings"

	"ai-scrm/internal/model"

	"gorm.io/gorm"
)

// StatusActive/StatusDisabled 包目录状态词表单点。
// 迁移 027 的部分唯一索引 WHERE 子句里写的就是这两个字面量，代码侧改动必须同步索引——
// 因此把它们收成常量并在注释里点名，避免"改了 Go 忘了 SQL"这类半截状态。
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// ErrPackNotFound 目标包行不存在（调用方据此回 404，不与数据库错误混成 500）。
var ErrPackNotFound = errors.New("pack_not_found")

// CompareVersions 按"点分数字段"比较两个包版本号，返回 -1/0/1。
//
// 为什么不用第三方 semver 库：包版本号由打包工具（cmd/pack -version）写入，
// 词法就是 1.2.3 这种点分十进制，不需要 prerelease/build 元数据语义。
// 但**必须**按数值逐段比：字符串比较会把 1.10.0 判成小于 1.9.0，
// 于是"保留最高版本"的回填/择新逻辑会把真正的新版本下架掉——这个错法是静默的，
// 界面上只会表现为"升到 1.10 又退回 1.9 的内容"。
//
// 非数字段（脏数据 "v1beta"、空串）一律判最旧，且比较本身不报错：
// 择新逻辑宁可保守保留可解析的那一行，也不能被一条脏版本直接 panic 掉启动。
func CompareVersions(a, b string) int {
	as, bs := versionParts(a), versionParts(b)
	for i := 0; i < len(as) || i < len(bs); i++ {
		var av, bv int
		var aok, bok bool
		if i < len(as) {
			av, aok = as[i].num, as[i].ok
		}
		if i < len(bs) {
			bv, bok = bs[i].num, bs[i].ok
		}
		// 不可解析的一侧沉底；两侧都不可解析时按原始串字典序定序，保证比较是全序（择新结果唯一）
		if !aok || !bok {
			if !aok && !bok {
				if pa, pb := numOrEmpty(as, i), numOrEmpty(bs, i); pa != pb {
					return cmpStr(pa, pb)
				}
				continue
			}
			if !aok {
				return -1
			}
			return 1
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

// seg 一个版本段的解析结果
type seg struct {
	num int
	ok  bool
	raw string
}

// versionParts 按 "." 切段并逐段尝试转成整数
func versionParts(v string) []seg {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	raw := strings.Split(v, ".")
	out := make([]seg, 0, len(raw))
	for _, r := range raw {
		n, err := strconv.Atoi(strings.TrimSpace(r))
		out = append(out, seg{num: n, ok: err == nil, raw: strings.TrimSpace(r)})
	}
	return out
}

// numOrEmpty 取第 i 段的原始字面量（越界返回空串），仅用于脏版本之间的兜底定序
func numOrEmpty(s []seg, i int) string {
	if i >= len(s) {
		return ""
	}
	return s[i].raw
}

// cmpStr 字符串比较的三值返回（避免调用处写一串 if）
func cmpStr(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// NewerPackID 从同一 code 的多条包行里选出"应当上架的那一条"：
// 版本号最高者优先，版本号相同（或都不可解析）时按 id 大者（后注册）优先。
//
// 为什么 tie-break 用 id 而不是 created_at：种子行的 created_at 可以为空（现场 id=1355 就是），
// 时间字段在脏数据上不可靠；id 单调且非空，择新结果因此唯一确定。
// 返回 0 表示入参为空。
func NewerPackID(rows []model.IndustryPack) uint {
	var best *model.IndustryPack
	for i := range rows {
		r := &rows[i]
		if best == nil {
			best = r
			continue
		}
		if c := CompareVersions(r.Version, best.Version); c > 0 || (c == 0 && r.ID > best.ID) {
			best = r
		}
	}
	if best == nil {
		return 0
	}
	return best.ID
}

// ActivatePackExclusive 把 packID 上架，并**在同一事务内**把同 code 的其它上架行下架。
//
// 语句顺序是这条不变式的关键：先兄弟后自己。反过来的话，在"自己已 active、兄弟也 active"
// 的存量脏数据上会先删掉自己再被兄弟占位，中间态虽然在同一事务里不可见，
// 但一旦自己那行才是唯一 active（正常态），删除自己就成了"谁也没上架"的空档——
// 顺序写对时任何输入下都不会出现该空档（兄弟的更新不涉及自己）。
//
// 目标行不存在返回 ErrPackNotFound（调用方回 404，别把"包不存在"报成 500）。
// 返回值是**被本次操作下架的兄弟行**：调用方要把它写进审计与回执文案——
// 超管点"上架 1.2.0"之后 1.1.0 自己变成下架态，不说清楚就是"界面背着我改了别的东西"。
// 用 gorm 传入句柄而非包内 db.DB：调用方有的是请求 ctx（db.PQ），有的是启动期裸库。
func ActivatePackExclusive(gdb *gorm.DB, packID uint) ([]model.IndustryPack, error) {
	var demoted []model.IndustryPack
	err := gdb.Transaction(func(tx *gorm.DB) error {
		var target model.IndustryPack
		if err := tx.Where("id = ?", packID).First(&target).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPackNotFound
			}
			return err
		}
		// 1) 同 code 其它 active 行让位（幂等：没有兄弟行时 0 行受影响，不报错）。
		//    先查后改不是为了拼 SQL，是为了拿到"谁被下架了"这份清单回给调用方。
		if err := tx.Where("code = ? AND id <> ? AND status = ?", target.Code, target.ID, StatusActive).
			Find(&demoted).Error; err != nil {
			return err
		}
		if len(demoted) > 0 {
			ids := make([]uint, 0, len(demoted))
			for _, d := range demoted {
				ids = append(ids, d.ID)
			}
			if err := tx.Model(&model.IndustryPack{}).Where("id IN ?", ids).
				Update("status", StatusDisabled).Error; err != nil {
				return err
			}
		}
		// 2) 自己上架
		return tx.Model(&model.IndustryPack{}).Where("id = ?", target.ID).
			Update("status", StatusActive).Error
	})
	if err != nil {
		return nil, err
	}
	return demoted, nil
}

// 说明：这里**刻意没有**一个"全表收拢"函数。存量多版本并存由迁移 027 的 UPDATE 一次性收口
// （数据在库里，回填只能在 SQL 里做），此后每一次状态变更都只经过 ActivatePackExclusive，
// "该留哪一版"这条规则因此只有一个 Go 实现点（NewerPackID）加一处 SQL 实现点（迁移文件）。
// 若在启动期再写一遍全表收拢，就等于把同一条规则第三次落进代码——
// 三处各写一遍正是本次残项3（错误码文案双份镜像）要根治的形态，不该自己动手再造一个。

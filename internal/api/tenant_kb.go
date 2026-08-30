package api

// 租户自定义知识库API（P2双层KB）：企业自有资料上传(自动切片入库)、本租户片段查询与删除。
// 复用knowledge_fragments(category=企业知识,tenant盖章私有)，上传/删除后触发知识缓存Reload多实例广播。

// ============================================================
// 租户自定义知识库（P2 双层KB，2026-08-26）
//
// 双层语义：行业包 product_kb（平台资产）+ 租户上传（企业自有资料），
// 检索时租户层优先命中（见 service.SearchTenantKnowledge 的排序权重）。
//
// POST   /api/v1/admin/kb/upload  {title, content, category?} —— 自动切片入库
// GET    /api/v1/admin/kb/my      本租户已上传片段（分页）
// DELETE /api/v1/admin/kb/my/:id  删除本租户片段
//
// 存储：复用 knowledge_fragments（category=企业知识，tenant 盖章私有）；
// 上传后触发知识缓存 Reload（版本戳多实例广播）。
// ============================================================

import (
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"ai-scrm/internal/cache"
	"ai-scrm/internal/db"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"

	"github.com/gin-gonic/gin"
)

const (
	kbMaxContentRunes = 20000 // 单次上传正文上限
	kbMaxChunks       = 40    // 切片数上限
	kbChunkTarget     = 400   // 每片目标长度（rune）
)

// splitKBChunks 按空行段落聚合切片：段落到目标长度即成片，超长单段硬切
func splitKBChunks(content string) []string {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	paras := strings.FieldsFunc(content, func(r rune) bool { return r == '\n' || r == '\r' })
	var chunks []string
	cur := ""
	flush := func() {
		if strings.TrimSpace(cur) != "" {
			chunks = append(chunks, strings.TrimSpace(cur))
		}
		cur = ""
	}
	for _, p := range paras {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		rp := []rune(p)
		// 超长单段硬切
		for len(rp) > kbChunkTarget*2 {
			chunks = append(chunks, string(rp[:kbChunkTarget]))
			rp = rp[kbChunkTarget:]
		}
		p = string(rp)
		if len([]rune(cur))+len(rp)+1 > kbChunkTarget || len(chunks)+1 > kbMaxChunks {
			flush()
			if len(chunks) >= kbMaxChunks {
				break
			}
		}
		if cur == "" {
			cur = p
		} else {
			cur += "\n" + p
		}
	}
	flush()
	return chunks
}

// isHanOrWord 判断是否值得参与关键词的字符
func isHanOrWord(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// TenantKBUpload POST /api/v1/admin/kb/upload
func TenantKBUpload(c *gin.Context) {
	ti := middleware.GetTenantInfo(c)
	if ti.ID == 0 {
		RespErr(c, http.StatusForbidden, 403, "无租户语境")
		return
	}
	var req struct {
		Title    string `json:"title" binding:"required"`
		Content  string `json:"content" binding:"required"`
		Category string `json:"category"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误：title/content 必填")
		return
	}
	if len([]rune(req.Content)) > kbMaxContentRunes {
		RespErr(c, http.StatusBadRequest, 400, fmt.Sprintf("内容过长，上限 %d 字", kbMaxContentRunes))
		return
	}
	chunks := splitKBChunks(req.Content)
	if len(chunks) == 0 {
		RespErr(c, http.StatusBadRequest, 400, "内容为空")
		return
	}
	category := strings.TrimSpace(req.Category)
	if category == "" {
		category = "企业知识"
	}
	title := strings.TrimSpace(req.Title)

	var inserted int64
	for i, ch := range chunks {
		fragTitle := title
		if len(chunks) > 1 {
			fragTitle = fmt.Sprintf("%s (%d/%d)", title, i+1, len(chunks))
		}
		row := model.KnowledgeFragment{
			TenantID: ti.ID,
			Category: category,
			Title:    fragTitle,
			Content:  ch,
			Status:   1,
		}
		// P0-4 向量检索：上传即向量化（best-effort）。先落库拿 ID，再回写向量列
		if err := db.DB.Create(&row).Error; err != nil {
			RespErr(c, http.StatusInternalServerError, 500, fmt.Sprintf("第%d片写入失败", i+1))
			return
		}
		service.EmbedAndSetFragment(&row)
		inserted++
	}
	if cache.DefaultKnowledgeCache != nil {
		cache.DefaultKnowledgeCache.Reload() // 版本戳多实例广播
	}
	RespOK(c, fmt.Sprintf("上传成功：%s 共 %d 字，切成 %d 个知识片段", title, len([]rune(req.Content)), inserted), gin.H{"fragments": inserted})
}

// TenantKBMy GET /api/v1/admin/kb/my?page=&page_size=
func TenantKBMy(c *gin.Context) {
	ti := middleware.GetTenantInfo(c)
	if ti.ID == 0 {
		RespErr(c, http.StatusForbidden, 403, "无租户语境")
		return
	}
	page, pageSize := 1, 20
	fmt.Sscanf(c.DefaultQuery("page", "1"), "%d", &page)
	fmt.Sscanf(c.DefaultQuery("page_size", "20"), "%d", &pageSize)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	var total int64
	db.DB.Model(&model.KnowledgeFragment{}).
		Where("tenant_id = ? AND category = ?", ti.ID, "企业知识").Count(&total)
	var rows []model.KnowledgeFragment
	db.DB.Where("tenant_id = ? AND category = ?", ti.ID, "企业知识").
		Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows)
	RespOK(c, "", gin.H{
		"list": rows, "total": total, "page": page, "page_size": pageSize,
	})
}

// TenantKBDelete DELETE /api/v1/admin/kb/my/:id （仅本租户的"企业知识"类片段可删）
func TenantKBDelete(c *gin.Context) {
	ti := middleware.GetTenantInfo(c)
	if ti.ID == 0 {
		RespErr(c, http.StatusForbidden, 403, "无租户语境")
		return
	}
	res := db.DB.Where("id = ? AND tenant_id = ? AND category = ?",
		c.Param("id"), ti.ID, "企业知识").Delete(&model.KnowledgeFragment{})
	if res.Error != nil || res.RowsAffected == 0 {
		RespErr(c, http.StatusNotFound, 404, "片段不存在")
		return
	}
	if cache.DefaultKnowledgeCache != nil {
		cache.DefaultKnowledgeCache.Reload()
	}
	RespOK(c, "已删除", nil)
}

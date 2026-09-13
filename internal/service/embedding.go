// Package service 提供 SCRM 业务服务层实现（计费/消息/配置/脱敏/监控/向量等）。
package service

// ============================================================
// Embedding 客户端（P0-4 双层KB向量检索：防御式实现）
//
// 设计：可插拔 EmbeddingClient；默认 HTTP 实现兼容 OpenAI /v1/embeddings。
// 配置 EMBEDDING_API_URL + EMBEDDING_API_KEY + EMBEDDING_MODEL 即点亮向量检索；
// 未配置时 DefaultEmbeddingClient 为空，SearchTenantKnowledge 自动回退纯关键词检索，
// 不引入 pgvector 扩展依赖（避免运行期缺扩展导致 SQL 报错）。
// 向量存储于 knowledge_fragments.embedding_json（JSON 数组），检索时内存余弦混合打分。
// ============================================================

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-scrm/config"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"

	"gorm.io/gorm"
)

// EmbeddingClient 文本向量化接口（可替换实现：本地模型/远端服务）
// 可插拔设计，支持不同向量化后端
type EmbeddingClient interface {
	// Embed 将文本转换为向量表示，失败返回nil
	Embed(text string) []float32
}

// DefaultEmbeddingClient 全局向量化客户端；nil 表示未启用（回退关键词）
var DefaultEmbeddingClient EmbeddingClient

// httpEmbeddingClient OpenAI 兼容 /v1/embeddings 实现
// 通过HTTP调用远端向量化服务，支持OpenAI API格式
type httpEmbeddingClient struct {
	url   string       // 向量化服务端点URL
	token string       // API密钥
	model string       // 向量模型名（如 text-embedding-3-small）
	http  *http.Client // HTTP客户端（30s超时）
}

// embeddingRequest OpenAI 兼容 /v1/embeddings 请求体（输入文本 + 模型名）
type embeddingRequest struct {
	Input string `json:"input"` // 待向量化文本
	Model string `json:"model"` // 向量模型（如 text-embedding-3-small）
}

// embeddingResponse /v1/embeddings 响应体：data 含向量数组，error 含业务错误
type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"` // 单条文本向量
	} `json:"data"`
	Error *struct {
		Message string `json:"message"` // 业务错误描述（如模型不存在）
	} `json:"error"`
}

// InitEmbeddingClient 由配置装配；未配置则不启用（graceful）
// 读取 EMBEDDING_API_URL + EMBEDDING_API_KEY + EMBEDDING_MODEL 配置
// 未配置时 DefaultEmbeddingClient 为空，SearchTenantKnowledge 自动回退纯关键词检索
func InitEmbeddingClient() {
	cfg := config.GlobalConfig.AI
	// D3：即使 embedding 端点未配置，也尽量让 pgvector schema 就位；向量列存在但不写不会破坏主链路。
	EnsurePgvector()
	if cfg.EmbeddingURL == "" {
		DefaultEmbeddingClient = nil
		log.Println("[向量检索] 未配置 EMBEDDING_API_URL，回退纯关键词检索")
		return
	}
	DefaultEmbeddingClient = &httpEmbeddingClient{
		url:   cfg.EmbeddingURL,
		token: cfg.EmbeddingKey,
		model: cfg.EmbeddingModel,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
	log.Printf("[向量检索] 已启用，端点: %s model=%s", cfg.EmbeddingURL, cfg.EmbeddingModel)
	// 启动期回填：历史未向量化片段（embedding_json 空或 embedding IS NULL）批量补齐，使其也能走向量索引
	BackfillEmbeddings()
}

// BackfillEmbeddings 后台分批回填历史片段的向量列（embedding_json 空或 pgvector 列为空）
// 限流避免打爆 Embedding 端点（每条20ms间隔）；租户隔离按行天然成立（逐行更新）
// 失败单条跳过，整体不阻断
func BackfillEmbeddings() {
	if DefaultEmbeddingClient == nil || !kbVectorSearchEnabled() {
		return
	}
	go func() {
		defer func() { _ = recover() }()
		for {
			var batch []model.KnowledgeFragment
			q := db.DB.Where("COALESCE(embedding_json, '') = ''")
			if pgvectorEnabled {
				q = q.Or("embedding IS NULL")
			}
			q.Order("id ASC").Limit(50).Find(&batch)
			if len(batch) == 0 {
				log.Println("[向量检索] 历史向量回填完成")
				return
			}
			for i := range batch {
				f := batch[i]
				emb := DefaultEmbeddingClient.Embed(f.Title + "\n" + f.Content)
				if len(emb) == 0 {
					// 向量化失败：写零向量占位（toVectorLiteral 自动补零到配置维度），
					// 使其不再为 NULL，避免被下一轮重复选中导致死循环
					emb = []float32{}
				}
				if b, err := json.Marshal(emb); err == nil {
					if pgvectorEnabled {
						vec := toVectorLiteral(emb)
						if err := db.DB.Exec("UPDATE knowledge_fragments SET embedding_json = ?, embedding = ?::vector WHERE id = ?",
							string(b), vec, f.ID).Error; err != nil {
							log.Printf("[向量检索] 回填失败 id=%d: %v", f.ID, err)
						}
					} else {
						if err := db.DB.Exec("UPDATE knowledge_fragments SET embedding_json = ? WHERE id = ?",
							string(b), f.ID).Error; err != nil {
							log.Printf("[向量检索] 回填失败 id=%d: %v", f.ID, err)
						}
					}
				}
				time.Sleep(20 * time.Millisecond) // 限流
			}
		}
	}()
}

// pgvectorEnabled 标记 pgvector 扩展是否就绪（决定 KB 检索走 SQL 向量索引还是 Go 内余弦）
var pgvectorEnabled bool

// PgvectorEnabled 对外暴露 pgvector 就绪状态（API 列表/诊断用）
func PgvectorEnabled() bool {
	return pgvectorEnabled
}

// kbVectorSearchEnabled 读取 D3 热开关；单测或未初始化配置时默认开，保持 fail-open 到可用向量路径。
func kbVectorSearchEnabled() bool {
	return SafeCfgBool("kb_vector_search", true)
}

// EnsurePgvector 幂等启用 pgvector：建扩展 + knowledge_fragments.embedding 定长列 + HNSW 索引
// 任意步骤失败均静默降级（pgvectorEnabled=false → 检索回退关键词+内存余弦，不影响主链路）
func EnsurePgvector() {
	if db.DB == nil {
		return
	}
	pgvectorEnabled = false
	if err := db.DB.Exec("CREATE EXTENSION IF NOT EXISTS vector").Error; err != nil {
		log.Printf("[向量检索] pgvector 扩展不可用（回退关键词+内存余弦）: %v", err)
		return
	}
	dim := config.GlobalConfig.AI.EmbeddingDim
	if dim <= 0 {
		dim = 1536
	}
	// 维度不一致时宁可回退旧路径，不能让 toVectorLiteral 往错误维度的列里写，否则拖垮检索/回填。
	var colType string
	db.DB.Raw(`
		SELECT coalesce(format_type(a.atttypid, a.atttypmod), '')
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		WHERE c.relname = 'knowledge_fragments'
		  AND a.attname = 'embedding'
		  AND a.attnum > 0`).Scan(&colType)
	if colType != "" {
		var got int
		if !strings.HasPrefix(colType, "vector(") {
			log.Printf("[向量检索] embedding 列类型不是 vector（%s），回退关键词+内存余弦", colType)
			return
		}
		if _, err := fmt.Sscanf(colType, "vector(%d)", &got); err != nil || got != dim {
			log.Printf("[向量检索] embedding 列维度 %d 与 EMBEDDING_DIM %d 不一致，回退关键词+内存余弦", got, dim)
			return
		}
	}
	if err := db.DB.Exec(fmt.Sprintf("ALTER TABLE knowledge_fragments ADD COLUMN IF NOT EXISTS embedding vector(%d)", dim)).Error; err != nil {
		log.Printf("[向量检索] 添加 embedding 列失败（回退）: %v", err)
		return
	}
	// HNSW 索引（pgvector≥0.5 支持；失败忽略=退化为顺序扫描，结果仍正确）
	if err := db.DB.Exec("CREATE INDEX IF NOT EXISTS idx_kf_embedding ON knowledge_fragments USING hnsw (embedding vector_cosine_ops)").Error; err != nil {
		log.Printf("[向量检索] HNSW 索引创建跳过（将顺序扫描）: %v", err)
	}
	pgvectorEnabled = true
	log.Printf("[向量检索] pgvector 已启用（dim=%d），KB 检索走 SQL 向量索引", dim)
}

// FillFragmentVectorStatus 给管理端补"是否已向量化"状态，避免把大 embedding_json 当状态列返回。
// 说明：gdb 由调用方传入（通常带租户 scope），这里仅按 ID 回读状态，不扩大可见范围。
func FillFragmentVectorStatus(gdb *gorm.DB, frags []model.KnowledgeFragment) {
	if gdb == nil || len(frags) == 0 {
		return
	}
	ids := make([]uint, 0, len(frags))
	for _, f := range frags {
		if f.ID != 0 {
			ids = append(ids, f.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	type row struct {
		ID    uint
		Ready bool
	}
	var rows []row
	if pgvectorEnabled {
		gdb.Raw(`SELECT id, (COALESCE(embedding_json, '') <> '' OR embedding IS NOT NULL) AS ready FROM knowledge_fragments WHERE id IN ?`, ids).Scan(&rows)
	} else {
		gdb.Raw(`SELECT id, COALESCE(embedding_json, '') <> '' AS ready FROM knowledge_fragments WHERE id IN ?`, ids).Scan(&rows)
	}
	m := make(map[uint]bool, len(rows))
	for _, r := range rows {
		m[r.ID] = r.Ready
	}
	for i := range frags {
		frags[i].Vectorized = m[frags[i].ID]
	}
}

// toVectorLiteral 将向量格式化为 pgvector 文本字面量 [..]，并按配置维度裁剪/补零对齐
// 用于SQL插入/更新向量列
func toVectorLiteral(emb []float32) string {
	dim := 1536
	if config.GlobalConfig != nil && config.GlobalConfig.AI.EmbeddingDim > 0 {
		dim = config.GlobalConfig.AI.EmbeddingDim
	}
	if len(emb) > dim {
		emb = emb[:dim]
	} else if len(emb) < dim {
		e := make([]float32, dim)
		copy(e, emb)
		emb = e
	}
	var sb strings.Builder
	sb.WriteByte('[')
	for i, v := range emb {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.FormatFloat(float64(v), 'f', -1, 32))
	}
	sb.WriteByte(']')
	return sb.String()
}

// Embed 向量化（best-effort：失败返回 nil，调用方回退关键词）
// 调用OpenAI兼容的/v1/embeddings接口，失败时记录日志并返回nil
func (c *httpEmbeddingClient) Embed(text string) []float32 {
	if text == "" {
		return nil
	}
	raw, _ := json.Marshal(embeddingRequest{Input: text, Model: c.model})
	req, err := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		log.Printf("[向量检索] 请求失败: %v", err)
		return nil
	}
	defer resp.Body.Close()
	var er embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil
	}
	if er.Error != nil && er.Error.Message != "" {
		log.Printf("[向量检索] 业务错误: %s", er.Error.Message)
		return nil
	}
	if len(er.Data) == 0 {
		return nil
	}
	return er.Data[0].Embedding
}

// EmbedAndSetFragment 为知识片段生成 embedding 并写入字段（best-effort，失败留空）
// 上传/更新知识片段时调用：标题+内容联合向量化；未启用向量客户端则不动
// 片段须已落库（frag.ID>0）以便回写向量列；pgvector 就绪时同步写 embedding 向量列，
// 否则仅写 embedding_json 供 Go 内余弦回退
func EmbedAndSetFragment(frag *model.KnowledgeFragment) {
	if DefaultEmbeddingClient == nil {
		return
	}
	if frag.ID == 0 {
		// 未落库无法回写向量列：仍暂存 JSON，由调用方落库后重跑
		emb := DefaultEmbeddingClient.Embed(frag.Title + "\n" + frag.Content)
		if len(emb) > 0 {
			if b, err := json.Marshal(emb); err == nil {
				frag.EmbeddingJSON = string(b)
			}
		}
		return
	}
	emb := DefaultEmbeddingClient.Embed(frag.Title + "\n" + frag.Content)
	if len(emb) == 0 {
		return
	}
	if b, err := json.Marshal(emb); err == nil {
		frag.EmbeddingJSON = string(b)
	}
	// 回写：embedding_json（Go 余弦回退） + embedding 向量列（pgvector 索引检索）
	if pgvectorEnabled {
		vec := toVectorLiteral(emb)
		if err := db.DB.Exec("UPDATE knowledge_fragments SET embedding_json = ?, embedding = ?::vector WHERE id = ?",
			frag.EmbeddingJSON, vec, frag.ID).Error; err != nil {
			log.Printf("[向量检索] 回写向量列失败: %v", err)
		}
	} else if frag.EmbeddingJSON != "" {
		_ = db.DB.Exec("UPDATE knowledge_fragments SET embedding_json = ? WHERE id = ?", frag.EmbeddingJSON, frag.ID).Error
	}
}

// cosineSimilarity 余弦相似度（维度不一致返回0）
// 用于向量检索时计算查询向量与文档向量的相似度
func cosineSimilarity(a, b []float32) float32 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float32
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	na = sqrt32(na)
	nb = sqrt32(nb)
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (na * nb)
}

// sqrt32 单精度平方根（Newton 4 次迭代，相似度用途足够）
// 避免引入math依赖，简单实现满足需求
func sqrt32(x float32) float32 {
	if x <= 0 {
		return 0
	}
	guess := x
	for i := 0; i < 4; i++ {
		guess = 0.5 * (guess + x/guess)
	}
	return guess
}

// embeddingFromJSON 解析片段存储的 embedding
// 从JSON数组格式的字符串解析为[]float32向量
func embeddingFromJSON(raw string) []float32 {
	if raw == "" {
		return nil
	}
	var v []float32
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil
	}
	return v
}

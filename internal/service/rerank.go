// Package service 业务服务层：E9 KB 检索重排（rerank）客户端与召回后二段排序。
//
// 设计对齐 embedding.go 的可插拔纪律：配置 RERANK_API_URL 即点亮，未配置/开关关/
// AI_MOCK_MODE/调用失败 一律 fail-open 回退原序（只改候选集内的展示顺序，不改召回集合）。
// 供应商侧与 embedding 同 Key 体系（硅基流动 /v1/rerank，bge-reranker 交叉编码器）。
package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"time"

	"ai-scrm/config"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
)

// RerankClient 交叉编码器重排接口（可替换实现：远端 API/本地模型）
type RerankClient interface {
	// Rerank 按 query 对 docs 打相关性分；返回与入参 docs 等长的 (原始下标, 分数) 列表（未排序，调用方定序）
	Rerank(query string, docs []string) ([]RerankItem, error)
}

// RerankItem 单条文档的重排结果：index 指向 docs 原始下标，score 为相关性分（越大越相关）
type RerankItem struct {
	Index int
	Score float64
}

// DefaultRerankClient 全局重排客户端；nil 表示未启用（检索回退原序）
var DefaultRerankClient RerankClient

// httpRerankClient 兼容硅基流动 /v1/rerank 协议的 HTTP 实现
type httpRerankClient struct {
	url   string       // Rerank 端点
	token string       // Bearer 鉴权
	model string       // 重排模型名
	http  *http.Client // 5s 超时——KB 检索在 AI 生成前串行执行，重排吃不得长尾时延
}

// rerankRequest /v1/rerank 请求体（OpenAI 兼容协议族：query + documents + top_n）
type rerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n"`
}

// rerankResponse /v1/rerank 响应体：results[].index 回指 documents 下标
type rerankResponse struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// InitRerankClient 由配置装配重排客户端；RERANK_API_URL 为空则不启用（graceful，同 embedding）。
// Key 未单独配置时复用 EmbeddingKey——同供应商不同端点共用凭证是常态。
func InitRerankClient() {
	cfg := config.GlobalConfig.AI
	if cfg.RerankURL == "" {
		DefaultRerankClient = nil
		return
	}
	key := cfg.RerankKey
	if key == "" {
		key = cfg.EmbeddingKey
	}
	DefaultRerankClient = &httpRerankClient{
		url:   cfg.RerankURL,
		token: key,
		model: cfg.RerankModel,
		http:  &http.Client{Timeout: 5 * time.Second},
	}
	log.Printf("[KB重排] 已启用，端点: %s model=%s（热开关 kb_rerank 默认关）", cfg.RerankURL, cfg.RerankModel)
}

// Rerank 调远端重排端点；任何失败返回 error，由 applyRerank fail-open
func (c *httpRerankClient) Rerank(query string, docs []string) ([]RerankItem, error) {
	raw, err := json.Marshal(rerankRequest{Model: c.model, Query: query, Documents: docs, TopN: len(docs)})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var rr rerankResponse
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return nil, fmt.Errorf("rerank 响应解析失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := ""
		if rr.Error != nil {
			msg = rr.Error.Message
		}
		return nil, fmt.Errorf("rerank HTTP %d: %s", resp.StatusCode, msg)
	}
	if len(rr.Results) == 0 {
		return nil, fmt.Errorf("rerank 返回空结果")
	}
	out := make([]RerankItem, 0, len(rr.Results))
	for _, r := range rr.Results {
		// 防御远端越界下标：坏数据整批作废走回退，而非 panic 或错序
		if r.Index < 0 || r.Index >= len(docs) {
			return nil, fmt.Errorf("rerank 下标越界: %d", r.Index)
		}
		out = append(out, RerankItem{Index: r.Index, Score: r.RelevanceScore})
	}
	return out, nil
}

// kbRerankEnabled E9 热开关：默认关（配了端点也要显式打开才放量），未初始化配置时按关处理
func kbRerankEnabled() bool {
	return runtimecfg.SafeCfgBool("kb_rerank", false)
}

// kbRerankCandidates 送重排的 top-N 候选上限（键缺失/非法时回默认 8）
func kbRerankCandidates() int {
	n := runtimecfg.SafeCfgInt("kb_rerank_candidates", 8)
	if n < 2 {
		n = 2
	}
	if n > 32 {
		n = 32 // 硬上限：候选越多时延与配额开销越大，防误配拖慢检索
	}
	return n
}

// applyRerank E9 二段重排：对已按混合分数排序的候选前 N 个送 rerank 重排。
// 只改前 N 内部的顺序，不改召回集合、不动第 N 名之后的尾部（fail-open 纪律）。
// 关闭/未启用/MockMode/单候选/任何调用失败 → 原序返回（零行为差）。
func applyRerank(query string, frags []model.KnowledgeFragment) []model.KnowledgeFragment {
	if DefaultRerankClient == nil || !kbRerankEnabled() {
		return frags
	}
	if config.GlobalConfig != nil && config.GlobalConfig.AI.MockMode {
		return frags // 测试/模拟环境不打真实重排端点，保证回归零外部依赖
	}
	cand := kbRerankCandidates()
	if cand > len(frags) {
		cand = len(frags)
	}
	if cand < 2 {
		return frags
	}
	docs := make([]string, cand)
	for i := 0; i < cand; i++ {
		docs[i] = frags[i].Title + "\n" + frags[i].Content
	}
	items, err := DefaultRerankClient.Rerank(query, docs)
	if err != nil {
		log.Printf("[KB重排] 回退原序: %v", err)
		return frags
	}
	// 稳定排序：同分保持原混合分序（远端并列时不引入随机抖序）
	sort.SliceStable(items, func(i, j int) bool { return items[i].Score > items[j].Score })
	head := make([]model.KnowledgeFragment, 0, cand)
	seen := make([]bool, cand)
	for _, it := range items {
		head = append(head, frags[it.Index])
		seen[it.Index] = true
	}
	// 远端少返回（如自作主张截断 top_n）：漏掉的候选按原序补在重排段尾，绝不静默丢片段
	for i := 0; i < cand; i++ {
		if !seen[i] {
			head = append(head, frags[i])
		}
	}
	out := append(head, frags[cand:]...)
	return out
}

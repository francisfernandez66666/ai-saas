// Package channel 通道适配层（W 批次，2026-09-12）——企微自建应用/微信客服/微信公众号。
// 分包红线：新增领域代码进本包，不再进 internal/service。
// 设计：入站统一转 ChatRequest 进现有三层分流/合并队列（复用处理链，延迟铁律天然生效），
// AI 回复统一经 strategy.GenerateReply（红线不破），出站按会话来源路由回对应通道适配器。
package channel

import (
	"errors"
	"fmt"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/pkg/crypto"
)

// 通道相关错误（对外映射到明确 HTTP 码，避免 500 裸奔）
var (
	ErrChannelNotFound = errors.New("通道不存在或无权访问")
	ErrChannelType     = errors.New("不支持的通道类型")
	ErrCredentialRekey = errors.New("凭据解密失败，需重新录入（密钥可能已轮换）")
)

// Credential 解密后的通道凭据（仅适配器内部使用，禁止序列化到接口响应）
type Credential struct {
	ChannelID uint // token 缓存分桶键
	Type      string
	CorpID    string
	AppID     string
	AgentID   string // 企微应用 agentid（message/send 必填，从 config_json 或 AppID 复用）
	Secret    string // 应用/公众号 secret
	Token     string // 回调 Token
	Encoding  string // EncodingAESKey（43 位）
	BaseURL   string // API 根（mock 注入；空=官方默认）
}

// ReceiveID 加解密 receive_id 校验值：企微用 corpid，公众号用 appid。
func (c *Credential) ReceiveID() string {
	if c.Type == model.ChannelTypeWechatMP {
		return c.AppID
	}
	return c.CorpID
}

// CreateInput 创建/更新通道入参（明文凭据，服务端加密落库）
type CreateInput struct {
	TenantID     uint
	Type         string
	Name         string
	CorpID       string
	AppID        string
	Secret       string // 明文（入参），落库转 secret_cipher
	Token        string
	Encoding     string // EncodingAESKey
	DepartmentID uint
	ConfigJSON   string
}

// validType 通道类型白名单
func validType(t string) bool {
	switch t {
	case model.ChannelTypeWecomApp, model.ChannelTypeWecomKf, model.ChannelTypeWechatMP:
		return true
	}
	return false
}

// Create 新建通道：加密凭据落库，返回明文一次性回显（前端展示后不可再取明文）。
func Create(in CreateInput) (*model.Channel, error) {
	if !validType(in.Type) {
		return nil, ErrChannelType
	}
	secretC, err := crypto.Encrypt(in.Secret)
	if err != nil {
		return nil, err
	}
	tokenC, err := crypto.Encrypt(in.Token)
	if err != nil {
		return nil, err
	}
	aesC, err := crypto.Encrypt(in.Encoding)
	if err != nil {
		return nil, err
	}
	ch := model.Channel{
		TenantID:     in.TenantID,
		Type:         in.Type,
		Name:         in.Name,
		CorpID:       in.CorpID,
		AppID:        in.AppID,
		SecretCipher: secretC,
		TokenCipher:  tokenC,
		AesKeyCipher: aesC,
		Status:       model.ChannelStatusUnverified,
		DepartmentID: in.DepartmentID,
		ConfigJSON:   in.ConfigJSON,
	}
	if err := db.DB.Create(&ch).Error; err != nil {
		return nil, fmt.Errorf("创建通道失败: %w", err)
	}
	return &ch, nil
}

// List 列出租户下通道（凭据永不下发明文，敏感字段已因 json:"-" 不出接口）。
func List(tenantID uint) ([]model.Channel, error) {
	var list []model.Channel
	err := db.DB.Where("tenant_id = ? AND deleted_at IS NULL", tenantID).Order("id ASC").Find(&list).Error
	return list, err
}

// Get 取单通道（含密文，供解密/更新用；不直接出接口）
func Get(tenantID, id uint) (*model.Channel, error) {
	var ch model.Channel
	if err := db.DB.Where("id = ? AND tenant_id = ? AND deleted_at IS NULL", id, tenantID).First(&ch).Error; err != nil {
		return nil, ErrChannelNotFound
	}
	return &ch, nil
}

// GetByID 回调专用：按主键取通道（不带租户上下文，安全靠签名验证保证；软删的视为不存在）。
func GetByID(id uint) (*model.Channel, error) {
	var ch model.Channel
	if err := db.DB.Where("id = ? AND deleted_at IS NULL", id).First(&ch).Error; err != nil {
		return nil, ErrChannelNotFound
	}
	return &ch, nil
}

// DecryptCredential 解密通道凭据（适配器收发用）。解不开→ErrCredentialRekey（引导重录，不 500）。
func DecryptCredential(ch *model.Channel) (*Credential, error) {
	secret, err := crypto.Decrypt(ch.SecretCipher)
	if err != nil {
		return nil, ErrCredentialRekey
	}
	token, err := crypto.Decrypt(ch.TokenCipher)
	if err != nil {
		return nil, ErrCredentialRekey
	}
	enc, err := crypto.Decrypt(ch.AesKeyCipher)
	if err != nil {
		return nil, ErrCredentialRekey
	}
	return &Credential{
		ChannelID: ch.ID,
		Type:      ch.Type,
		CorpID:    ch.CorpID,
		AppID:     ch.AppID,
		AgentID:   cfgStr(ch.ConfigJSON, "agentid"),
		Secret:    secret,
		Token:     token,
		Encoding:  enc,
		BaseURL:   resolveBaseURL(ch.ConfigJSON),
	}, nil
}

// UpdateInput 更新入参：凭据字段留空表示"不改动"（沿用旧密文），非空才重新加密覆盖。
type UpdateInput struct {
	Name         string
	CorpID       string
	AppID        string
	Secret       string // 非空→重加密
	Token        string
	Encoding     string
	Status       string
	DepartmentID *uint
	ConfigJSON   *string
}

// Update 更新通道（凭据按"是否传新值"决定是否重加密，避免编辑基础信息时误清凭据）。
func Update(tenantID, id uint, in UpdateInput) (*model.Channel, error) {
	ch, err := Get(tenantID, id)
	if err != nil {
		return nil, err
	}
	upd := map[string]interface{}{}
	if in.Name != "" {
		upd["name"] = in.Name
	}
	if in.CorpID != "" {
		upd["corpid"] = in.CorpID
	}
	if in.AppID != "" {
		upd["appid"] = in.AppID
	}
	if in.Secret != "" {
		c, err := crypto.Encrypt(in.Secret)
		if err != nil {
			return nil, err
		}
		upd["secret_cipher"] = c
	}
	if in.Token != "" {
		c, err := crypto.Encrypt(in.Token)
		if err != nil {
			return nil, err
		}
		upd["token_cipher"] = c
	}
	if in.Encoding != "" {
		c, err := crypto.Encrypt(in.Encoding)
		if err != nil {
			return nil, err
		}
		upd["aeskey_cipher"] = c
	}
	if in.Status != "" {
		upd["status"] = in.Status
	}
	if in.DepartmentID != nil {
		upd["department_id"] = *in.DepartmentID
	}
	if in.ConfigJSON != nil {
		upd["config_json"] = *in.ConfigJSON
	}
	if len(upd) > 0 {
		if err := db.DB.Model(ch).Updates(upd).Error; err != nil {
			return nil, err
		}
		// D6 修复(2026-09-14)：凭据类字段变更即失效 access_token 缓存——
		// 旧实现在换 secret/corpid 后仍复用旧 token 直到临期，新凭据不生效导致发送持续失败。
		if _, hit := upd["secret_cipher"]; hit {
			defaultTokenManager.Invalidate(id)
		}
		if _, hit := upd["corpid"]; hit {
			defaultTokenManager.Invalidate(id)
		}
		if _, hit := upd["appid"]; hit {
			defaultTokenManager.Invalidate(id)
		}
	}
	return Get(tenantID, id)
}

// Delete 软删通道（保留审计）
func Delete(tenantID, id uint) error {
	return db.DB.Model(&model.Channel{}).
		Where("id = ? AND tenant_id = ?", id, tenantID).Update("deleted_at", time.Now()).Error
}

// FindActiveByCallback 入站路由：按 (type, corpid/appid) 定位启用中的通道（回调不带 JWT，靠标识匹配）。
func FindActiveByCallback(typ, id string) (*model.Channel, error) {
	var ch model.Channel
	col := "corpid"
	if typ == model.ChannelTypeWechatMP {
		col = "appid"
	}
	err := db.DB.Where("type = ? AND "+col+" = ? AND status = ? AND deleted_at IS NULL", typ, id, model.ChannelStatusActive).
		First(&ch).Error
	if err != nil {
		return nil, ErrChannelNotFound
	}
	return &ch, nil
}

-- 003 通道接入表（W2，2026-09-12）
-- 企微自建应用/微信客服/微信公众号 三类通道的配置与凭据（凭据列为 AES-256-GCM 密文，见 pkg/crypto）。
-- 与 GORM AutoMigrate 幂等共存（IF NOT EXISTS）；本文件为生产迁移真源与版本记录。
BEGIN;

CREATE TABLE IF NOT EXISTS channels (
    id             BIGSERIAL PRIMARY KEY,
    tenant_id      BIGINT NOT NULL,
    type           VARCHAR(20) NOT NULL,          -- wecom_app|wecom_kf|wechat_mp
    name           VARCHAR(100) NOT NULL,
    corpid         VARCHAR(64),                    -- 非机密，明文存
    appid          VARCHAR(64),                    -- 非机密，明文存
    secret_cipher  VARCHAR(512),                   -- 应用/公众号 secret 密文
    token_cipher   VARCHAR(512),                   -- 回调 Token 密文
    aeskey_cipher  VARCHAR(512),                   -- EncodingAESKey 密文
    status         VARCHAR(20) DEFAULT 'unverified',
    department_id  BIGINT,
    config_json    TEXT,
    created_at     TIMESTAMPTZ,
    updated_at     TIMESTAMPTZ,
    deleted_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_channels_tenant ON channels (tenant_id);
CREATE INDEX IF NOT EXISTS idx_channels_type ON channels (type);
CREATE INDEX IF NOT EXISTS idx_channels_corpid ON channels (corpid);
CREATE INDEX IF NOT EXISTS idx_channels_appid ON channels (appid);
CREATE INDEX IF NOT EXISTS idx_channels_department ON channels (department_id);
CREATE INDEX IF NOT EXISTS idx_channels_deleted_at ON channels (deleted_at);

COMMIT;

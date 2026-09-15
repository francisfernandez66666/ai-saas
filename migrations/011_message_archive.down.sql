-- 回滚：归档表整体移除（回滚前请先人工把 messages_archive 数据搬回 messages，防丢数）
DROP TABLE IF EXISTS messages_archive;

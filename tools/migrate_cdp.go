//go:build ignore

// 说明：本文件为 Phase P4（CDP 真实化）的半成品迁移工具，暂无法编译。
// 加 build ignore 标签排除出常规构建，P4 时重写后移除该标签。
package main

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	_ "github.com/mattn/go-sqlite3"
)

// MigrateCDPData 迁移现有数据到CDP表
func MigrateCDPData() error {
	log.Println("开始CDP数据迁移...")

	// 1. 迁移客户 → cdp_profiles
	if err := migrateCustomers(); err != nil {
		return fmt.Errorf("迁移客户失败: %w", err)
	}
	log.Println("  ✓ 客户迁移完成")

	// 2. 迁移标签 → cdp_tag_definitions
	if err := migrateTags(); err != nil {
		return fmt.Errorf("迁移标签失败: %w", err)
	}
	log.Println("  ✓ 标签迁移完成")

	// 3. 迁移消息 → event_logs
	if err := migrateMessages(); err != nil {
		return fmt.Errorf("迁移消息失败: %w", err)
	}
	log.Println("  ✓ 消息迁移完成")

	// 4. 迁移客户标签 → cdp_tag_assignments
	if err := migrateCustomerTags(); err != nil {
		return fmt.Errorf("迁移客户标签失败: %w", err)
	}
	log.Println("  ✓ 客户标签迁移完成")

	log.Println("CDP数据迁移完成!")
	return nil
}

// migrateCustomers 迁移 customers → cdp_profiles
func migrateCustomers() error {
	db := db.DB

	// 查询所有客户
	rows, err := db.Query("SELECT id, name, phone, wechat_id, gender, age, region, city, career, customer_type, interest_model, budget, decision_cycle, store_visited, trust_level, intent_score, price_sensitivity, brand_awareness, resistance_type, journey_stage, tags, t_vector, created_at, updated_at FROM customers")
	if err != nil {
		return err
	}
	defer rows.Close()

	// 开始事务
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	count := 0
	for rows.Next() {
		var id, customerID sql.NullInt64
		var name, phone, wechatID, interestModel, journeyStage, tagsStr, tVectorStr sql.NullString
		var gender, storeVisited, decisionCycle sql.NullInt64
		var budget, carAge, trustLevel, intentScore, priceSensitivity, brandAwareness sql.NullFloat64
		var createdAt, updatedAt sql.NullTime

		err := rows.Scan(&id, &name, &phone, &wechatID, &gender, &age, &region, &city, &career, &customerType, &interestModel, &budget, &decisionCycle, &storeVisited, &trustLevel, &intentScore, &priceSensitivity, &brandAwareness, &resistanceType, &journeyStage, &tagsStr, &tVectorStr, &createdAt, &updatedAt)
		if err != nil {
			log.Printf("扫描客户 %d 失败: %v", id.Int64, err)
			continue
		}

		customerIDVal := id.Int64
		if customerIDVal <= 0 {
			continue
		}

		// 解析 tags
		tagNames := []string{}
		if tagsStr.Valid && tagsStr.String != "" {
			tagNames = strings.Split(tagsStr.String, ",")
		}

		// 解析 t_vector (32位浮点数数组)
		var intentVector [32]float64
		if tVectorStr.Valid && tVectorStr.String != "" {
			// 简单解析，实际生产环境应使用JSON解析
			fields := strings.Fields(tVectorStr.String)
			for i, f := range fields {
				if i < 32 {
					var val float64
					fmt.Sscanf(f, "%f", &val)
					intentVector[i] = val
				}
			}
		}

		// 创建CDP配置文件
		profile := &model.CdpProfile{
			CustomerID:     int(customerIDVal),
			TenantID:       0,
			CDPID:          fmt.Sprintf("cdm-%d", customerIDVal),
			ProfileName:    name,
			Status:         1,
			ProfileData:    fmt.Sprintf("name:%s,phone:%s,gender:%d,age:%d,region:%s", name, phone, gender, age),
			CreatedAt:      createdAt.Time,
			UpdatedAt:      updatedAt.Time,
			VisitCount:     0,
			TestDriveCount: 0,
			MessageCount:   0,
			IntentScore:    intentScore.Float64,
		}

		// 如果有意向分，根据分数设置初始标签
		if intentScore.Float64 >= 1.5 {
			profile.TagIDs = append(profile.TagIDs, 1) // TagHotLead
		} else if intentScore.Float64 >= 0.5 {
			profile.TagIDs = append(profile.TagIDs, 2) // TagWarmLead
		}

		_, err := tx.Exec(`INSERT INTO cdp_profiles (tenant_id, customer_id, cdp_id, profile_name, status, profile_data, created_at, updated_at, visit_count, test_drive_count, message_count, intent_score) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			profile.TenantID, profile.CustomerID, profile.CDPID, profile.ProfileName, profile.Status, profile.ProfileData, profile.CreatedAt, profile.UpdatedAt, profile.VisitCount, profile.TestDriveCount, profile.MessageCount, profile.IntentScore)
		if err != nil {
			log.Printf("迁移客户 %d 失败: %v", customerIDVal, err)
			continue
		}

		count++
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	log.Printf("  迁移 %d 条客户记录", count)
	return nil
}

// migrateTags 迁移 tags → cdp_tag_definitions
func migrateTags() error {
	db := db.DB

	// 查询所有标签
	rows, err := db.Query("SELECT id, name, code, category, weight, description, status, created_at, updated_at FROM tags")
	if err != nil {
		return err
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var id sql.NullInt64
		var name, code, category, description sql.NullString
		var weight sql.NullFloat64
		var status sql.NullInt64
		var createdAt, updatedAt sql.NullTime

		err := rows.Scan(&id, &name, &code, &category, &weight, &description, &status, &createdAt, &updatedAt)
		if err != nil {
			log.Printf("扫描标签失败: %v", err)
			continue
		}

		if !name.Valid || !name.String {
			continue
		}

		// 确保code不为空
		if !code.Valid {
			code.String = name.String
			code.Valid = true
		}

		_, err := db.Exec(`INSERT INTO cdp_tag_definitions (tenant_id, code, name, category, weight_default, is_active, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			0, code.String, name.String, category.String, weight.Float64, status.Int64, createdAt.Time, updatedAt.Time)
		if err != nil {
			log.Printf("迁移标签 %d 失败: %v", id.Int64, err)
			continue
		}

		count++
	}

	log.Printf("  迁移 %d 条标签记录", count)
	return nil
}

// migrateMessages 迁移 messages → event_logs
func migrateMessages() error {
	db := db.DB

	// 查询所有消息
	rows, err := db.Query("SELECT id, conversation_id, customer_id, sender_type, sender_id, content, message_type, anchor_type, template_id, route_result, intent_score, hooked, emotion, metadata, created_at, updated_at FROM messages")
	if err != nil {
		return err
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var id, conversationID, customerID, senderID sql.NullInt64
		var senderType, messageType, anchorTypeStr, templateID, routeResult, emotion, metadata sql.NullString
		var intentScore sql.NullFloat64
		var hooked sql.NullFloat64

		err := rows.Scan(&id, &conversationID, &customerID, &senderType, &senderID, &content, &messageType, &anchorTypeStr, &templateID, &routeResult, &intentScore, &hooked, &emotion, &metadata, &createdAt, &updatedAt)
		if err != nil {
			log.Printf("扫描消息 %d 失败: %v", id.Int64, err)
			continue
		}

		if !customerID.Valid || customerID.Int64 <= 0 {
			continue
		}

		// 确定事件类型
		eventType := "message"
		if senderType.Valid && senderType.String == "human" {
			eventType = "human_reply"
		} else if senderType.Valid && senderType.String == "ai" {
			eventType = "ai_reply"
		}

		// 确定来源
		source := "scrm"
		if routeResult == RoutePrice {
			source = "price_routing"
		}

		// 解析anchorType
		anchorType := "unknown"
		if anchorTypeStr.Valid {
			switch anchorTypeStr.String {
			case "0":
				anchorType = "none"
			case "1":
				anchorType = "price"
			case "2":
				anchorType = "spec"
			case "3":
				anchorType = "service"
			case "4":
				anchorType = "brand"
			}
		}

		_, err := db.Exec(`INSERT INTO event_logs (tenant_id, customer_id, event_type, event_key, event_value, source, related_id, related_type, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			0, customerID.Int64, eventType, content.String, content.String, source, id.Int64, "message", createdAt.Time)
		if err != nil {
			log.Printf("迁移消息 %d 失败: %v", id.Int64, err)
			continue
		}

		count++
	}

	log.Printf("  迁移 %d 条消息记录", count)
	return nil
}

// migrateCustomerTags 迁移客户标签关系
func migrateCustomerTags() error {
	db := db.DB

	// 查询 customer_tags 关联表
	rows, err := db.Query("SELECT customer_id, tag_id FROM customer_tags")
	if err != nil {
		return err
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var customerID, tagID sql.NullInt64

		err := rows.Scan(&customerID, &tagID)
		if err != nil {
			log.Printf("扫描客户标签关联失败: %v", err)
			continue
		}

		if !customerID.Valid || customerID.Int64 <= 0 || !tagID.Valid || tagID.Int64 <= 0 {
			continue
		}

		_, err = db.Exec(`INSERT INTO cdp_tag_assignments (tenant_id, cdp_profile_id, definition_id, tag_value, assigned_at) VALUES (?, ?, ?, ?, ?)`,
			0, customerID.Int64, tagID.Int64, "migrated", time.Now())
		if err != nil {
			log.Printf("迁移客户标签 %d-%d 失败: %v", customerID.Int64, tagID.Int64, err)
			continue
		}

		count++
	}

	log.Printf("  迁移 %d 条客户标签关联记录", count)
	return nil
}

// RoutePrice 变量定义（自动补注释）。
const RoutePrice = "RoutePrice"

// main 程序入口（自动补注释，原为缺注释的顶层声明）。
func main() {
	// 确保数据库已初始化
	if err := db.Init(); err != nil {
		log.Fatalf("数据库初始化失败: %v", err)
	}

	if err := MigrateCDPData(); err != nil {
		log.Fatalf("CDP数据迁移失败: %v", err)
	}

	// 验证迁移结果
	verifyMigration()
}

// verifyMigration 验证（自动补注释，原为缺注释的顶层声明）。
func verifyMigration() {
	log.Println("\n--- 验证迁移结果 ---")

	var count int64
	tables := []string{"cdp_profiles", "cdp_tag_definitions", "cdp_tag_assignments", "event_logs", "id_mappings"}

	for _, t := range tables {
		db.DB.Model(&model.Model{}).Count(&count) // placeholder
		_ = count
		_ = t
	}

	// 简单计数
	for _, table := range []string{"cdp_profiles", "cdp_tag_definitions", "cdp_tag_assignments", "event_logs"} {
		var c int64
		db.DB.Table(table).Count(&c)
		log.Printf("  %s: %d 条记录", table, c)
	}
}

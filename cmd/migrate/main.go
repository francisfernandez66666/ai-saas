// 迁移运维入口（C4）：支持 up/down/status，down 默认只回退 003+ 增量，full-down 才允许回退基线。
// 用法：
//
//	go run ./cmd/migrate up
//	go run ./cmd/migrate down 3
//	go run ./cmd/migrate full-down
//	go run ./cmd/migrate status
package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"

	"ai-scrm/config"
	"ai-scrm/internal/db"
)

func main() {
	log.SetFlags(log.LstdFlags)
	_ = godotenv.Load(".env")
	if config.GlobalConfig == nil {
		_ = config.LoadConfig()
	}
	if db.DB == nil {
		if err := db.Init(); err != nil {
			log.Fatalf("初始化数据库失败: %v", err)
		}
	}

	args := os.Args[1:]
	cmd := "status"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "up":
		if err := db.MigrateUp(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("up ok")
	case "down":
		steps := 1
		if len(args) > 1 {
			n, err := strconv.Atoi(args[1])
			if err != nil {
				log.Fatalf("down 参数必须是数字: %v", err)
			}
			steps = n
		}
		confirmDestructive()
		if err := db.MigrateDown(steps); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("down ok: %d\n", steps)
	case "full-down":
		confirmDestructive()
		if err := db.MigrateDown(-1); err != nil {
			log.Fatal(err)
		}
		fmt.Println("full-down ok")
	case "status":
		v, err := db.AppliedMigrations()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(strings.Join(v, "\n"))
	default:
		log.Fatalf("未知命令: %s", cmd)
	}
}

func confirmDestructive() {
	fmt.Print("即将执行数据库迁移回退（可能删除表/列），输入 yes 继续: ")
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	if strings.TrimSpace(line) != "yes" {
		os.Exit(1)
	}
}

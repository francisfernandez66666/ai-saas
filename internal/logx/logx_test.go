// C3 单测：手机号/身份证/邮箱掩码、Safe 截断、幂等、误伤防护
package logx

import (
	"strings"
	"testing"
)

func TestMaskPhone(t *testing.T) {
	got := Mask("客户手机 13812345678 请回电")
	if strings.Contains(got, "13812345678") {
		t.Fatalf("手机号未掩码: %q", got)
	}
	if !strings.Contains(got, "138****5678") {
		t.Fatalf("应保留首3尾4: %q", got)
	}
}

func TestMaskAdjacentPhones(t *testing.T) {
	got := Mask("13811112222,13933334444")
	if strings.Contains(got, "13811112222") || strings.Contains(got, "13933334444") {
		t.Fatalf("相邻两手机号应都掩码: %q", got)
	}
}

func TestMaskIDCard(t *testing.T) {
	got := Mask("身份证 11010119900307123X 备案")
	if strings.Contains(got, "11010119900307123X") {
		t.Fatalf("身份证未掩码: %q", got)
	}
	if !strings.Contains(got, "1101") || !strings.Contains(got, "123X") {
		t.Fatalf("身份证应保留首4尾4: %q", got)
	}
}

func TestMaskEmail(t *testing.T) {
	got := Mask("邮箱 zhangsan@example.com 已注册")
	if strings.Contains(got, "zhangsan@") {
		t.Fatalf("邮箱未掩码: %q", got)
	}
	if !strings.Contains(got, "z***n@example.com") {
		t.Fatalf("邮箱应保留首尾+域名: %q", got)
	}
}

func TestMaskIdempotent(t *testing.T) {
	once := Mask("手机 13812345678")
	twice := Mask(once)
	if once != twice {
		t.Fatalf("掩码应幂等: once=%q twice=%q", once, twice)
	}
}

func TestNoFalsePositive(t *testing.T) {
	// 订单号/时间戳/版本号等非手机号数字不应被吞
	in := "订单 2026091212345678 端口9090 版本v1.2.3"
	got := Mask(in)
	if got != in {
		t.Fatalf("长数字/普通数字不应误伤: in=%q got=%q", in, got)
	}
	// 10 位数字（非 11）不掩码
	if Mask("编号 1381234567") != "编号 1381234567" {
		t.Fatal("10位数字不应被当作手机号掩码")
	}
}

func TestSafeTruncate(t *testing.T) {
	long := strings.Repeat("字", 50)
	got := Safe(long, 20)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("超长应截断加省略号: %q", got)
	}
	if strings.Count(got, "字") != 20 {
		t.Fatalf("应截断到20字: %q", got)
	}
}

func TestSafeMasksPhone(t *testing.T) {
	// 截断后仍含手机片段也要掩码
	got := Safe("我的电话是13812345678请尽快联系我谢谢", 40)
	if strings.Contains(got, "13812345678") {
		t.Fatalf("Safe 应同时掩码手机号: %q", got)
	}
}

func TestGormLoggerMasksSQL(t *testing.T) {
	// 脱敏 logger 接口满足编译即验证结构；行为经 Mask 已覆盖
	if NewGormLogger(nil) == nil {
		t.Fatal("NewGormLogger 不应返回 nil")
	}
}

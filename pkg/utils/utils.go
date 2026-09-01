// Package utils：pkg/utils 模块（自动补包注释）。
package utils

import (
	"crypto/md5"
	"encoding/hex"
	"math"
	"math/rand"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ============================================================
// 工具函数集合
// 所有通用工具函数都放在这里
// ============================================================

// HashPassword 密码哈希（使用bcrypt）
// 为什么用bcrypt？bcrypt自带盐值，安全性高，且计算慢抗暴力破解
func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(bytes), err
}

// CheckPassword 验证密码
func CheckPassword(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// MD5 MD5哈希（用于生成简单的唯一标识等）
func MD5(str string) string {
	h := md5.New()
	h.Write([]byte(str))
	return hex.EncodeToString(h.Sum(nil))
}

// Softmax softmax归一化函数
// 输入：分数向量  输出：概率向量
// τ温度系数：越小越锐利，越大越平滑
func Softmax(scores []float64, tau float64) []float64 {
	n := len(scores)
	if n == 0 {
		return []float64{}
	}

	// 先除以温度系数
	scaled := make([]float64, n)
	for i, s := range scores {
		scaled[i] = s / tau
	}

	// 找最大值（数值稳定技巧：减去最大值再计算）
	maxVal := scaled[0]
	for _, s := range scaled[1:] {
		if s > maxVal {
			maxVal = s
		}
	}

	// 计算exp
	exps := make([]float64, n)
	sumExp := 0.0
	for i, s := range scaled {
		e := math.Exp(s - maxVal)
		exps[i] = e
		sumExp += e
	}

	// 归一化
	probs := make([]float64, n)
	for i, e := range exps {
		probs[i] = e / sumExp
	}
	return probs
}

// ArgMax 返回最大值的索引
func ArgMax(arr []float64) int {
	if len(arr) == 0 {
		return -1
	}
	maxIdx := 0
	maxVal := arr[0]
	for i, v := range arr {
		if v > maxVal {
			maxVal = v
			maxIdx = i
		}
	}
	return maxIdx
}

// CosineSimilarity 余弦相似度
// 两个向量必须长度相同
func CosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	dotProduct := 0.0
	magA := 0.0
	magB := 0.0

	for i := range a {
		dotProduct += a[i] * b[i]
		magA += a[i] * a[i]
		magB += b[i] * b[i]
	}

	if magA == 0 || magB == 0 {
		return 0
	}

	return dotProduct / (math.Sqrt(magA) * math.Sqrt(magB))
}

// Clamp 将值限制在[min, max]范围内
func Clamp(val, minVal, maxVal float64) float64 {
	if val < minVal {
		return minVal
	}
	if val > maxVal {
		return maxVal
	}
	return val
}

// RandomString 生成随机字符串
func RandomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	seed := rand.New(rand.NewSource(time.Now().UnixNano()))
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[seed.Intn(len(charset))]
	}
	return string(b)
}

// ContainsString 字符串数组包含检查
func ContainsString(arr []string, target string) bool {
	for _, s := range arr {
		if s == target {
			return true
		}
	}
	return false
}

// LevenshteinDistance 编辑距离
// 用于简单的文本相似度计算
func LevenshteinDistance(s1, s2 string) int {
	r1 := []rune(s1)
	r2 := []rune(s2)
	len1 := len(r1)
	len2 := len(r2)

	if len1 == 0 {
		return len2
	}
	if len2 == 0 {
		return len1
	}

	// 创建二维数组
	prev := make([]int, len2+1)
	curr := make([]int, len2+1)

	for j := 0; j <= len2; j++ {
		prev[j] = j
	}

	for i := 1; i <= len1; i++ {
		curr[0] = i
		for j := 1; j <= len2; j++ {
			cost := 1
			if r1[i-1] == r2[j-1] {
				cost = 0
			}
			curr[j] = minInt(
				prev[j]+1,      // 删除
				curr[j-1]+1,    // 插入
				prev[j-1]+cost, // 替换
			)
		}
		prev, curr = curr, prev
	}

	return prev[len2]
}

// TextSimilarity 简单文本相似度（基于编辑距离）
// 返回0-1之间的值，1表示完全相同
func TextSimilarity(s1, s2 string) float64 {
	if s1 == "" && s2 == "" {
		return 1.0
	}
	if s1 == "" || s2 == "" {
		return 0.0
	}
	dist := LevenshteinDistance(strings.ToLower(s1), strings.ToLower(s2))
	maxLen := len([]rune(s1))
	if len([]rune(s2)) > maxLen {
		maxLen = len([]rune(s2))
	}
	if maxLen == 0 {
		return 1.0
	}
	return 1.0 - float64(dist)/float64(maxLen)
}

// minInt 返回三个 int 中的最小值（编辑距离计算用）
func minInt(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// Now 统一获取当前时间的函数
// 方便后续统一时区处理
func Now() time.Time {
	return time.Now()
}

// FormatTime 格式化时间
func FormatTime(t time.Time) string {
	return t.Format("2006-01-02 15:04:05")
}

package main

import (
	"fmt"
	"strings"
)

// SurfSeq Flowcell ID 可读压缩：21 字符 → 10 字符（lane字母 + YYMMDD + 批次），
// 与原生同构，MGI/Illumina 通用。
const (
	fcSourceLen = 21
	fcCodeLen   = 10
)

// fcPatterns：字母段模式注册表。
// 未收录组合 encodeFC 报错，追加一行即可扩展；首字母必须互不相同（init 校验双射）。
var fcPatterns = [][4]string{
	{"4P", "US", "A2", "A"}, // 实测
	{"4P", "US", "D2", "D"}, // 实测
	{"4N", "CN", "B1", "B"}, // 预留扩展示例
}

func init() {
	seen := make(map[string]string, len(fcPatterns))
	for _, p := range fcPatterns {
		if prev, dup := seen[p[3]]; dup {
			panic(fmt.Sprintf("fcPatterns: letter %q mapped by both %s and %s/%s/%s",
				p[3], prev, p[0], p[1], p[2]))
		}
		seen[p[3]] = p[0] + "/" + p[1] + "/" + p[2]
	}
}

func isAlphaByte(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

// encodeFC 将 21 字符 SurfSeq 芯片号压缩为 10 字符：lane字母 + 日期(6) + 批次(3)。
func encodeFC(fc string) (string, error) {
	if len(fc) != fcSourceLen {
		return "", fmt.Errorf("flowcell ID %q: want %d chars, got %d", fc, fcSourceLen, len(fc))
	}
	for i := 0; i < fcSourceLen; i++ {
		c := fc[i]
		if i >= 2 && i <= 10 || i >= 13 && i <= 18 {
			if !isDigit(c) {
				return "", fmt.Errorf("flowcell ID %q: position %d wants a digit, got %q", fc, i, c)
			}
		} else if !isAlphaByte(c) && !isDigit(c) {
			// 实测
			return "", fmt.Errorf("flowcell ID %q: position %d wants an alphanumeric char, got %q", fc, i, c)
		}
	}
	if err := checkDate(fc[2:8]); err != nil {
		return "", fmt.Errorf("flowcell ID %q: %v", fc, err)
	}
	var letter string
	for _, p := range fcPatterns {
		if fc[0:2] == p[0] && fc[11:13] == p[1] && fc[19:21] == p[2] {
			letter = p[3]
			break
		}
	}
	if letter == "" {
		return "", fmt.Errorf("flowcell ID %q: letter pattern %s/%s/%s not in fcPatterns registry (append it to encode)",
			fc, fc[0:2], fc[11:13], fc[19:21])
	}
	return letter + fc[2:11], nil
}

// checkDate 校验 YYMMDD 段
func checkDate(s string) error {
	if len(s) != 6 {
		return fmt.Errorf("date %q: want 6 digits", s)
	}
	mm := int(s[2]-'0')*10 + int(s[3]-'0')
	dd := int(s[4]-'0')*10 + int(s[5]-'0')
	if mm < 1 || mm > 12 {
		return fmt.Errorf("date %q: month %02d out of range 01-12", s, mm)
	}
	if dd < 1 || dd > 31 {
		return fmt.Errorf("date %q: day %02d out of range 01-31", s, dd)
	}
	return nil
}

// fcMapLine 返回对照表一行："原始芯片号\t压缩编码"。
func fcMapLine(fc string) (string, error) {
	code, err := encodeFC(fc)
	if err != nil {
		return "", err
	}
	return fc + "\t" + code, nil
}

// fcEncoder 缓存单 worker 内的 FC 编码结果（同一文件 FC 恒定，仅首次编码有开销；
// FC 变化时自动重编码）。
//
// encode 返回的切片归 encoder 所有，FC 变化后被复用覆盖：调用方必须立即消费，
type fcEncoder struct {
	orig string
	code []byte
}

func (e *fcEncoder) encode(fc string) ([]byte, error) {
	if e.orig == fc && e.code != nil {
		return e.code, nil
	}
	s, err := encodeFC(fc)
	if err != nil {
		return nil, err
	}
	e.orig = fc
	e.code = []byte(s)
	return e.code, nil
}

// 转换表 .chipmap.tsv
func chipMapPath(out1 string) string {
	return strings.TrimSuffix(out1, ".gz") + ".chipmap.tsv"
}

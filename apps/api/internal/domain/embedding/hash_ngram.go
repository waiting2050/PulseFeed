// hash_ngram.go 实现基于 FNV 哈希的字符 n-gram 文本向量化（维度 256、2–3 gram、带符号累加）。
package domembedding

import (
	"hash/fnv"
	"strings"
	"unicode"
)

// HashText 把标题文本映射为 Dimension 维向量：先做小写归一与标点过滤，
// 再对 2–3 gram 做 FNV-1a 哈希，按哈希值落到固定维度并做带符号累加，最后 L2 归一化。
// 同一文本恒定得到同一向量；空文本返回零向量；单字符文本退化为 1-gram，避免退化为全零。
func HashText(text string) Vector {
	normalized := normalizeText(text)
	vector := make(Vector, Dimension)
	if normalized == "" {
		return vector
	}

	for n := 2; n <= 3; n++ {
		if len(normalized) < n {
			continue
		}
		for i := 0; i+n <= len(normalized); i++ {
			index, sign := hashGram(normalized[i : i+n])
			vector[index] += sign
		}
	}

	// 单字符文本没有 2–3 gram，退化为 1-gram，保证非空文本仍有特征。
	if vector.IsZero() {
		for _, r := range normalized {
			index, sign := hashGram(string(r))
			vector[index] += sign
		}
	}
	return vector.Normalized()
}

// normalizeText 小写化并只保留字母与数字，去掉空白与标点，降低文本格式差异带来的噪声。
func normalizeText(text string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// hashGram 计算 gram 的 FNV-1a 64 位哈希，映射到 [0,Dimension) 的维度下标与 ±1 符号。
func hashGram(gram string) (int, float64) {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(gram))
	value := hasher.Sum64()
	index := int(value % Dimension)
	if value&1 == 1 {
		return index, -1
	}
	return index, 1
}

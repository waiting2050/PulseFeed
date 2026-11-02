// hash_ngram_test.go 校验 hash n-gram 的确定性、维度、余弦相似度区分度与空文本行为。
package domembedding

import (
	"math"
	"testing"
)

// TestHashTextDimension 校验所有输入都得到固定 256 维向量。
func TestHashTextDimension(t *testing.T) {
	cases := []string{"", "a", "golang 并发编程实战", "PulseFeed 推荐系统 · 向量召回"}
	for _, text := range cases {
		if got := len(HashText(text)); got != Dimension {
			t.Fatalf("HashText(%q) 维度 = %d, 期望 %d", text, got, Dimension)
		}
	}
}

// TestHashTextDeterministic 校验同一文本重复计算得到完全一致的向量。
func TestHashTextDeterministic(t *testing.T) {
	text := "golang 并发编程 实战 教程"
	first := HashText(text)
	second := HashText(text)
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("同一文本第 %d 维不一致: %v != %v", i, first[i], second[i])
		}
	}
}

// TestHashTextEmptyIsZero 校验空文本与纯标点文本得到零向量。
func TestHashTextEmptyIsZero(t *testing.T) {
	for _, text := range []string{"", "   ", "!!!···"} {
		if vector := HashText(text); !vector.IsZero() {
			t.Fatalf("HashText(%q) 期望零向量，实际存在非零分量", text)
		}
	}
}

// TestHashTextCosineSimilarity 校验相近文本的余弦相似度明显高于无关文本。
func TestHashTextCosineSimilarity(t *testing.T) {
	base := HashText("golang 并发编程 实战 教程")
	cases := []struct {
		name       string
		text       string
		wantHigher bool
	}{
		{name: "词序调换", text: "golang 并发编程 教程 实战", wantHigher: true},
		{name: "同主题换词", text: "golang 并发编程 入门 教程", wantHigher: true},
		{name: "无关主题", text: "美食 烘焙 蛋糕 配方", wantHigher: false},
	}
	unrelated := base.Cosine(HashText("美食 烘焙 蛋糕 配方"))
	if unrelated >= 0.2 {
		t.Fatalf("无关文本相似度 %.4f 过高，区分度不足", unrelated)
	}
	for _, tc := range cases {
		score := base.Cosine(HashText(tc.text))
		if tc.wantHigher && score <= unrelated {
			t.Fatalf("%s 相似度 %.4f 未高于无关文本 %.4f", tc.name, score, unrelated)
		}
	}
}

// TestHashTextIdenticalCosine 校验逐字符一致的文本余弦相似度为 1。
func TestHashTextIdenticalCosine(t *testing.T) {
	text := "PulseFeed 同构仿写 推荐与向量"
	score := HashText(text).Cosine(HashText(text))
	if math.Abs(score-1) > 1e-9 {
		t.Fatalf("相同文本余弦相似度 = %.9f, 期望 1", score)
	}
}

// TestCosineInvalid 校验维度不一致与零向量场景下余弦相似度返回 0。
func TestCosineInvalid(t *testing.T) {
	if score := HashText("abc").Cosine(make(Vector, 3)); score != 0 {
		t.Fatalf("维度不一致余弦相似度 = %v, 期望 0", score)
	}
	zero := make(Vector, Dimension)
	if score := zero.Cosine(HashText("abc")); score != 0 {
		t.Fatalf("零向量余弦相似度 = %v, 期望 0", score)
	}
}

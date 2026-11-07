// entity_test.go 验证标题长度按字符（rune）计长的边界行为。
package domclip

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestNewClipDescription 覆盖简介可选、按字符限长、emoji 与旧记录空简介读取。
func TestNewClipDescription(t *testing.T) {
	tests := []struct {
		name        string
		description string
		wantErr     error
	}{
		{name: "空简介合法", description: ""},
		{name: "仅空白简介按空处理", description: "   "},
		{name: "中文等于上限", description: strings.Repeat("中", descriptionMaxLen)},
		{name: "中文超过上限", description: strings.Repeat("中", descriptionMaxLen+1), wantErr: ErrInvalidDescription},
		{name: "emoji 等于上限", description: strings.Repeat("🎬", descriptionMaxLen)},
		{name: "emoji 超过上限", description: strings.Repeat("🎬", descriptionMaxLen+1), wantErr: ErrInvalidDescription},
		{name: "含 emoji 与换行合法", description: "  第一行 🎬\n第二行 🔥  "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clip, err := NewClip(1, "标题", tt.description, "https://cdn.example.com/v.mp4", "", 0, "key")
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("期望错误 %v，实际 %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("不应返回错误: %v", err)
			}
			if clip.Description != strings.TrimSpace(tt.description) {
				t.Fatalf("简介不符预期: %q", clip.Description)
			}
		})
	}
}

// TestRestoreClipWithoutDescription 验证旧记录无简介（列不存在/NULL）恢复后按空串可读。
func TestRestoreClipWithoutDescription(t *testing.T) {
	clip := RestoreClip(9, 7, "旧标题", "", "/media/9.mp4", "", 0, StatusPublished, "seed-9", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if clip.Description != "" {
		t.Fatalf("旧记录简介应为空串, 实际 %q", clip.Description)
	}
	if !clip.Visible() {
		t.Fatalf("旧记录应正常可读")
	}
}

func TestNewClipTitleRuneLimit(t *testing.T) {
	tests := []struct {
		name    string
		title   string
		wantErr error
	}{
		{name: "中文等于上限", title: strings.Repeat("中", titleMaxLen)},
		{name: "中文超过上限", title: strings.Repeat("中", titleMaxLen+1), wantErr: ErrInvalidTitle},
		{name: "emoji 等于上限", title: strings.Repeat("🎬", titleMaxLen)},
		{name: "emoji 超过上限", title: strings.Repeat("🎬", titleMaxLen+1), wantErr: ErrInvalidTitle},
		{name: "中文与 emoji 混合等于上限", title: strings.Repeat("中", 64) + strings.Repeat("🎬", 64)},
		{name: "去空白后等于上限", title: "  " + strings.Repeat("中", titleMaxLen) + "  "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clip, err := NewClip(1, tt.title, "", "https://cdn.example.com/v.mp4", "", 0, "key")
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("期望错误 %v，实际 %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("不应返回错误: %v", err)
			}
			if clip.Title != strings.TrimSpace(tt.title) {
				t.Fatalf("标题不符预期: %q", clip.Title)
			}
		})
	}
}

// entity_test.go 验证标题长度按字符（rune）计长的边界行为。
package domclip

import (
	"errors"
	"strings"
	"testing"
)

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
			clip, err := NewClip(1, tt.title, "https://cdn.example.com/v.mp4", "", 0, "key")
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

package cursor

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var ErrInvalid = errors.New("invalid cursor")

type Page struct {
	PublishedAt time.Time
	ID          int64
}

func Encode(publishedAt time.Time, id int64) string {
	raw := fmt.Sprintf("%d:%d", publishedAt.UTC().UnixNano(), id)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func Decode(value string) (*Page, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, ErrInvalid
	}

	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return nil, ErrInvalid
	}

	nanos, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil, ErrInvalid
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		return nil, ErrInvalid
	}

	return &Page{PublishedAt: time.Unix(0, nanos).UTC(), ID: id}, nil
}

// Rank 用于按综合分数排序的场景，例如热榜。
type Rank struct {
	Score float64
	ID    int64
}

func EncodeRank(score float64, id int64) string {
	raw := strconv.FormatFloat(score, 'f', -1, 64) + ":" + strconv.FormatInt(id, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func DecodeRank(value string) (*Rank, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, ErrInvalid
	}

	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return nil, ErrInvalid
	}

	score, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return nil, ErrInvalid
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		return nil, ErrInvalid
	}
	return &Rank{Score: score, ID: id}, nil
}

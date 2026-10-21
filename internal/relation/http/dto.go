package http

type followResponse struct {
	UserID    int64 `json:"user_id"`
	Following bool  `json:"following"`
}

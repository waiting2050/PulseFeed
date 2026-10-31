// dto.go 定义账号认证相关接口的请求与响应结构，响应统一包裹在 code/message/data 信封内。
package httpaccount

// registerRequest 是注册接口的请求体。
type registerRequest struct {
	Account  string `json:"account"`
	Password string `json:"password"`
	Nickname string `json:"nickname"`
}

// loginRequest 是登录接口的请求体。
type loginRequest struct {
	Account  string `json:"account"`
	Password string `json:"password"`
}

// updateProfileRequest 是资料部分更新的请求体，字段缺省表示不修改。
type updateProfileRequest struct {
	Nickname  *string `json:"nickname"`
	AvatarURL *string `json:"avatar_url"`
	Bio       *string `json:"bio"`
}

// profileResponse 是资料视图响应。
type profileResponse struct {
	UserID    int64  `json:"user_id"`
	Account   string `json:"account,omitempty"`
	Nickname  string `json:"nickname"`
	AvatarURL string `json:"avatar_url"`
	Bio       string `json:"bio"`
}

// sessionResponse 是登录成功的响应，携带 Bearer 访问令牌。
type sessionResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
	UserID      int64  `json:"user_id"`
	Account     string `json:"account"`
}

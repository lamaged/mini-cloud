package baidu

// ============================================================
// OAuth2 Token
// ============================================================

type TokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

type TokenErrResp struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// ============================================================
// 文件列表
// ============================================================

type File struct {
	FsId           int64  `json:"fs_id"`
	Path           string `json:"path"`
	ServerFilename string `json:"server_filename"`
	Size           int64  `json:"size"`
	ServerMtime    int64  `json:"server_mtime"`
	ServerCtime    int64  `json:"server_ctime"`
	Isdir          int    `json:"isdir"`
	Category       int    `json:"category"`
	Md5            string `json:"md5"`
	Thumbs         struct {
		Url3 string `json:"url3"`
	} `json:"thumbs"`
}

type ListResp struct {
	Errno int    `json:"errno"`
	List  []File `json:"list"`
}

// ============================================================
// 下载链接
// ============================================================

type DownloadResp struct {
	Errno int `json:"errno"`
	List  []struct {
		Dlink string `json:"dlink"`
	} `json:"list"`
}

// ============================================================
// Token 持久化
// ============================================================

// TokenState state 缓存（批 2 §6.5：纯缓存——只存 access_token/过期时间/格式版本/授权代次；
// refresh_token 唯一真值源为 config.json，不再入缓存）。
// V=1 为新格式；旧文件（无 V 或 V=0，曾含 refresh_token）视为过期缓存、重新兑换。
type TokenState struct {
	V                   int   `json:"v"`
	AccessToken         string `json:"access_token"`
	ExpiresAt           int64  `json:"expires_at"`
	CredentialGeneration int   `json:"credential_generation"`
}

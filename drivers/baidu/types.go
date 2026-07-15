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

type TokenState struct {
	RefreshToken string `json:"refresh_token"`
	AccessToken  string `json:"access_token"`
	ExpiresAt    int64  `json:"expires_at"`
}

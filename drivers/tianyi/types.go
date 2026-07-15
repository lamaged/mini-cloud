package tianyi

import "strings"

// ============================================================
// API 响应错误
// ============================================================

// RespErr 天翼云盘 API 错误响应（多种格式兼容）
type RespErr struct {
	ResCode    any    `json:"res_code"`
	ResMessage string `json:"res_message"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Msg        string `json:"msg"`
	ErrorCode  string `json:"errorCode"`
	ErrorMsg   string `json:"errorMsg"`
}

func (e *RespErr) HasError() bool {
	switch v := e.ResCode.(type) {
	case float64:
		return v != 0
	case string:
		return v != ""
	}
	return (e.Code != "" && e.Code != "SUCCESS") || e.ErrorCode != ""
}

func (e *RespErr) Error() string {
	if e.ResMessage != "" {
		return "tianyi: " + e.ResMessage
	}
	if e.Msg != "" {
		return "tianyi: " + e.Msg
	}
	if e.Message != "" {
		return "tianyi: " + e.Message
	}
	if e.ErrorMsg != "" {
		return "tianyi: " + e.ErrorMsg
	}
	return "tianyi: unknown error"
}

// ============================================================
// 加密配置
// ============================================================

type EncryptConfResp struct {
	Result int `json:"result"`
	Data   struct {
		PubKey string `json:"pubKey"`
		Pre    string `json:"pre"`
	} `json:"data"`
}

// ============================================================
// 登录相关类型
// ============================================================

type LoginParam struct {
	RsaUsername  string
	RsaPassword  string
	Lt           string
	ReqId        string
	ParamId      string
	CaptchaToken string
}

type LoginResp struct {
	Result int    `json:"result"`
	Msg    string `json:"msg"`
	ToUrl  string `json:"toUrl"`
}

// ============================================================
// 会话 Token
// ============================================================

type AppSessionResp struct {
	ResCode    int    `json:"res_code"`
	ResMessage string `json:"res_message"`
	LoginName  string `json:"loginName"`

	SessionKey    string `json:"sessionKey"`
	SessionSecret string `json:"sessionSecret"`
	AccessToken   string `json:"accessToken"`
	RefreshToken  string `json:"refreshToken"`

	FamilySessionKey    string `json:"familySessionKey"`
	FamilySessionSecret string `json:"familySessionSecret"`
}

// 刷新后的用户会话（不含 AccessToken）
type UserSessionResp struct {
	ResCode     int    `json:"res_code"`
	ResMessage  string `json:"res_message"`
	SessionKey  string `json:"sessionKey"`
	SessionSecret string `json:"sessionSecret"`
}

// ============================================================
// 文件和文件夹
// ============================================================

// FlexString 灵活字符串：JSON 反序列化时兼容数字和字符串
type FlexString string

func (s *FlexString) UnmarshalJSON(b []byte) error {
	str := strings.Trim(string(b), "\"")
	*s = FlexString(str)
	return nil
}

type Cloud189File struct {
	ID         FlexString `json:"id"`
	Name       string     `json:"name"`
	Size       int64      `json:"size"`
	Md5        string     `json:"md5"`
	LastOpTime string     `json:"lastOpTime"`
	CreateDate string     `json:"createDate"`
	Icon       struct {
		SmallUrl string `json:"smallUrl"`
		LargeUrl string `json:"largeUrl"`
	} `json:"icon"`
}

type Cloud189Folder struct {
	ID         FlexString `json:"id"`
	ParentID   int64  `json:"parentId"`
	Name       string `json:"name"`
	LastOpTime string `json:"lastOpTime"`
	CreateDate string `json:"createDate"`
}

type Cloud189FilesResp struct {
	FileListAO struct {
		Count      int                `json:"count"`
		FileList   []Cloud189File     `json:"fileList"`
		FolderList []Cloud189Folder   `json:"folderList"`
	} `json:"fileListAO"`
}

// ============================================================
// 下载
// ============================================================

type DownloadURLResp struct {
	URL string `json:"fileDownloadUrl"`
}

// ============================================================
// Token 状态持久化
// ============================================================

type TokenState struct {
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token"`
	SessionKey    string `json:"session_key"`
	SessionSecret string `json:"session_secret"`
	ExpiresAt     int64  `json:"expires_at"`
	LoginName     string `json:"login_name"`

	FamilyID            string `json:"family_id,omitempty"`
	FamilySessionKey    string `json:"family_session_key,omitempty"`
	FamilySessionSecret string `json:"family_session_secret,omitempty"`
}

package mobile

import "encoding/xml"

// ============================================================
// 基础响应
// ============================================================

type BaseResp struct {
	Success bool   `json:"success"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ============================================================
// 个人云新版 API 类型
// ============================================================

type PersonalThumbnail struct {
	Style string `json:"style"`
	Url   string `json:"url"`
}

type PersonalFileItem struct {
	FileId     string              `json:"fileId"`
	Name       string              `json:"name"`
	Size       int64               `json:"size"`
	Type       string              `json:"type"` // "file" or "folder"
	CreatedAt  string              `json:"createdAt"`
	UpdatedAt  string              `json:"updatedAt"`
	Thumbnails []PersonalThumbnail `json:"thumbnailUrls"`
}

type PersonalListResp struct {
	BaseResp
	Data struct {
		Items          []PersonalFileItem `json:"items"`
		NextPageCursor string             `json:"nextPageCursor"`
	} `json:"data"`
}

// ============================================================
// 下载链接
// ============================================================

type DownloadUrlResp struct {
	BaseResp
	Data struct {
		Url    string `json:"url"`
		CdnUrl string `json:"cdnUrl"`
	} `json:"data"`
}

// ============================================================
// 路由策略（获取个人云主机地址）
// ============================================================

type QueryRoutePolicyResp struct {
	Success bool   `json:"success"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Data    struct {
		RoutePolicyList []struct {
			SiteID   string `json:"siteID"`
			ModName  string `json:"modName"`
			HttpsUrl string `json:"httpsUrl"`
		} `json:"routePolicyList"`
	} `json:"data"`
}

// ============================================================
// Token 刷新
// ============================================================

type RefreshTokenResp struct {
	XMLName     xml.Name `xml:"root"`
	Return      string   `xml:"return"`
	Token       string   `xml:"token"`
	Expiretime  int32    `xml:"expiretime"`
	AccessToken string   `xml:"accessToken"`
	Desc        string   `xml:"desc"`
}

// ============================================================
// Token 持久化
// ============================================================

type TokenState struct {
	Authorization string `json:"authorization"`
	Account       string `json:"account"`
	CloudHost     string `json:"cloud_host"`
	ExpiresAt     int64  `json:"expires_at"`
}

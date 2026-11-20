package webdav

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"mini-cloud/internal/driver"
	"mini-cloud/internal/model"
)

// handlePropfind 处理 PROPFIND 请求
func (h *Handler) handlePropfind(w http.ResponseWriter, r *http.Request) (int, error) {
	reqPath := h.stripPrefix(r.URL.Path)

	// 先判断目标是文件还是目录
	info, err := h.getObj(r.Context(), reqPath)
	if err != nil {
		if isNotFound(err) {
			return http.StatusNotFound, nil
		}
		if errors.Is(err, driver.ErrForbidden) {
			// Vault _mc 密文对象：PROPFIND 隐藏（I4）
			return http.StatusForbidden, nil
		}
		return http.StatusInternalServerError, err
	}

	depth := r.Header.Get("Depth")
	if depth == "" {
		depth = "infinity"
	}

	ms := &multistatus{}

	// 目标路径本身的属性
	href := h.buildHref(reqPath, info.IsDir)
	props := h.buildPropsXML(info)
	ms.Responses = append(ms.Responses, response{
		Href: href,
		Propstat: []propstat{
			{Prop: rawXML{Inner: []byte(props)}, Status: "HTTP/1.1 200 OK"},
		},
	})

	// 如果是目录且 depth > 0，添加子项
	if info.IsDir {
		walkDepth := 0
		if depth == "1" || depth == "infinity" {
			walkDepth = 1
		}
		if walkDepth > 0 {
			objs, err := h.listPath(r.Context(), reqPath)
			if err != nil {
				if isNotFound(err) {
					return http.StatusNotFound, nil
				}
				return http.StatusInternalServerError, err
			}
			for i := range objs {
				childPath := path.Join(reqPath, objs[i].Name)
				href := h.buildHref(childPath, objs[i].IsDir)
				props := h.buildPropsXML(&objs[i])
				ms.Responses = append(ms.Responses, response{
					Href: href,
					Propstat: []propstat{
						{Prop: rawXML{Inner: []byte(props)}, Status: "HTTP/1.1 200 OK"},
					},
				})
			}
		}
	}

	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)

	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	return 0, enc.Encode(ms)
}

// buildPropsXML 构建属性的原始 XML（textProps 为文本属性，dirProps 为含子元素的属性）
func (h *Handler) buildPropsXML(obj *model.Obj) string {
	textProps := make(map[string]string)
	dirProps := make(map[string]string)

	// resourcetype（含子元素）
	if obj.IsDir {
		dirProps["resourcetype"] = "<collection/>"
	} else {
		dirProps["resourcetype"] = ""
	}

	// displayname
	textProps["displayname"] = obj.Name

	// getcontentlength
	if !obj.IsDir {
		textProps["getcontentlength"] = fmt.Sprintf("%d", obj.Size)
	}

	// getlastmodified
	t := obj.ModTime
	if !t.IsZero() {
		textProps["getlastmodified"] = t.UTC().Format(http.TimeFormat)
	}

	// getcontenttype（D3：ContentType 非空时为唯一来源，Vault 虚拟树填充）
	if obj.IsDir {
		textProps["getcontenttype"] = "httpd/unix-directory"
	} else if obj.ContentType != "" {
		textProps["getcontenttype"] = obj.ContentType
	} else {
		ext := filepath.Ext(obj.Name)
		ct := mime.TypeByExtension(ext)
		if ct == "" {
			ct = "application/octet-stream"
		}
		textProps["getcontenttype"] = ct
	}

	// getetag（D3：ETag 非空时为唯一来源）
	if obj.ETag != "" {
		textProps["getetag"] = obj.ETag
	} else {
		textProps["getetag"] = fmt.Sprintf(`"%x-%x"`, obj.ModTime.UnixNano(), obj.Size)
	}

	return buildPropXML(textProps, dirProps)
}

// listPath 列出路径内容（仅对目录有效）
func (h *Handler) listPath(ctx context.Context, reqPath string) ([]model.Obj, error) {
	return h.Driver.List(ctx, reqPath)
}

// getObj 获取单个路径的信息
func (h *Handler) getObj(ctx context.Context, reqPath string) (*model.Obj, error) {
	if reqPath == "/" || reqPath == "" {
		return &model.Obj{
			Name:  "/",
			IsDir: true,
		}, nil
	}

	parent := path.Dir(reqPath)
	name := path.Base(reqPath)

	objs, err := h.Driver.List(ctx, parent)
	if err != nil {
		// #10（外部审核 2026-11-20）：不得把 List 错误伪装成目录——
		// ErrForbidden（如 _mc 路径）必须透传 403，其余错误如实上抛，
		// 否则 PROPFIND 会返回虚假 207 且与 Vault 访问控制语义不一致
		return nil, err
	}

	for i := range objs {
		if objs[i].Name == name {
			return &objs[i], nil
		}
	}

	return nil, fmt.Errorf("not found: %s", reqPath)
}

// buildHref 构建 WebDAV href（经 escPath 逐段百分号编码：
// 中文、空格、#、? 文件名在严格客户端上需要编码，v0.10.0 独立修复）
func (h *Handler) buildHref(reqPath string, isDir bool) string {
	href := h.escPath(reqPath)
	if isDir && !strings.HasSuffix(href, "/") {
		href += "/"
	}
	return href
}

// isNotFound 判断是否为文件不存在的错误
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if os.IsNotExist(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "no such file") ||
		strings.Contains(msg, "cannot find") ||
		strings.Contains(msg, "does not exist")
}

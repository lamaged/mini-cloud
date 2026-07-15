package webdav

import (
	"encoding/xml"
	"strings"
)

// multistatus PROPFIND 响应的根元素
type multistatus struct {
	XMLName   xml.Name   `xml:"DAV: multistatus"`
	Responses []response `xml:"DAV: response"`
}

// response 单个资源的响应
type response struct {
	Href     string     `xml:"DAV: href"`
	Propstat []propstat `xml:"DAV: propstat"`
}

// propstat 属性状态
type propstat struct {
	Prop   rawXML `xml:"DAV: prop"`
	Status string `xml:"DAV: status"`
}

// rawXML 使用 innerxml 输出原始 XML，避免 encoding/xml 的命名空间问题
type rawXML struct {
	Inner []byte `xml:",innerxml"`
}

// propXML 构建 prop 内部的原始 XML 字符串
// 使用字符串拼接，避开 encoding/xml 对 WebDAV 命名空间的处理缺陷。
// 注意：不添加 D: 前缀，因为父元素 <prop> 已通过 xmlns="DAV:" 声明了默认命名空间，
// 子元素会自动继承该命名空间，无需前缀。
// 之前使用 D: 前缀会导致 XML 命名空间错误（前缀未声明），
// 这在 Windows Mini-Redirector 等严格客户端上会导致解析失败。
func buildPropXML(props map[string]string, dirProps map[string]string) string {
	var b strings.Builder
	for k, v := range props {
		if v == "" {
			b.WriteString("<" + k + "/>")
		} else {
			b.WriteString("<" + k + ">" + xmlEscape(v) + "</" + k + ">")
		}
	}
	for k, v := range dirProps {
		b.WriteString("<" + k + ">" + v + "</" + k + ">")
	}
	return b.String()
}

// xmlEscape 转义 XML 特殊字符
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

package service

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// The generated document can animate with CSS/SMIL, but cannot load resources or execute scripts.
const AccountProbeDocumentCSP = "default-src 'none'; script-src 'none'; style-src 'unsafe-inline'; img-src 'none'; font-src 'none'; connect-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"

var probeHTMLTags = wordSet("html head body title style div span main section article header footer p h1 h2 h3 small strong em b i br figure figcaption")
var probeAnimationTags = wordSet("animate animateTransform animateMotion set mpath")
var probeAnimationAttributes = wordSet("attributeName attributeType from to by dur repeatCount repeatDur begin end restart additive accumulate keyTimes keySplines keyPoints calcMode")
var probeAnimationTargets = func() map[string]bool {
	targets := wordSet("transform-origin transform-box visibility display color")
	// Any accepted drawing attribute may animate; identity and navigation may not.
	for name := range probeSVGAttributes {
		if name != "href" && name != "id" {
			targets[name] = true
		}
	}
	return targets
}()
var probeCSSComments = regexp.MustCompile("(?s)/\\*.*?\\*/")
var probeCSSURLs = regexp.MustCompile("(?i)url\\(([^)]*)\\)")

func validateProbeCSS(css string) error {
	check := strings.ToLower(probeCSSComments.ReplaceAllString(css, ""))
	if strings.Contains(check, "\\") || strings.Contains(check, "@import") || strings.Contains(check, "expression(") || strings.Contains(check, "javascript:") || strings.Contains(check, "behavior:") || strings.Contains(check, "-moz-binding") {
		return errors.New("动画样式不能包含脚本或外部导入")
	}
	for _, match := range probeCSSURLs.FindAllStringSubmatch(check, -1) {
		ref := strings.Trim(strings.TrimSpace(match[1]), "\"'")
		if !probeSVGRef.MatchString(ref) {
			return errors.New("动画样式只允许 SVG 内部引用")
		}
	}
	return nil
}

// Parse and rebuild an allowlisted document. Never insert model markup into the admin DOM.
func sanitizeProbeHTML(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	fence := strings.Repeat(string(rune(96)), 3)
	if strings.HasPrefix(raw, fence) {
		lines := strings.Split(raw, "\n")
		if len(lines) < 3 || strings.TrimSpace(lines[len(lines)-1]) != fence {
			return nil, errors.New("HTML 代码围栏未闭合")
		}
		raw = strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
	}
	if len(raw) > accountSVGProbeMaxBytes {
		return nil, errors.New("动画文档超过 1 MiB")
	}
	lower := strings.ToLower(raw)
	if !(strings.HasPrefix(lower, "<!doctype html") || strings.HasPrefix(lower, "<html") || strings.HasPrefix(lower, "<svg")) || strings.Contains(lower, "<!entity") || strings.Contains(lower, "<?") {
		return nil, errors.New("请返回包含 SVG 的完整 HTML 文档")
	}
	doc, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		return nil, errors.New("HTML 文档格式无效")
	}
	nodes, svgCount := 0, 0
	var head *html.Node
	var walk func(*html.Node, int) error
	walk = func(n *html.Node, depth int) error {
		nodes++
		if nodes > 20000 || depth > 64 {
			return errors.New("动画文档结构过于复杂")
		}
		if n.Type == html.ElementNode {
			svg := n.Namespace == "svg"
			if n.Namespace != "" && !svg {
				return errors.New("动画包含不支持的命名空间")
			}
			if svg {
				if !probeSVGTags[n.Data] && !probeAnimationTags[n.Data] && n.Data != "style" {
					return fmt.Errorf("不支持的 SVG 元素：%s", n.Data)
				}
				if n.Data == "svg" {
					svgCount++
				}
			} else if !probeHTMLTags[n.Data] {
				return fmt.Errorf("不支持的 HTML 元素：%s", n.Data)
			}
			if n.Data == "head" && !svg {
				head = n
			}
			animation := svg && probeAnimationTags[n.Data]
			attrs := make([]html.Attribute, 0, len(n.Attr))
			for _, a := range n.Attr {
				key := a.Key
				if strings.HasPrefix(strings.ToLower(key), "on") {
					return errors.New("动画不能包含事件脚本")
				}
				if a.Namespace == "xmlns" || key == "xmlns" {
					continue
				}
				if a.Namespace != "" && !(svg && a.Namespace == "xlink" && key == "href") {
					return errors.New("不支持的属性命名空间")
				}
				a.Namespace = "" // xlink:href is normalized to SVG2 href.
				common := key == "id" || key == "class" || key == "style" || key == "lang" || key == "title" || key == "role" || strings.HasPrefix(key, "aria-")
				if !common && !(svg && (probeSVGAttributes[key] || animation && probeAnimationAttributes[key])) {
					continue
				}
				if key == "href" && !probeSVGRef.MatchString(strings.TrimSpace(a.Val)) {
					return errors.New("动画只能引用 SVG 内部元素")
				}
				if key == "attributeName" && !probeAnimationTargets[strings.TrimSpace(a.Val)] {
					return fmt.Errorf("不支持的动画属性：%s（仅允许绘图属性）", a.Val)
				}
				if key == "style" {
					if err := validateProbeCSS(a.Val); err != nil {
						return err
					}
				}
				if svg && key != "style" {
					value := strings.ToLower(a.Val)
					if strings.Contains(value, "javascript:") || strings.Contains(value, "data:") || strings.Contains(value, "http:") || strings.Contains(value, "https:") || strings.Contains(value, "\\") {
						return errors.New("动画不能包含外部资源")
					}
					if strings.Contains(value, "url") {
						for _, part := range strings.Split(a.Val, ";") {
							if strings.Contains(strings.ToLower(part), "url") && !probeSVGPaintRef.MatchString(strings.TrimSpace(part)) {
								return errors.New("SVG 只允许内部绘图引用")
							}
						}
					}
				}
				attrs = append(attrs, a)
			}
			n.Attr = attrs
			if n.Data == "style" {
				var css strings.Builder
				for child := n.FirstChild; child != nil; child = child.NextSibling {
					if child.Type == html.TextNode {
						css.WriteString(child.Data)
					}
				}
				if err := validateProbeCSS(css.String()); err != nil {
					return err
				}
			}
		}
		for child := n.FirstChild; child != nil; {
			next := child.NextSibling
			// Metadata is not artwork. Drop all model-supplied declarations, including
			// refresh/CSP, then install known encoding, viewport and policy below.
			if child.Type == html.CommentNode || child.Type == html.DoctypeNode ||
				(child.Type == html.ElementNode && child.Namespace == "" && child.Data == "meta") {
				n.RemoveChild(child)
			} else if err := walk(child, depth+1); err != nil {
				return err
			}
			child = next
		}
		return nil
	}
	if err := walk(doc, 0); err != nil {
		return nil, err
	}
	if svgCount == 0 || head == nil {
		return nil, errors.New("HTML 中没有 SVG 画面")
	}
	policy := &html.Node{Type: html.ElementNode, Data: "meta", DataAtom: atom.Meta, Attr: []html.Attribute{{Key: "http-equiv", Val: "Content-Security-Policy"}, {Key: "content", Val: AccountProbeDocumentCSP}}}
	head.InsertBefore(policy, head.FirstChild)
	head.InsertBefore(&html.Node{Type: html.ElementNode, Data: "meta", DataAtom: atom.Meta, Attr: []html.Attribute{{Key: "charset", Val: "utf-8"}}}, policy)
	head.InsertBefore(&html.Node{Type: html.ElementNode, Data: "meta", DataAtom: atom.Meta, Attr: []html.Attribute{{Key: "name", Val: "viewport"}, {Key: "content", Val: "width=device-width, initial-scale=1"}}}, policy.NextSibling)
	var out bytes.Buffer
	out.WriteString("<!doctype html>\n")
	if err := html.Render(&out, doc); err != nil {
		return nil, err
	}
	if out.Len() > accountSVGProbeMaxBytes {
		return nil, errors.New("动画文档超过 1 MiB")
	}
	return out.Bytes(), nil
}

package tools

import (
	"fmt"
	stdhtml "html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// === 常量 ===

const (
	// fetchMaxBodyBytes 单次响应 body 上限,防超大页面爆内存。2MB 对 99% 文档页足够。
	fetchMaxBodyBytes = 2 * 1024 * 1024

	// fetchDefaultMaxChars 输出给 LLM 的字符数上限默认值。
	// ~10K chars ≈ 2-3K tokens(中英混合),够一篇博客或文档页核心内容。
	fetchDefaultMaxChars = 10000

	// fetchMaxMaxChars 即便用户/LLM 指定也不能超过的上限,防上下文炸掉。
	fetchMaxMaxChars = 30000

	// fetchTimeout HTTP 请求总超时(含 redirect)。
	fetchTimeout = 15 * time.Second
)

// === 工具入口 ===

// WebFetch 拉取单个 URL 的内容,HTML 自动转纯文本(去 script/style/nav,抽 main/article)。
// 比让 LLM 自己用 Bash 跑 curl + sed pipeline 更省 token、更可靠。
func WebFetch(args map[string]any) ToolResult {
	rawURL, _ := args["url"].(string)
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ToolResult{Output: "url 不能为空", Success: false}
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return ToolResult{Output: "url 必须以 http:// 或 https:// 开头", Success: false}
	}

	maxChars := toInt(args["max_chars"], fetchDefaultMaxChars)
	if maxChars <= 0 {
		maxChars = fetchDefaultMaxChars
	}
	if maxChars > fetchMaxMaxChars {
		maxChars = fetchMaxMaxChars
	}

	body, ct, finalURL, err := fetchURL(rawURL)
	if err != nil {
		return ToolResult{Output: fmt.Sprintf("拉取失败: %v", err), Success: false}
	}

	var content string
	if isHTMLType(ct) {
		content = extractMainText(body)
	} else {
		// 非 HTML (JSON / 纯文本 / Markdown 等) 直接返回原文
		content = body
	}
	content = strings.TrimSpace(content)

	totalChars := len([]rune(content))
	truncated := false
	if totalChars > maxChars {
		runes := []rune(content)
		content = string(runes[:maxChars])
		truncated = true
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "URL: %s\n", finalURL)
	if finalURL != rawURL {
		fmt.Fprintf(&sb, "(redirected from %s)\n", rawURL)
	}
	fmt.Fprintf(&sb, "Content-Type: %s\n", ct)
	if truncated {
		fmt.Fprintf(&sb, "Length: %d chars (truncated to first %d)\n", totalChars, maxChars)
	} else {
		fmt.Fprintf(&sb, "Length: %d chars\n", totalChars)
	}
	sb.WriteString("\n---\n\n")
	sb.WriteString(content)
	if truncated {
		sb.WriteString("\n\n[...内容已截断。如需更多,加大 max_chars 或在原 URL 用浏览器看全文]")
	}
	return ToolResult{Output: sb.String(), Success: true}
}

// === HTTP 拉取 ===

// fetchURL 发起 GET 请求,跟随重定向,body 限制 2MB。
// 返回 (body 文本, content-type, 最终 URL, err)。
func fetchURL(rawURL string) (string, string, string, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return "", "", "", err
	}
	// 真实浏览器 UA + 中文优先,绕过一些简单的反爬
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 "+
			"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,text/plain;q=0.8,*/*;q=0.7")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	client := &http.Client{Timeout: fetchTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBodyBytes))
	if err != nil {
		return "", "", "", err
	}

	finalURL := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	return string(data), resp.Header.Get("Content-Type"), finalURL, nil
}

func isHTMLType(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")
}

// === HTML → 纯文本 ===

// 一组正则用于 HTML 转纯文本。`(?is)` = 全文+忽略大小写+多行。
var (
	// 这些区块整段抹掉(脚本/样式/导航/页眉页脚/侧栏/表单)。
	// Go RE2 不支持反向引用 (\1),所以每个标签独立一条正则。
	fetchStripScriptRe   = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`)
	fetchStripStyleRe    = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`)
	fetchStripNavRe      = regexp.MustCompile(`(?is)<nav\b[^>]*>.*?</nav>`)
	fetchStripHeaderRe   = regexp.MustCompile(`(?is)<header\b[^>]*>.*?</header>`)
	fetchStripFooterRe   = regexp.MustCompile(`(?is)<footer\b[^>]*>.*?</footer>`)
	fetchStripAsideRe    = regexp.MustCompile(`(?is)<aside\b[^>]*>.*?</aside>`)
	fetchStripFormRe     = regexp.MustCompile(`(?is)<form\b[^>]*>.*?</form>`)
	fetchStripNoscriptRe = regexp.MustCompile(`(?is)<noscript\b[^>]*>.*?</noscript>`)
	fetchStripIframeRe   = regexp.MustCompile(`(?is)<iframe\b[^>]*>.*?</iframe>`)
	fetchStripSvgRe      = regexp.MustCompile(`(?is)<svg\b[^>]*>.*?</svg>`)
	// 优先抽 <main> 或 <article>,认为是正文
	fetchMainRe = regexp.MustCompile(`(?is)<(main|article)\b[^>]*>(.*?)</(main|article)>`)
	// 兜底 <body>
	fetchBodyRe = regexp.MustCompile(`(?is)<body\b[^>]*>(.*?)</body>`)
	// 保留结构感的换行(br / p / li / h1-6 / div 末尾)
	fetchBRRe   = regexp.MustCompile(`(?i)<br\s*/?>`)
	fetchPEndRe = regexp.MustCompile(`(?i)</p\s*>`)
	fetchLiRe   = regexp.MustCompile(`(?i)</li\s*>`)
	fetchHRe    = regexp.MustCompile(`(?i)</h[1-6]\s*>`)
	fetchDivRe  = regexp.MustCompile(`(?i)</div\s*>`)
	// 剩余 HTML 标签全删
	fetchTagRe = regexp.MustCompile(`<[^>]+>`)
	// 整理空白:多个空格折成一个,多于 2 个换行折成 2 个
	fetchSpacesRe = regexp.MustCompile(`[ \t]+`)
	fetchNlRe     = regexp.MustCompile(`\n{3,}`)
)

// extractMainText 把 HTML 转成可读纯文本。
// 流程:
//  1. 删除 script/style/nav/header/footer/aside/form/noscript/iframe/svg 整段
//  2. 优先抽 <main>/<article>,否则抽 <body>
//  3. 把 <br>/<p>/<li>/<h*>/<div> 末尾换成换行,保留段落结构
//  4. 删除剩余所有 HTML 标签
//  5. 解码 HTML entity (&amp; / &nbsp; / &#x2014;)
//  6. 折叠多余空白
func extractMainText(html string) string {
	for _, re := range []*regexp.Regexp{
		fetchStripScriptRe, fetchStripStyleRe, fetchStripNavRe,
		fetchStripHeaderRe, fetchStripFooterRe, fetchStripAsideRe,
		fetchStripFormRe, fetchStripNoscriptRe, fetchStripIframeRe,
		fetchStripSvgRe,
	} {
		html = re.ReplaceAllString(html, "")
	}

	if m := fetchMainRe.FindStringSubmatch(html); m != nil {
		html = m[2]
	} else if m := fetchBodyRe.FindStringSubmatch(html); m != nil {
		html = m[1]
	}

	html = fetchBRRe.ReplaceAllString(html, "\n")
	html = fetchPEndRe.ReplaceAllString(html, "\n\n")
	html = fetchLiRe.ReplaceAllString(html, "\n")
	html = fetchHRe.ReplaceAllString(html, "\n\n")
	html = fetchDivRe.ReplaceAllString(html, "\n")

	html = fetchTagRe.ReplaceAllString(html, "")
	html = stdhtml.UnescapeString(html)

	html = fetchSpacesRe.ReplaceAllString(html, " ")
	html = fetchNlRe.ReplaceAllString(html, "\n\n")
	return strings.TrimSpace(html)
}

package tools

import (
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// === 公共类型 ===

// webResult 是单条搜索结果的归一化结构。
type webResult struct {
	Title   string
	URL     string
	Snippet string
}

// === 工具入口 ===

// WebSearch 是 OpenAI-style 工具入口,被 tools.go 的工具表注册并由 LLM 调用。
// 后端固定走 Bing HTML 抓取(先试 cn.bing.com 再试 www.bing.com),零配置、无需任何 API key。
func WebSearch(args map[string]any) ToolResult {
	query, _ := args["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return ToolResult{Output: "query 不能为空", Success: false}
	}
	maxResults := toInt(args["max_results"], 5)
	if maxResults <= 0 {
		maxResults = 5
	}
	if maxResults > 15 {
		maxResults = 15
	}

	results, err := (&bingProvider{}).search(query, maxResults)
	if err != nil {
		return ToolResult{
			Output:  fmt.Sprintf("搜索失败 (Bing): %v\n\n可能是当前网络无法访问 Bing,稍后重试或检查网络。", err),
			Success: false,
		}
	}
	if len(results) == 0 {
		return ToolResult{Output: fmt.Sprintf("\"%s\" 无结果", query), Success: true}
	}
	return ToolResult{Output: formatWebResults(query, results), Success: true}
}

func formatWebResults(query string, results []webResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "搜索 \"%s\" 找到 %d 条结果:\n\n", query, len(results))
	for i, r := range results {
		fmt.Fprintf(&sb, "%d. %s\n   %s\n", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&sb, "   %s\n", r.Snippet)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// === Bing HTML 抓取(唯一后端,零配置)===

type bingProvider struct{}

func (b *bingProvider) search(query string, n int) ([]webResult, error) {
	// 先尝试国内域名 cn.bing.com,失败回退国际版 www.bing.com。
	// cn.bing.com 在国内 ISP 通常直连;海外用户走国际版。
	var lastErr error
	for _, host := range []string{"cn.bing.com", "www.bing.com"} {
		results, err := bingFetch(host, query, n)
		if err == nil && len(results) > 0 {
			return results, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("两个 Bing 域名都返回 0 条结果")
	}
	return nil, lastErr
}

func bingFetch(host, query string, n int) ([]webResult, error) {
	u := fmt.Sprintf("https://%s/search?q=%s&count=%d&FORM=PERE",
		host, url.QueryEscape(query), n)
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	// UA 必须像真实浏览器,否则 Bing 返回纯净空页或挑战页
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 "+
			"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, host)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseBingHTML(string(body), n)
}

// parseBingHTML 用正则从 Bing 结果页 HTML 提取搜索结果。
// Bing 的 DOM 结构经常微调,这里只锚定相对稳定的几个标签 (b_algo / h2 a / b_caption)。
// 任何一项解不到就跳过该结果,不让一个坏结构拖垮整体。
// 注意:Bing 实际输出的 <li class="b_algo" data-id ... iid=SERP.xxx> 会带额外属性,
// 任何对 class 后字符的固定假设都会失配。所有结果块的 class= 后用 [^>]* 容忍后续属性。
var (
	bingBlockRe      = regexp.MustCompile(`(?s)<li class="b_algo"[^>]*>.*?</li>`)
	bingTitleRe      = regexp.MustCompile(`(?s)<h2[^>]*>.*?<a[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	bingSnippetRe    = regexp.MustCompile(`(?s)<p[^>]*class="[^"]*b_lineclamp[^"]*"[^>]*>(.*?)</p>`)
	bingSnippetAltRe = regexp.MustCompile(`(?s)<div[^>]*class="[^"]*b_caption[^"]*"[^>]*>.*?<p[^>]*>(.*?)</p>`)
	tagRe            = regexp.MustCompile(`<[^>]+>`)
	wsRe             = regexp.MustCompile(`\s+`)
)

func parseBingHTML(htmlBody string, n int) ([]webResult, error) {
	var results []webResult
	for _, block := range bingBlockRe.FindAllString(htmlBody, -1) {
		if len(results) >= n {
			break
		}
		var r webResult
		if m := bingTitleRe.FindStringSubmatch(block); m != nil {
			r.URL = stdhtml.UnescapeString(strings.TrimSpace(m[1]))
			r.Title = cleanHTMLText(m[2])
		}
		if m := bingSnippetRe.FindStringSubmatch(block); m != nil {
			r.Snippet = cleanHTMLText(m[1])
		} else if m := bingSnippetAltRe.FindStringSubmatch(block); m != nil {
			r.Snippet = cleanHTMLText(m[1])
		}
		if r.Title != "" && r.URL != "" {
			results = append(results, r)
		}
	}
	if len(results) == 0 {
		return nil, errors.New("HTML 结构变化导致 0 条结果可解析")
	}
	return results, nil
}

func cleanHTMLText(s string) string {
	s = tagRe.ReplaceAllString(s, "")
	s = stdhtml.UnescapeString(s)
	s = wsRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

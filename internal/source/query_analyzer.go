package source

import (
	"regexp"
	"strings"
	"unicode"
)

// QueryAnalysis is the normalized search intent passed to token search.
type QueryAnalysis struct {
	Original       string   `json:"original"`
	Tokens         []string `json:"tokens"`
	Phrases        []string `json:"phrases"`
	TechnicalTerms []string `json:"technicalTerms"`
	Analyzer       string   `json:"analyzer"`
}

// QueryAnalyzer allows a future model-backed analyzer without coupling search
// execution to a model provider. The local implementation is deterministic.
type QueryAnalyzer interface {
	Analyze(string) QueryAnalysis
}

type LocalQueryAnalyzer struct{}

var technicalSynonyms = map[string][]string{
	"库存": {"stock", "stock_record"}, "扣": {"decreaseStock"}, "扣减": {"decreaseStock"},
	"配送": {"distribution", "deliver"}, "调拨": {"transfer"}, "门店": {"store"},
	"收货": {"receiving"}, "出库": {"distribution_out"}, "复核": {"out_chk"},
	"审核": {"audit", "review"}, "流程": {"flow", "process"},
}

var chineseTerms = []string{
	"配送", "调拨", "门店", "收货", "流程", "库存", "扣减", "审核", "通过", "出库", "复核",
	"服务", "记录", "设计", "订单", "商品", "入库", "采购", "发货", "退货", "供应链", "分配",
	"查询", "创建", "更新", "删除", "修改", "读取", "执行", "验证", "项目", "测试", "代码",
	"接口", "模块", "数据", "状态", "任务", "工作区", "认证", "授权", "日志", "配置",
}

var identifierPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_]*|[0-9]+`)

func (LocalQueryAnalyzer) Analyze(query string) QueryAnalysis {
	analysis := QueryAnalysis{Original: query, Analyzer: "local"}
	addToken := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || isStopToken(value) {
			return
		}
		analysis.Tokens = appendUnique(analysis.Tokens, value)
	}
	addTechnical := func(value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			analysis.TechnicalTerms = appendUnique(analysis.TechnicalTerms, value)
		}
	}

	for _, match := range identifierPattern.FindAllString(query, -1) {
		addToken(match)
		if strings.ContainsAny(match, "_0123456789") || hasUppercase(match[1:]) {
			addTechnical(match)
		}
		for _, part := range strings.FieldsFunc(match, func(r rune) bool { return r == '_' || r == '-' }) {
			addToken(part)
		}
	}

	for index := 0; index < len([]rune(query)); {
		runes := []rune(query)
		if !unicode.Is(unicode.Han, runes[index]) {
			index++
			continue
		}
		end := index
		for end < len(runes) && unicode.Is(unicode.Han, runes[end]) {
			end++
		}
		segment := string(runes[index:end])
		terms := segmentChinese(segment)
		for _, term := range terms {
			addToken(term.text)
			for _, synonym := range technicalSynonyms[term.text] {
				addTechnical(synonym)
			}
		}
		for termIndex := 0; termIndex+1 < len(terms); termIndex++ {
			if terms[termIndex].end == terms[termIndex+1].start {
				analysis.Phrases = appendUnique(analysis.Phrases, terms[termIndex].text+terms[termIndex+1].text)
			}
		}
		index = end
	}

	for _, token := range analysis.Tokens {
		if synonyms := technicalSynonyms[token]; len(synonyms) > 0 {
			for _, synonym := range synonyms {
				addTechnical(synonym)
			}
		}
	}
	return analysis
}

func AnalyzeQuery(query string) QueryAnalysis {
	return (LocalQueryAnalyzer{}).Analyze(query)
}

type chineseTerm struct {
	text       string
	start, end int
}

func segmentChinese(segment string) []chineseTerm {
	runes := []rune(segment)
	terms := make([]chineseTerm, 0)
	for index := 0; index < len(runes); {
		best := ""
		for _, candidate := range chineseTerms {
			if len([]rune(candidate)) > len(best) && index+len([]rune(candidate)) <= len(runes) && string(runes[index:index+len([]rune(candidate))]) == candidate {
				best = candidate
			}
		}
		if best != "" {
			end := index + len([]rune(best))
			terms = append(terms, chineseTerm{text: best, start: index, end: end})
			index = end
			continue
		}
		if !isChineseStopRune(runes[index]) {
			terms = append(terms, chineseTerm{text: string(runes[index]), start: index, end: index + 1})
		}
		index++
	}
	return terms
}

func isChineseStopRune(value rune) bool {
	return strings.ContainsRune("的与和及了是为什么怎么何时哪在将把对中从到被并或", value)
}

func isStopToken(value string) bool {
	return len([]rune(value)) == 1 && isChineseStopRune([]rune(value)[0])
}

func hasUppercase(value string) bool {
	for _, r := range value {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

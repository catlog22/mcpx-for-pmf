package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyzeChineseBusinessQuery(t *testing.T) {
	analysis := AnalyzeQuery("ERP配送调拨与门店收货流程")
	for _, token := range []string{"ERP", "配送", "调拨", "门店", "收货", "流程"} {
		if !containsString(analysis.Tokens, token) {
			t.Fatalf("missing token %q: %+v", token, analysis)
		}
	}
	if !containsString(analysis.Phrases, "配送调拨") || !containsString(analysis.Phrases, "门店收货") {
		t.Fatalf("missing business phrases: %+v", analysis)
	}
}

func TestSmartQueryRanksChineseFilenameAndTechnicalSymbol(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"05-ERP配送调拨与门店收货流程.md":              "# ERP配送调拨与门店收货流程\n门店收货需要完成配送调拨。\n",
		"库存管理设计.md":                         "# 库存管理设计\n库存设计说明。\n",
		"ErpDistributionOutChkService.java": "class ErpDistributionOutChkService {\n  String table = \"erp_distribution_out_chk\";\n}\n",
	}
	writeSmartFiles(t, root, files)

	business, err := SmartQueryPage(root, SmartQueryOptions{Query: "ERP配送调拨与门店收货流程", Mode: "smart", Parallel: true, MaxResults: 5})
	if err != nil {
		t.Fatal(err)
	}
	businessFiles := business["files"].([]map[string]any)
	if len(businessFiles) == 0 || businessFiles[0]["path"] != "05-ERP配送调拨与门店收货流程.md" {
		t.Fatalf("business filename was not ranked first: %+v", businessFiles)
	}

	technical, err := SmartQueryPage(root, SmartQueryOptions{Query: "erp_distribution_out_chk", Mode: "smart", Parallel: false, MaxResults: 5})
	if err != nil {
		t.Fatal(err)
	}
	technicalFiles := technical["files"].([]map[string]any)
	if len(technicalFiles) == 0 || technicalFiles[0]["path"] != "ErpDistributionOutChkService.java" {
		t.Fatalf("technical symbol was not ranked first: %+v", technicalFiles)
	}
	if !containsString(technicalFiles[0]["source"].([]string), "exact") {
		t.Fatalf("technical result missing exact source: %+v", technicalFiles[0])
	}
}

func TestSmartQueryNaturalLanguageUsesSynonyms(t *testing.T) {
	root := t.TempDir()
	writeSmartFiles(t, root, map[string]string{
		"配送流程.md":                    "# 配送流程\n审核通过以后会扣减库存。\n",
		"StockService.java":          "class StockService { void decreaseStock() {} }\n",
		"ErpDistributionOutChk.java": "配送出库复核代码。\n",
	})
	result, err := SmartQueryPage(root, SmartQueryOptions{Query: "配送审核通过为什么扣库存", Mode: "token", MaxResults: 10})
	if err != nil {
		t.Fatal(err)
	}
	files := result["files"].([]map[string]any)
	if len(files) < 2 {
		t.Fatalf("natural language query returned too few results: %+v", result)
	}
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file["path"].(string))
	}
	if !containsString(paths, "配送流程.md") || !containsString(paths, "StockService.java") {
		t.Fatalf("synonym results missed business or code file: %+v", paths)
	}
}

func TestSmartQueryRanksChineseTargetWindowAndExplainsRanking(t *testing.T) {
	root := t.TempDir()
	writeSmartFiles(t, root, map[string]string{
		"docs/player.md": strings.Join([]string{
			"# 播放器说明",
			"这是文件开头的泛化介绍。",
			"还有一些与交互无关的背景。",
			"继续描述页面结构。",
			"键盘控制可以暂停播放、重置进度和调整速度。",
			"最后补充其他说明。",
		}, "\n") + "\n",
		"docs/keyboard.md": "# 键盘说明\n键盘控制用于页面导航和焦点移动。\n",
		"docs/speed.md":    "# 速度说明\n速度设置会影响动画。\n",
	})

	first, err := SmartQueryPage(root, SmartQueryOptions{
		Query: "键盘控制暂停播放、重置和速度", Mode: "smart", MaxResults: 3,
		ContextBefore: 1, ContextAfter: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	files := first["files"].([]map[string]any)
	if len(files) == 0 || files[0]["path"] != "docs/player.md" {
		t.Fatalf("Chinese target paragraph was not ranked first: %+v", files)
	}
	if !strings.Contains(files[0]["content"].(string), "键盘控制可以暂停播放、重置进度和调整速度") {
		t.Fatalf("first window did not center the target paragraph: %+v", files[0])
	}
	if files[0]["offset"].(int) == 0 {
		t.Fatalf("context window still starts at the file head: %+v", files[0])
	}
	if files[0]["rank"] != 1 {
		t.Fatalf("rank=%v, want 1: %+v", files[0]["rank"], files[0])
	}
	matchReason, _ := files[0]["match_reason"].(string)
	if files[0]["rank"] != 1 || matchReason == "" {
		t.Fatalf("rank explanation missing: %+v", files[0])
	}
}

func writeSmartFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

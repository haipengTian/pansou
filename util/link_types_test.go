package util

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// LinkTypes 供管理后台校验"允许的网盘类型"。GetLinkType 新增返回值却忘了同步时，
// 管理员将无法在后台勾选该类型，所以这里直接从源码里比对。
func TestLinkTypesCoversGetLinkType(t *testing.T) {
	src, err := os.ReadFile("regex_util.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func GetLinkType(")
	if start < 0 {
		t.Fatal("找不到 GetLinkType")
	}
	end := strings.Index(body[start:], "\n}")
	fn := body[start : start+end]

	known := map[string]bool{}
	for _, lt := range LinkTypes {
		known[lt] = true
	}
	for _, m := range regexp.MustCompile(`return "([a-z0-9]+)"`).FindAllStringSubmatch(fn, -1) {
		if !known[m[1]] {
			t.Errorf("GetLinkType 会返回 %q，但 LinkTypes 中没有", m[1])
		}
	}
}

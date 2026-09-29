package config

import (
	"strings"
	"testing"
)

// 未设置 AUTH_JWT_SECRET 时，原实现用"固定前缀 + 启动时间戳"作密钥，
// 知道大致启动时间就能伪造任意用户（含管理员）的令牌。必须改为密码学随机值。
func TestDefaultJWTSecretIsRandom(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "")

	first, second := getAuthJWTSecret(), getAuthJWTSecret()
	if first == second {
		t.Fatal("两次生成的默认密钥不应相同")
	}
	if strings.HasPrefix(first, "pansou-default-secret-") {
		t.Fatalf("默认密钥不应再是可预测的时间戳格式: %s", first)
	}
	if len(first) < 32 {
		t.Fatalf("默认密钥过短: %d", len(first))
	}
}

func TestExplicitJWTSecretIsUsed(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "configured-secret")
	if got := getAuthJWTSecret(); got != "configured-secret" {
		t.Fatalf("得到 %q", got)
	}
}

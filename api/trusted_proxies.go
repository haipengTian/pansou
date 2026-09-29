package api

import (
	"fmt"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// defaultTrustedProxies 默认只信任本机与私有网段的反向代理：
// 一体镜像里请求经 宿主机 Nginx → Docker 网桥 → 容器内 Nginx → 后端，这些跳都在其中。
var defaultTrustedProxies = []string{
	"127.0.0.0/8",
	"::1/128",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
}

// configureTrustedProxies 让 gin 只从可信代理转发的 X-Forwarded-For 中取客户端 IP。
//
// gin 默认信任所有代理，ClientIP() 会直接采用客户端可随意伪造的 X-Forwarded-For，
// 登录限速按 IP 计数也就形同虚设。部署拓扑不同时可用 TRUSTED_PROXIES（逗号分隔 CIDR/IP）覆盖。
func configureTrustedProxies(r *gin.Engine, proxies []string) {
	if err := r.SetTrustedProxies(proxies); err != nil {
		fmt.Printf("⚠️  TRUSTED_PROXIES 配置无效（%v），改为不信任任何代理\n", err)
		_ = r.SetTrustedProxies(nil)
	}
}

// trustedProxiesFromEnv 读取 TRUSTED_PROXIES，未设置时使用默认值。
func trustedProxiesFromEnv() []string {
	raw := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES"))
	if raw == "" {
		return defaultTrustedProxies
	}
	var proxies []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			proxies = append(proxies, item)
		}
	}
	return proxies
}

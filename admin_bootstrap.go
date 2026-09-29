package main

import (
	"fmt"
	"log"
	"os"

	"pansou/admin/store"
	"pansou/config"
)

// defaultDataDir 是后台数据（settings.json、users.json）的默认目录，相对于工作目录。
// 一体镜像中工作目录为 /app，即落在 /app/data 持久卷内。
const defaultDataDir = "./data"

// openAdminStore 打开后台存储。存储不可用意味着账号与设置都无法生效，因此直接终止启动。
func openAdminStore() *store.Store {
	dir := os.Getenv("PANSOU_DATA_DIR")
	if dir == "" {
		dir = defaultDataDir
	}

	seed := store.Settings{
		EnabledPlugins:  config.AppConfig.EnabledPlugins,
		DefaultChannels: config.AppConfig.DefaultChannels,
	}
	s, err := store.Open(dir, seed, envAccounts(config.AppConfig))
	if err != nil {
		log.Fatalf("打开后台数据目录 %s 失败: %v", dir, err)
	}

	if s.SeededFromEnv() {
		fmt.Printf("后台设置: 首次启动，已根据 ENABLED_PLUGINS/CHANNELS 生成 %s/settings.json\n", dir)
	} else {
		fmt.Printf("后台设置: 使用 %s/settings.json（ENABLED_PLUGINS/CHANNELS 环境变量不再生效，请在管理后台修改）\n", dir)
	}
	if !hasActiveAdmin(s) {
		fmt.Println("⚠️  未配置任何管理员账号，管理后台不可用。请设置 ADMIN_USERS=用户名:密码")
	}
	return s
}

// envAccounts 决定环境变量账号的角色。
//
// 设置了 ADMIN_USERS 时：ADMIN_USERS 为管理员，AUTH_USERS 为普通用户。
// 未设置时：AUTH_USERS 视为管理员，保证升级前唯一的账号仍能进入后台。
func envAccounts(cfg *config.Config) store.EnvAccounts {
	if len(cfg.AdminUsers) > 0 {
		return store.EnvAccounts{Admins: cfg.AdminUsers, Users: cfg.AuthUsers}
	}
	return store.EnvAccounts{Admins: cfg.AuthUsers}
}

func hasActiveAdmin(s *store.Store) bool {
	for _, u := range s.Users() {
		if u.Role == store.RoleAdmin && !u.Disabled {
			return true
		}
	}
	return false
}

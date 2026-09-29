package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"pansou/admin/store"
	"pansou/config"
	"pansou/stats"
)

// statsFileName 是搜索历史与访问统计数据库的文件名，与后台设置放在同一数据目录。
const statsFileName = "stats.db"

// adminDataDir 返回后台数据目录。
func adminDataDir() string {
	if dir := os.Getenv("PANSOU_DATA_DIR"); dir != "" {
		return dir
	}
	return defaultDataDir
}

// openStats 打开统计数据库并启动异步记录器。失败时返回 nil，统计功能关闭、搜索不受影响。
func openStats() (*stats.Store, *stats.Recorder) {
	path := filepath.Join(adminDataDir(), statsFileName)
	st, err := stats.Open(path)
	if err != nil {
		fmt.Printf("⚠️  统计数据库不可用，搜索历史与访问统计已关闭: %v\n", err)
		return nil, nil
	}
	fmt.Printf("访问统计: 使用 %s\n", path)
	return st, stats.NewRecorder(st, stats.RecorderOptions{})
}

// defaultDataDir 是后台数据（settings.json、users.json）的默认目录，相对于工作目录。
// 一体镜像中工作目录为 /app，即落在 /app/data 持久卷内。
const defaultDataDir = "./data"

// openAdminStore 打开后台存储。存储不可用意味着账号与设置都无法生效，因此直接终止启动。
func openAdminStore() *store.Store {
	dir := adminDataDir()

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

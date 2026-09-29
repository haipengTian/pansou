// Package store 持久化管理后台的两类数据：运行期可改的搜索设置与登录账号。
//
// 数据量很小（几十个插件名、几百个频道、几十个账号），所以直接存成两个 JSON 文件，
// 便于人工查看与备份。所有写入都走"写临时文件 + rename"，进程在写一半时崩溃也不会
// 留下半截文件；内存里只持有不可变快照，读方拿到的永远是一份完整、独立的副本。
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	json "pansou/util/json"
)

const (
	settingsFileName = "settings.json"
	usersFileName    = "users.json"
)

// Store 是设置与账号的唯一读写入口，可被多个 goroutine 并发使用。
type Store struct {
	dir string

	mu            sync.RWMutex
	settings      Settings
	seededFromEnv bool
	dbUsers       map[string]User // 持久化到 users.json
	envUsers      map[string]User // 仅来自环境变量，不落盘
}

// Open 加载 dir 下的设置与账号。
//
// settings.json 不存在时用 seed（即当前环境变量的取值）生成并落盘，保证升级后行为不变；
// 文件存在则以文件为准。文件损坏时返回错误而不是回落到 seed——静默回落会让管理员
// 在后台做过的修改在一次重启后悄悄消失。
func Open(dir string, seed Settings, env EnvAccounts) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}

	envUsers, err := env.toUsers()
	if err != nil {
		return nil, err
	}

	s := &Store{dir: dir, envUsers: envUsers, dbUsers: map[string]User{}}

	if err := s.loadSettings(seed); err != nil {
		return nil, err
	}
	if err := s.loadUsers(); err != nil {
		return nil, err
	}
	return s, nil
}

// SeededFromEnv 报告本次启动是否因为缺少 settings.json 而从环境变量生成了设置。
func (s *Store) SeededFromEnv() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.seededFromEnv
}

func (s *Store) loadSettings(seed Settings) error {
	var loaded Settings
	found, err := readJSON(filepath.Join(s.dir, settingsFileName), &loaded)
	if err != nil {
		return fmt.Errorf("读取 %s 失败: %w", settingsFileName, err)
	}
	if found {
		s.settings = loaded.normalized()
		return nil
	}

	s.settings = seed.normalized()
	s.seededFromEnv = true
	if err := writeJSONAtomic(filepath.Join(s.dir, settingsFileName), s.settings); err != nil {
		return fmt.Errorf("写入初始 %s 失败: %w", settingsFileName, err)
	}
	return nil
}

func (s *Store) loadUsers() error {
	var file usersFile
	found, err := readJSON(filepath.Join(s.dir, usersFileName), &file)
	if err != nil {
		return fmt.Errorf("读取 %s 失败: %w", usersFileName, err)
	}
	if !found {
		return nil
	}
	for _, u := range file.Users {
		// 与环境账号重名时以环境账号为准：环境账号是救急通道，不能被文件覆盖。
		if _, isEnv := s.envUsers[u.Username]; isEnv {
			continue
		}
		u.Source = SourceDB
		s.dbUsers[u.Username] = u
	}
	return nil
}

// readJSON 读取并解析 JSON 文件；文件不存在时返回 found=false 且无错误。
func readJSON(path string, v interface{}) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, err
	}
	return true, nil
}

// writeJSONAtomic 先写同目录临时文件并 fsync，再 rename 覆盖目标文件。
func writeJSONAtomic(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

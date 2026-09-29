package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Role 是账号角色。
type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

// Source 标记账号来源。
type Source string

const (
	// SourceEnv 来自 ADMIN_USERS / AUTH_USERS 环境变量，后台只读，作为救急通道。
	SourceEnv Source = "env"
	// SourceDB 由管理员在后台创建，保存在 users.json。
	SourceDB Source = "db"
)

const (
	minPasswordLen = 8
	// bcrypt 只使用前 72 字节，超出部分会被静默忽略，所以直接拒绝。
	maxPasswordLen = 72
)

var (
	ErrInvalidCredentials = errors.New("用户名或密码错误")
	ErrUserDisabled       = errors.New("账号已被禁用")
	ErrUserExists         = errors.New("用户名已存在")
	ErrUserNotFound       = errors.New("用户不存在")
	ErrEnvUserReadOnly    = errors.New("环境变量账号只读，请修改部署配置")
	ErrLastAdmin          = errors.New("至少需要保留一个可用的管理员")
	ErrInvalidUsername    = errors.New("用户名需为 3-32 位字母、数字、点、下划线或短横线")
	ErrInvalidPassword    = fmt.Errorf("密码长度需在 %d-%d 字节之间", minPasswordLen, maxPasswordLen)
	ErrInvalidRole        = errors.New("角色只能是 admin 或 user")
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,32}$`)

// bcryptCost 允许测试调低以加快运行。
var bcryptCost = bcrypt.DefaultCost

// 未知用户名时也做一次等价的 bcrypt 比较，避免通过响应时间枚举用户名。
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("pansou-dummy-password"), bcrypt.DefaultCost)

// User 是一个登录账号。
type User struct {
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	Role         Role      `json:"role"`
	Disabled     bool      `json:"disabled"`
	TokenVersion int       `json:"token_version"`
	Source       Source    `json:"source"`
	CreatedAt    time.Time `json:"created_at"`
	LastLoginAt  time.Time `json:"last_login_at"`
}

// UserPatch 描述对账号的部分修改；nil 字段表示不修改。
type UserPatch struct {
	Role     *Role
	Disabled *bool
	Password *string
}

// EnvAccounts 是从环境变量解析出的明文账号，启动时哈希后只保存在内存里。
type EnvAccounts struct {
	Admins map[string]string
	Users  map[string]string
}

type usersFile struct {
	Users []User `json:"users"`
}

func (e EnvAccounts) toUsers() (map[string]User, error) {
	users := make(map[string]User, len(e.Admins)+len(e.Users))
	add := func(accounts map[string]string, role Role) error {
		for name, password := range accounts {
			if _, exists := users[name]; exists {
				continue // 同时出现在管理员与普通用户里时，以先加入的管理员为准
			}
			hash, err := bcrypt.GenerateFromPassword(bcryptInput(password), bcryptCost)
			if err != nil {
				return fmt.Errorf("处理环境账号 %s 失败: %w", name, err)
			}
			users[name] = User{
				Username:     name,
				PasswordHash: string(hash),
				Role:         role,
				TokenVersion: envTokenVersion(name, password, role),
				Source:       SourceEnv,
			}
		}
		return nil
	}
	if err := add(e.Admins, RoleAdmin); err != nil {
		return nil, err
	}
	if err := add(e.Users, RoleUser); err != nil {
		return nil, err
	}
	return users, nil
}

// Authenticate 校验用户名与密码，成功时返回账号。
func (s *Store) Authenticate(username, password string) (User, error) {
	u, ok := s.GetUser(username)
	if !ok {
		_ = bcrypt.CompareHashAndPassword(dummyHash, bcryptInput(password))
		return User{}, ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), bcryptInput(password)); err != nil {
		return User{}, ErrInvalidCredentials
	}
	if u.Disabled {
		return User{}, ErrUserDisabled
	}
	return u, nil
}

// GetUser 按用户名查找账号（环境账号优先）。
func (s *Store) GetUser(username string) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lookupLocked(username)
}

func (s *Store) lookupLocked(username string) (User, bool) {
	if u, ok := s.envUsers[username]; ok {
		return u, true
	}
	u, ok := s.dbUsers[username]
	return u, ok
}

// Users 返回按用户名排序的全部账号。
func (s *Store) Users() []User {
	s.mu.RLock()
	defer s.mu.RUnlock()

	users := make([]User, 0, len(s.envUsers)+len(s.dbUsers))
	for _, u := range s.envUsers {
		users = append(users, u)
	}
	for _, u := range s.dbUsers {
		users = append(users, u)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Username < users[j].Username })
	return users
}

// CreateUser 新建一个后台账号并落盘。
func (s *Store) CreateUser(username, password string, role Role) (User, error) {
	if !usernamePattern.MatchString(username) {
		return User{}, ErrInvalidUsername
	}
	if err := validatePassword(password); err != nil {
		return User{}, err
	}
	if role != RoleAdmin && role != RoleUser {
		return User{}, ErrInvalidRole
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return User{}, fmt.Errorf("生成密码哈希失败: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.lookupLocked(username); exists {
		return User{}, ErrUserExists
	}
	u := User{
		Username:     username,
		PasswordHash: string(hash),
		Role:         role,
		TokenVersion: newTokenVersion(),
		Source:       SourceDB,
		CreatedAt:    time.Now(),
	}
	if err := s.commitUserLocked(u); err != nil {
		return User{}, err
	}
	return u, nil
}

// UpdateUser 修改角色、禁用状态或密码。任何一项变化都会递增 TokenVersion，
// 让该账号已签发的令牌立即失效。
func (s *Store) UpdateUser(username string, patch UserPatch) (User, error) {
	var newHash string
	if patch.Password != nil {
		if err := validatePassword(*patch.Password); err != nil {
			return User{}, err
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(*patch.Password), bcryptCost)
		if err != nil {
			return User{}, fmt.Errorf("生成密码哈希失败: %w", err)
		}
		newHash = string(hash)
	}
	if patch.Role != nil && *patch.Role != RoleAdmin && *patch.Role != RoleUser {
		return User{}, ErrInvalidRole
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	current, err := s.editableUserLocked(username)
	if err != nil {
		return User{}, err
	}

	next := current
	if patch.Role != nil {
		next.Role = *patch.Role
	}
	if patch.Disabled != nil {
		next.Disabled = *patch.Disabled
	}
	if newHash != "" {
		next.PasswordHash = newHash
	}

	losesAdmin := isActiveAdmin(current) && !isActiveAdmin(next)
	if losesAdmin && s.activeAdminCountLocked() <= 1 {
		return User{}, ErrLastAdmin
	}

	next.TokenVersion = current.TokenVersion + 1
	if err := s.commitUserLocked(next); err != nil {
		return User{}, err
	}
	return next, nil
}

// DeleteUser 删除后台账号。
func (s *Store) DeleteUser(username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	current, err := s.editableUserLocked(username)
	if err != nil {
		return err
	}
	if isActiveAdmin(current) && s.activeAdminCountLocked() <= 1 {
		return ErrLastAdmin
	}

	next := make(map[string]User, len(s.dbUsers))
	for name, u := range s.dbUsers {
		if name != username {
			next[name] = u
		}
	}
	if err := s.persistUsersLocked(next); err != nil {
		return err
	}
	s.dbUsers = next
	return nil
}

// RecordLogin 记录最近登录时间。db 账号会落盘；落盘失败只影响这个展示字段，不阻断登录。
func (s *Store) RecordLogin(username string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if u, ok := s.envUsers[username]; ok {
		u.LastLoginAt = at
		next := make(map[string]User, len(s.envUsers))
		for name, existing := range s.envUsers {
			next[name] = existing
		}
		next[username] = u
		s.envUsers = next
		return
	}
	if u, ok := s.dbUsers[username]; ok {
		u.LastLoginAt = at
		if err := s.commitUserLocked(u); err != nil {
			fmt.Printf("[admin] 记录 %s 最近登录时间失败: %v\n", username, err)
		}
	}
}

func (s *Store) editableUserLocked(username string) (User, error) {
	if _, isEnv := s.envUsers[username]; isEnv {
		return User{}, ErrEnvUserReadOnly
	}
	u, ok := s.dbUsers[username]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return u, nil
}

// commitUserLocked 以写时复制的方式更新 dbUsers 并落盘；落盘失败时内存保持原状。
func (s *Store) commitUserLocked(u User) error {
	next := make(map[string]User, len(s.dbUsers)+1)
	for name, existing := range s.dbUsers {
		next[name] = existing
	}
	next[u.Username] = u
	if err := s.persistUsersLocked(next); err != nil {
		return err
	}
	s.dbUsers = next
	return nil
}

func (s *Store) persistUsersLocked(users map[string]User) error {
	list := make([]User, 0, len(users))
	for _, u := range users {
		list = append(list, u)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Username < list[j].Username })
	if err := writeJSONAtomic(filepath.Join(s.dir, usersFileName), usersFile{Users: list}); err != nil {
		return fmt.Errorf("保存账号失败: %w", err)
	}
	return nil
}

func (s *Store) activeAdminCountLocked() int {
	count := 0
	for _, u := range s.envUsers {
		if isActiveAdmin(u) {
			count++
		}
	}
	for _, u := range s.dbUsers {
		if isActiveAdmin(u) {
			count++
		}
	}
	return count
}

func isActiveAdmin(u User) bool {
	return u.Role == RoleAdmin && !u.Disabled
}

// bcryptInput 把超过 bcrypt 72 字节上限的口令先做 SHA-256，保证长口令整体参与校验。
// 后台创建的账号本身限制在 72 字节以内，只有环境变量里的历史长口令会走这条路径。
func bcryptInput(password string) []byte {
	if len(password) <= maxPasswordLen {
		return []byte(password)
	}
	sum := sha256.Sum256([]byte(password))
	return []byte(hex.EncodeToString(sum[:]))
}

// newTokenVersion 为新账号生成随机的起始令牌版本。
// 不能固定从 1 开始：删除后重建同名账号时，旧令牌的版本号会与新账号相同而"复活"。
func newTokenVersion() int {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic("生成令牌版本失败: " + err.Error())
	}
	return int(binary.BigEndian.Uint32(buf[:])&0x3fffffff) + 1
}

// envTokenVersion 由用户名、口令与角色派生环境账号的令牌版本：
// 环境账号不能在后台禁用，修改部署中的口令或角色并重启，就能让其旧令牌全部失效。
func envTokenVersion(username, password string, role Role) int {
	sum := sha256.Sum256([]byte(username + "\x00" + password + "\x00" + string(role)))
	return int(binary.BigEndian.Uint32(sum[:4])&0x3fffffff) + 1
}

func validatePassword(password string) error {
	if len(password) < minPasswordLen || len(password) > maxPasswordLen {
		return ErrInvalidPassword
	}
	return nil
}

package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEnvAdminsCanAuthenticate(t *testing.T) {
	s, _ := openTestStore(t, Settings{}, EnvAccounts{Admins: map[string]string{"root": "root-password"}})

	u, err := s.Authenticate("root", "root-password")
	if err != nil {
		t.Fatalf("环境管理员应能登录: %v", err)
	}
	if u.Role != RoleAdmin || u.Source != SourceEnv {
		t.Fatalf("role=%s source=%s, 期望 admin/env", u.Role, u.Source)
	}
	if u.PasswordHash == "root-password" {
		t.Fatal("环境账号密码也必须以哈希形式保存")
	}
}

func TestAuthenticateRejectsWrongPasswordAndUnknownUser(t *testing.T) {
	s, _ := openTestStore(t, Settings{}, EnvAccounts{Admins: map[string]string{"root": "root-password"}})

	if _, err := s.Authenticate("root", "nope"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("错误密码应返回 ErrInvalidCredentials，得到 %v", err)
	}
	if _, err := s.Authenticate("ghost", "whatever"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("不存在的用户应返回 ErrInvalidCredentials，得到 %v", err)
	}
}

func TestCreateUserPersistsAcrossReopen(t *testing.T) {
	s, dir := openTestStore(t, Settings{}, EnvAccounts{})

	created, err := s.CreateUser("alice", "alice-password", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	if created.Source != SourceDB || created.CreatedAt.IsZero() {
		t.Fatalf("新用户应为 db 来源并记录创建时间: %+v", created)
	}

	reopened, err := Open(dir, Settings{}, EnvAccounts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Authenticate("alice", "alice-password"); err != nil {
		t.Fatalf("重开后应能登录: %v", err)
	}
}

func TestCreateUserValidation(t *testing.T) {
	s, _ := openTestStore(t, Settings{}, EnvAccounts{Admins: map[string]string{"root": "root-password"}})

	cases := []struct {
		name, username, password string
		role                     Role
		want                     error
	}{
		{"用户名过短", "ab", "long-enough", RoleUser, ErrInvalidUsername},
		{"用户名含非法字符", "a b!", "long-enough", RoleUser, ErrInvalidUsername},
		{"密码过短", "bob", "short", RoleUser, ErrInvalidPassword},
		{"密码超出 bcrypt 上限", "bob", string(make([]byte, 73)), RoleUser, ErrInvalidPassword},
		{"未知角色", "bob", "long-enough", Role("owner"), ErrInvalidRole},
		{"与环境账号重名", "root", "long-enough", RoleUser, ErrUserExists},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.CreateUser(tc.username, tc.password, tc.role); !errors.Is(err, tc.want) {
				t.Fatalf("得到 %v, 期望 %v", err, tc.want)
			}
		})
	}

	if _, err := s.CreateUser("bob", "long-enough", RoleUser); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser("bob", "long-enough", RoleUser); !errors.Is(err, ErrUserExists) {
		t.Fatalf("重复创建应返回 ErrUserExists，得到 %v", err)
	}
}

func TestDisabledUserCannotAuthenticate(t *testing.T) {
	s, _ := openTestStore(t, Settings{}, EnvAccounts{Admins: map[string]string{"root": "root-password"}})
	if _, err := s.CreateUser("alice", "alice-password", RoleUser); err != nil {
		t.Fatal(err)
	}

	disabled := true
	if _, err := s.UpdateUser("alice", UserPatch{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate("alice", "alice-password"); !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("禁用用户应返回 ErrUserDisabled，得到 %v", err)
	}
}

func TestSecuritySensitiveChangesBumpTokenVersion(t *testing.T) {
	s, _ := openTestStore(t, Settings{}, EnvAccounts{Admins: map[string]string{"root": "root-password"}})
	u, _ := s.CreateUser("alice", "alice-password", RoleUser)
	version := u.TokenVersion

	newPassword := "brand-new-password"
	u, err := s.UpdateUser("alice", UserPatch{Password: &newPassword})
	if err != nil {
		t.Fatal(err)
	}
	if u.TokenVersion <= version {
		t.Fatal("重置密码应使旧令牌失效")
	}
	version = u.TokenVersion

	admin := RoleAdmin
	u, _ = s.UpdateUser("alice", UserPatch{Role: &admin})
	if u.TokenVersion <= version {
		t.Fatal("修改角色应使旧令牌失效")
	}
	version = u.TokenVersion

	disabled := true
	u, _ = s.UpdateUser("alice", UserPatch{Disabled: &disabled})
	if u.TokenVersion <= version {
		t.Fatal("禁用应使旧令牌失效")
	}

	if _, err := s.Authenticate("alice", newPassword); !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("已禁用，得到 %v", err)
	}
}

func TestEnvUsersAreReadOnly(t *testing.T) {
	s, _ := openTestStore(t, Settings{}, EnvAccounts{
		Admins: map[string]string{"root": "root-password"},
		Users:  map[string]string{"guest": "guest-password"},
	})

	disabled := true
	if _, err := s.UpdateUser("guest", UserPatch{Disabled: &disabled}); !errors.Is(err, ErrEnvUserReadOnly) {
		t.Fatalf("修改环境账号应返回 ErrEnvUserReadOnly，得到 %v", err)
	}
	if err := s.DeleteUser("root"); !errors.Is(err, ErrEnvUserReadOnly) {
		t.Fatalf("删除环境账号应返回 ErrEnvUserReadOnly，得到 %v", err)
	}
}

func TestLastAdminIsProtected(t *testing.T) {
	s, _ := openTestStore(t, Settings{}, EnvAccounts{})
	if _, err := s.CreateUser("boss", "boss-password", RoleAdmin); err != nil {
		t.Fatal(err)
	}

	user := RoleUser
	if _, err := s.UpdateUser("boss", UserPatch{Role: &user}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("降级最后一个管理员应返回 ErrLastAdmin，得到 %v", err)
	}
	disabled := true
	if _, err := s.UpdateUser("boss", UserPatch{Disabled: &disabled}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("禁用最后一个管理员应返回 ErrLastAdmin，得到 %v", err)
	}
	if err := s.DeleteUser("boss"); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("删除最后一个管理员应返回 ErrLastAdmin，得到 %v", err)
	}

	// 有第二个管理员后即可操作。
	if _, err := s.CreateUser("deputy", "deputy-password", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser("boss"); err != nil {
		t.Fatalf("存在其他管理员时应允许删除: %v", err)
	}
}

func TestDeleteUnknownUser(t *testing.T) {
	s, _ := openTestStore(t, Settings{}, EnvAccounts{})
	if err := s.DeleteUser("ghost"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("得到 %v, 期望 ErrUserNotFound", err)
	}
}

func TestUsersListIsSortedAndMarksSource(t *testing.T) {
	s, _ := openTestStore(t, Settings{}, EnvAccounts{Admins: map[string]string{"root": "root-password"}})
	_, _ = s.CreateUser("zed", "zed-password", RoleUser)
	_, _ = s.CreateUser("amy", "amy-password", RoleUser)

	users := s.Users()
	var names []string
	for _, u := range users {
		names = append(names, u.Username)
	}
	want := []string{"amy", "root", "zed"}
	if len(names) != len(want) {
		t.Fatalf("得到 %v, 期望 %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("得到 %v, 期望 %v", names, want)
		}
	}
}

func TestRecordLoginUpdatesTimestamp(t *testing.T) {
	s, _ := openTestStore(t, Settings{}, EnvAccounts{Admins: map[string]string{"root": "root-password"}})
	_, _ = s.CreateUser("alice", "alice-password", RoleUser)

	now := time.Now().Truncate(time.Second)
	s.RecordLogin("alice", now)
	s.RecordLogin("root", now)

	u, ok := s.GetUser("alice")
	if !ok || !u.LastLoginAt.Equal(now) {
		t.Fatalf("db 用户最近登录时间应更新: %+v", u)
	}
	u, ok = s.GetUser("root")
	if !ok || !u.LastLoginAt.Equal(now) {
		t.Fatalf("env 用户最近登录时间应在内存中更新: %+v", u)
	}
}

// 删除后重建同名账号，旧令牌里的版本号不能恰好等于新账号的版本号，否则旧令牌会复活。
func TestRecreatedUserGetsFreshTokenVersion(t *testing.T) {
	s, _ := openTestStore(t, Settings{}, EnvAccounts{Admins: map[string]string{"root": "root-password"}})

	seen := map[int]bool{}
	for i := 0; i < 20; i++ {
		u, err := s.CreateUser("bob", "bob-password", RoleUser)
		if err != nil {
			t.Fatal(err)
		}
		if seen[u.TokenVersion] {
			t.Fatalf("第 %d 次重建得到了重复的令牌版本 %d", i, u.TokenVersion)
		}
		seen[u.TokenVersion] = true
		if err := s.DeleteUser("bob"); err != nil {
			t.Fatal(err)
		}
	}
}

// 环境账号无法在后台禁用，修改部署里的密码或角色并重启就是它唯一的吊销手段，
// 因此版本号必须随密码与角色变化。
func TestEnvUserTokenVersionFollowsCredentials(t *testing.T) {
	version := func(env EnvAccounts, name string) int {
		s, _ := openTestStore(t, Settings{}, env)
		u, ok := s.GetUser(name)
		if !ok {
			t.Fatalf("找不到 %s", name)
		}
		return u.TokenVersion
	}

	base := version(EnvAccounts{Admins: map[string]string{"root": "password-one"}}, "root")
	if base != version(EnvAccounts{Admins: map[string]string{"root": "password-one"}}, "root") {
		t.Fatal("相同的环境账号在重启后版本号应保持不变")
	}
	if base == version(EnvAccounts{Admins: map[string]string{"root": "password-two"}}, "root") {
		t.Fatal("修改环境账号密码后版本号应改变")
	}
	if base == version(EnvAccounts{Admins: map[string]string{"x": "y"}, Users: map[string]string{"root": "password-one"}}, "root") {
		t.Fatal("修改环境账号角色后版本号应改变")
	}
}

// bcrypt 只接受 72 字节以内的输入。升级前 AUTH_USERS 里的长口令必须继续可用，而不是让服务启动失败。
func TestLongEnvPasswordStillWorks(t *testing.T) {
	long := strings.Repeat("长口令", 30) // 远超 72 字节
	s, _ := openTestStore(t, Settings{}, EnvAccounts{Admins: map[string]string{"root": long}})

	if _, err := s.Authenticate("root", long); err != nil {
		t.Fatalf("长口令应能登录: %v", err)
	}
	if _, err := s.Authenticate("root", long[:len(long)-3]); err == nil {
		t.Fatal("长口令的前缀不应通过校验")
	}
}

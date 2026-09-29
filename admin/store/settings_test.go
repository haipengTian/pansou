package store

import (
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func init() {
	// 测试里用最低成本，避免每个用例都做完整的 bcrypt 运算。
	bcryptCost = bcrypt.MinCost
}

func openTestStore(t *testing.T, seed Settings, env EnvAccounts) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir, seed, env)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	return s, dir
}

func TestOpenSeedsSettingsFromEnvWhenFileMissing(t *testing.T) {
	seed := Settings{
		EnabledPlugins:  []string{"pansearch", "duoduo"},
		DefaultChannels: []string{"tgsearchers7"},
	}
	s, dir := openTestStore(t, seed, EnvAccounts{})

	got := s.Settings()
	if !reflect.DeepEqual(got.EnabledPlugins, seed.EnabledPlugins) {
		t.Fatalf("EnabledPlugins = %v, 期望 %v", got.EnabledPlugins, seed.EnabledPlugins)
	}
	if !reflect.DeepEqual(got.DefaultChannels, seed.DefaultChannels) {
		t.Fatalf("DefaultChannels = %v, 期望 %v", got.DefaultChannels, seed.DefaultChannels)
	}
	if !s.SeededFromEnv() {
		t.Fatal("文件不存在时应标记为从环境变量播种")
	}
	if _, err := os.Stat(filepath.Join(dir, settingsFileName)); err != nil {
		t.Fatalf("播种后应落盘 settings.json: %v", err)
	}
}

func TestOpenPrefersExistingSettingsFile(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(dir, Settings{DefaultChannels: []string{"a"}}, EnvAccounts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.UpdateSettings(Settings{DefaultChannels: []string{"b", "c"}}, "admin"); err != nil {
		t.Fatal(err)
	}

	// 再次打开时传入不同的 env 种子，文件内容应胜出。
	second, err := Open(dir, Settings{DefaultChannels: []string{"env-only"}}, EnvAccounts{})
	if err != nil {
		t.Fatal(err)
	}
	if got := second.Settings().DefaultChannels; !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Fatalf("DefaultChannels = %v, 期望文件中的 [b c]", got)
	}
	if second.SeededFromEnv() {
		t.Fatal("已有文件时不应标记为播种")
	}
}

func TestUpdateSettingsNormalizesAndStampsAuthor(t *testing.T) {
	s, _ := openTestStore(t, Settings{}, EnvAccounts{})

	got, err := s.UpdateSettings(Settings{
		EnabledPlugins:    []string{" pansearch ", "", "pansearch", "duoduo"},
		DefaultChannels:   []string{"a", " a", "b "},
		AllowedChannels:   []string{"a"},
		AllowedCloudTypes: []string{"quark", "QUARK", "baidu"},
	}, "root")
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"pansearch", "duoduo"}; !reflect.DeepEqual(got.EnabledPlugins, want) {
		t.Fatalf("EnabledPlugins = %v, 期望 %v", got.EnabledPlugins, want)
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(got.DefaultChannels, want) {
		t.Fatalf("DefaultChannels = %v, 期望 %v", got.DefaultChannels, want)
	}
	if want := []string{"quark", "baidu"}; !reflect.DeepEqual(got.AllowedCloudTypes, want) {
		t.Fatalf("AllowedCloudTypes = %v, 期望 %v", got.AllowedCloudTypes, want)
	}
	if got.UpdatedBy != "root" || got.UpdatedAt.IsZero() {
		t.Fatalf("应记录修改人与时间，得到 by=%q at=%v", got.UpdatedBy, got.UpdatedAt)
	}
}

func TestSettingsReturnsIndependentCopy(t *testing.T) {
	s, _ := openTestStore(t, Settings{DefaultChannels: []string{"a", "b"}}, EnvAccounts{})

	snapshot := s.Settings()
	snapshot.DefaultChannels[0] = "mutated"

	if got := s.Settings().DefaultChannels[0]; got != "a" {
		t.Fatalf("调用方修改快照不应影响存储，得到 %q", got)
	}
}

func TestEffectiveAllowedChannelsFallsBackToDefault(t *testing.T) {
	settings := Settings{DefaultChannels: []string{"a", "b"}}
	if got := settings.EffectiveAllowedChannels(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("未配置可选频道时应回落到默认频道，得到 %v", got)
	}

	settings.AllowedChannels = []string{"x"}
	if got := settings.EffectiveAllowedChannels(); !reflect.DeepEqual(got, []string{"x"}) {
		t.Fatalf("配置了可选频道时应直接使用，得到 %v", got)
	}
}

func TestConcurrentSettingsReadWrite(t *testing.T) {
	s, _ := openTestStore(t, Settings{DefaultChannels: []string{"a"}}, EnvAccounts{})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = s.UpdateSettings(Settings{DefaultChannels: []string{"a", "b"}}, "w")
		}()
		go func() {
			defer wg.Done()
			_ = s.Settings()
		}()
	}
	wg.Wait()
}

func TestCorruptSettingsFileIsReported(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, settingsFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, Settings{}, EnvAccounts{}); err == nil {
		t.Fatal("损坏的 settings.json 应返回错误，而不是静默用 env 覆盖")
	}
}

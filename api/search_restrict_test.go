package api

import (
	"reflect"
	"testing"

	"pansou/admin/store"
	"pansou/model"
)

func TestRestrictSearchRequest(t *testing.T) {
	settings := store.Settings{
		DefaultChannels:   []string{"chan_a", "chan_b"},
		AllowedChannels:   []string{"chan_a", "chan_b", "Chan_C"},
		AllowedCloudTypes: []string{"quark", "baidu"},
	}

	cases := []struct {
		name           string
		in             model.SearchRequest
		wantChannels   []string
		wantCloudTypes []string
	}{
		{
			name:           "未指定时保持为空，交给默认值逻辑",
			in:             model.SearchRequest{},
			wantChannels:   nil,
			wantCloudTypes: []string{"quark", "baidu"},
		},
		{
			name:           "范围外的频道与网盘类型被剔除",
			in:             model.SearchRequest{Channels: []string{"chan_a", "evil"}, CloudTypes: []string{"quark", "115"}},
			wantChannels:   []string{"chan_a"},
			wantCloudTypes: []string{"quark"},
		},
		{
			name:           "频道比较不区分大小写，保留后台配置的写法",
			in:             model.SearchRequest{Channels: []string{"chan_c"}},
			wantChannels:   []string{"Chan_C"},
			wantCloudTypes: []string{"quark", "baidu"},
		},
		{
			name:           "全部越界时回落到默认范围",
			in:             model.SearchRequest{Channels: []string{"evil"}, CloudTypes: []string{"115"}},
			wantChannels:   nil,
			wantCloudTypes: []string{"quark", "baidu"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := tc.in
			got := restrictSearchRequest(tc.in, settings)
			if !reflect.DeepEqual(got.Channels, tc.wantChannels) {
				t.Fatalf("Channels = %v, 期望 %v", got.Channels, tc.wantChannels)
			}
			if !reflect.DeepEqual(got.CloudTypes, tc.wantCloudTypes) {
				t.Fatalf("CloudTypes = %v, 期望 %v", got.CloudTypes, tc.wantCloudTypes)
			}
			if !reflect.DeepEqual(tc.in, original) {
				t.Fatal("不应修改传入的请求")
			}
		})
	}
}

func TestRestrictSearchRequestWithoutCloudTypeLimit(t *testing.T) {
	settings := store.Settings{DefaultChannels: []string{"chan_a"}}
	got := restrictSearchRequest(model.SearchRequest{CloudTypes: []string{"115"}}, settings)
	if !reflect.DeepEqual(got.CloudTypes, []string{"115"}) {
		t.Fatalf("未限制网盘类型时应原样保留，得到 %v", got.CloudTypes)
	}
}

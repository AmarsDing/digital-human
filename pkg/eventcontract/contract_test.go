package eventcontract

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
)

const contractPath = "../../web/src/services/api/events.contract.json"

type contract struct {
	Version  int `json:"version"`
	Envelope struct {
		Fields []string `json:"fields"`
	} `json:"envelope"`
	Events map[string][]string `json:"events"`
}

func loadContract(t *testing.T) contract {
	t.Helper()
	raw, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatalf("读取共享契约失败: %v", err)
	}
	var c contract
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("解析共享契约失败: %v", err)
	}
	return c
}

// 信封字段必须与 Go 侧常量一致（顺序也一致，便于文档对齐）。
func TestEnvelopeFieldsMatchContract(t *testing.T) {
	c := loadContract(t)
	if !reflect.DeepEqual(c.Envelope.Fields, EnvelopeFields) {
		t.Fatalf("envelope 不一致: json=%v go=%v", c.Envelope.Fields, EnvelopeFields)
	}
}

// 字幕事件：事件名与 payload 字段必须与契约一致。
func TestTranscriptEventsMatchContract(t *testing.T) {
	c := loadContract(t)
	cases := []struct {
		event  string
		fields []string
	}{
		{TranscriptPartial, []string{FieldText, FieldIndex, FieldMode}},
		{TranscriptFinal, []string{FieldText, FieldIndex, FieldUtteranceID}},
	}
	for _, tc := range cases {
		got, ok := c.Events[tc.event]
		if !ok {
			t.Fatalf("契约缺少事件 %q", tc.event)
		}
		if !reflect.DeepEqual(got, tc.fields) {
			t.Fatalf("%s payload 不一致: json=%v go=%v", tc.event, got, tc.fields)
		}
	}
}

// 契约里声明的每个事件都必须有非空 payload 声明列表（或显式空数组），不得拼写漂移。
func TestContractEventNamesNonEmpty(t *testing.T) {
	c := loadContract(t)
	names := make([]string, 0, len(c.Events))
	for name := range c.Events {
		if name == "" {
			t.Fatal("契约存在空事件名")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("契约未声明任何事件")
	}
}

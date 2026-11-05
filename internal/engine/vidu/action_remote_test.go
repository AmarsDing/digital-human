package vidu

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
)

func TestActionRemoteCRUDUsesViduContract(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token vda_test" {
			t.Fatalf("Authorization=%q", r.Header.Get("Authorization"))
		}
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/live/v1/action-libraries":
			_, _ = w.Write([]byte(`{"data":{"list":[{"id":"lib1","name":"默认","description":"开场"}]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/live/v1/action-libraries":
			_, _ = w.Write([]byte(`{"data":{"id":"lib1","name":"默认","description":"开场"}}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/live/v1/action-libraries/lib1":
			_, _ = w.Write([]byte(`{"data":{}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/live/v1/action-libraries/lib1/actions":
			_, _ = w.Write([]byte(`{"data":{"list":[{"id":"act1","library_id":"lib1","name":"挥手","instruction":"右手挥动"}]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/live/v1/action-libraries/lib1/actions":
			_, _ = w.Write([]byte(`{"data":{"id":"act1","library_id":"lib1","name":"挥手","instruction":"右手挥动","duration_ms":4500}}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/live/v1/actions/act1":
			_, _ = w.Write([]byte(`{"data":{}}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected %s %s", r.Method, r.URL.Path), http.StatusNotFound)
		}
	}))
	defer srv.Close()

	remote := NewActionRemote(newTestClient(t, srv.URL, nil))
	ctx := context.Background()
	libs, err := remote.Libraries(ctx)
	if err != nil || len(libs) != 1 || libs[0].ID != "lib1" {
		t.Fatalf("libraries=%+v err=%v", libs, err)
	}
	lib, err := remote.CreateLibrary(ctx, "默认", "开场")
	if err != nil || lib.ID != "lib1" {
		t.Fatalf("library=%+v err=%v", lib, err)
	}
	actions, err := remote.Actions(ctx, lib.ID)
	if err != nil || len(actions) != 1 || actions[0].Instruction != "右手挥动" {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
	act, err := remote.CreateAction(ctx, lib.ID, "挥手", "右手挥动", 4500)
	if err != nil || act.ID != "act1" || act.LibraryID != lib.ID || act.DurationMS != 4500 {
		t.Fatalf("action=%+v err=%v", act, err)
	}
	if err := remote.DeleteAction(ctx, act.ID); err != nil {
		t.Fatal(err)
	}
	if err := remote.DeleteLibrary(ctx, lib.ID); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"GET /live/v1/action-libraries?pager.page=1&pager.pagesz=100",
		"POST /live/v1/action-libraries",
		"GET /live/v1/action-libraries/lib1/actions?pager.page=1&pager.pagesz=100",
		"POST /live/v1/action-libraries/lib1/actions",
		"DELETE /live/v1/actions/act1",
		"DELETE /live/v1/action-libraries/lib1",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
}

func TestActionRemoteUnavailableDoesNotFakeSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"message":"Vidu 暂不可用"}`))
	}))
	defer srv.Close()
	remote := NewActionRemote(newTestClient(t, srv.URL, nil))
	if _, err := remote.Libraries(context.Background()); err == nil {
		t.Fatal("Vidu 不可达必须返回错误，不得返回空动作库冒充成功")
	}
}

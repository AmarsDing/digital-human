package vidu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/teamwork/digital-human/internal/biz/action"
)

// ActionRemote 把动作库端口接到 Vidu HTTP；任何上游错误都原样返回，不用内存数据兜底。
type ActionRemote struct{ client *Client }

func NewActionRemote(client *Client) *ActionRemote { return &ActionRemote{client: client} }

func (r *ActionRemote) Libraries(ctx context.Context) ([]action.Library, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	raw, err := r.request(ctx, http.MethodGet, "/live/v1/action-libraries?pager.page=1&pager.pagesz=100", nil, true)
	if err != nil {
		return nil, err
	}
	var out []action.Library
	if err := decodeActionList(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *ActionRemote) CreateLibrary(ctx context.Context, name, description string) (action.Library, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := r.request(ctx, http.MethodPost, "/live/v1/action-libraries", map[string]any{"name": name, "description": description}, false)
	if err != nil {
		return action.Library{}, err
	}
	var out action.Library
	if err := decodeActionEntity(raw, "library", &out); err != nil {
		return action.Library{}, err
	}
	return out, nil
}

func (r *ActionRemote) DeleteLibrary(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := r.request(ctx, http.MethodDelete, "/live/v1/action-libraries/"+url.PathEscape(id), nil, true)
	return err
}

func (r *ActionRemote) Actions(ctx context.Context, libraryID string) ([]action.Action, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	raw, err := r.request(ctx, http.MethodGet, "/live/v1/action-libraries/"+url.PathEscape(libraryID)+"/actions?pager.page=1&pager.pagesz=100", nil, true)
	if err != nil {
		return nil, err
	}
	var out []action.Action
	if err := decodeActionList(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *ActionRemote) CreateAction(ctx context.Context, libraryID, name, instruction string) (action.Action, error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	raw, err := r.request(ctx, http.MethodPost, "/live/v1/action-libraries/"+url.PathEscape(libraryID)+"/actions", map[string]any{"name": name, "instruction": instruction}, false)
	if err != nil {
		return action.Action{}, err
	}
	var out action.Action
	if err := decodeActionEntity(raw, "action", &out); err != nil {
		return action.Action{}, err
	}
	if out.LibraryID == "" {
		out.LibraryID = libraryID
	}
	return out, nil
}

func (r *ActionRemote) DeleteAction(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := r.request(ctx, http.MethodDelete, "/live/v1/actions/"+url.PathEscape(id), nil, true)
	return err
}

func (r *ActionRemote) request(ctx context.Context, method, path string, body any, retry bool) (map[string]any, error) {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	var out map[string]any
	if err := r.client.doJSON(ctx, method, r.client.url(path), r.client.authHeader(), data, &out, retry, false, nil); err != nil {
		return nil, err
	}
	return out, nil
}

func decodeActionList(raw map[string]any, out any) error {
	body := actionData(raw)
	for _, key := range []string{"list", "items", "libraries", "actions"} {
		if list, ok := body[key]; ok {
			return remarshalAction(list, out)
		}
	}
	return fmt.Errorf("Vidu 动作库响应缺少列表字段")
}

func decodeActionEntity(raw map[string]any, key string, out any) error {
	body := actionData(raw)
	if nested, ok := body[key]; ok {
		return remarshalAction(nested, out)
	}
	if _, ok := body["id"]; ok {
		return remarshalAction(body, out)
	}
	return fmt.Errorf("Vidu 动作库响应缺少 %s 对象", key)
}

func actionData(raw map[string]any) map[string]any {
	if data, ok := raw["data"].(map[string]any); ok {
		return data
	}
	return raw
}

func remarshalAction(in, out any) error {
	data, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

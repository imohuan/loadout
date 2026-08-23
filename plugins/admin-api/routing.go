package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"loadout/core/config"
	"loadout/core/db"
	"loadout/plugins/contracts"
	"loadout/plugins/types"
)

type channelInput struct {
	Name          string `json:"name"`
	ChannelName   string `json:"channel_name"`
	BaseURL       string `json:"base_url"`
	APIKey        string `json:"api_key"`
	Enabled       *bool  `json:"enabled"`
	ManualEnabled *bool  `json:"manual_enabled"`
	SyncBilling   bool   `json:"sync_billing"`
	// Models 为调用方提供的模型列表：非空时不请求上游探测，直接用该列表。
	// 空/缺省时才会自动请求上游 /v1/models 探测填充。
	Models []string `json:"models"`
}

func channelAPI(channel db.Channel) types.Channel {
	models := make([]string, 0, len(channel.Models))
	detail := make([]types.ChannelModelDetail, 0, len(channel.Models))
	for _, model := range channel.Models {
		detail = append(detail, types.ChannelModelDetail{Model: model.Model, Source: model.Source, Enabled: model.Enabled})
		if model.Enabled {
			models = append(models, model.Model)
		}
	}
	return types.Channel{ID: channel.ID, Name: channel.Name, ChannelName: channel.ChannelName, BaseURL: channel.BaseURL, APIKeyCipher: channel.APIKeyCipher, Enabled: channel.ManualEnabled, ManualEnabled: channel.ManualEnabled, SyncBilling: channel.SyncBilling, Models: models, ModelsDetail: detail, ModelsError: channel.ModelsError, CreatedAt: channel.CreatedAt, UpdatedAt: channel.UpdatedAt}
}

func (s *Service) listDBChannels(ctx context.Context) ([]db.Channel, error) {
	return s.routing.ListChannels(ctx)
}

// sameBaseChannels 返回与 baseURL 同组（忽略尾斜杠差异）的渠道列表。
func (s *Service) sameBaseChannels(ctx context.Context, baseURL string) ([]db.Channel, error) {
	channels, err := s.listDBChannels(ctx)
	if err != nil {
		return nil, err
	}
	target := strings.TrimRight(baseURL, "/")
	out := make([]db.Channel, 0, 1)
	for _, ch := range channels {
		if strings.TrimRight(ch.BaseURL, "/") == target {
			out = append(out, ch)
		}
	}
	return out, nil
}

// applyChannelNameSync 当渠道名称变化时，同步同 Base URL 全部渠道的名称（含自身）。
func applyChannelNameSync(channels []db.Channel, changedID, baseURL, newName string) {
	for i := range channels {
		if channels[i].ID != changedID && strings.TrimRight(channels[i].BaseURL, "/") == strings.TrimRight(baseURL, "/") {
			channels[i].ChannelName = newName
		}
	}
}

func (s *Service) handleChannelsListDB(w http.ResponseWriter, r *http.Request) {
	channels, err := s.listDBChannels(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	result := make([]types.Channel, 0, len(channels))
	for _, channel := range channels {
		result = append(result, channelAPI(channel))
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Service) handleChannelCreateDB(w http.ResponseWriter, r *http.Request) {
	var input channelInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Name == "" || input.BaseURL == "" {
		writeError(w, http.StatusBadRequest, "名称和地址必填")
		return
	}
	// 渠道名称：未显式提供时，若同 Base URL 已有渠道则继承其渠道名（添加 Key 场景），
	// 否则回退为 Key 名。
	channelName := input.ChannelName
	if channelName == "" {
		if existing, err := s.sameBaseChannels(r.Context(), input.BaseURL); err == nil && len(existing) > 0 {
			channelName = existing[0].ChannelName
		} else {
			channelName = input.Name
		}
	}
	cipher, err := s.st.Encrypt(input.APIKey)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	id, err := newID()
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	manual := true
	if input.ManualEnabled != nil {
		manual = *input.ManualEnabled
	} else if input.Enabled != nil {
		manual = *input.Enabled
	}
	// 模型列表策略：请求里带了模型列表 → 直接用（source=manual），不请求上游；
	// 没带 → 自动请求上游 /v1/models 探测填充（source=probe）。
	now := time.Now().UTC().Format(time.RFC3339Nano)
	channel := db.Channel{ID: id, Name: input.Name, ChannelName: channelName, BaseURL: input.BaseURL, APIKeyCipher: cipher, ManualEnabled: manual, SyncBilling: input.SyncBilling, CreatedAt: now, UpdatedAt: now}
	if len(input.Models) > 0 {
		for _, model := range input.Models {
			if strings.TrimSpace(model) == "" {
				continue
			}
			channel.Models = append(channel.Models, db.ChannelModel{Model: model, Source: "manual", Enabled: true, FirstSeenAt: now, LastSeenAt: now})
		}
	} else {
		models, modelsError := probeChannelModels(input.BaseURL, input.APIKey)
		channel.ModelsError = modelsError
		for _, model := range models {
			channel.Models = append(channel.Models, db.ChannelModel{Model: model, Source: "probe", Enabled: true, FirstSeenAt: now, LastSeenAt: now})
		}
	}
	channels, err := s.listDBChannels(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	channels = append(channels, channel)
	if err := s.routing.ReplaceChannels(r.Context(), channels); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, channelAPI(channel))
}

func (s *Service) channelByID(ctx context.Context, id string) (db.Channel, int, error) {
	channels, err := s.listDBChannels(ctx)
	if err != nil {
		return db.Channel{}, -1, err
	}
	for index, channel := range channels {
		if channel.ID == id {
			return channel, index, nil
		}
	}
	return db.Channel{}, -1, errNotFound("channel")
}

func (s *Service) handleChannelUpdateDB(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var input channelInput
	if !decodeJSON(w, r, &input) {
		return
	}
	channels, err := s.listDBChannels(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	index := -1
	for i := range channels {
		if channels[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		writeError(w, http.StatusNotFound, "渠道不存在")
		return
	}
	channel := &channels[index]
	oldBase := strings.TrimRight(channel.BaseURL, "/")
	if input.Name != "" {
		channel.Name = input.Name
	}
	if input.BaseURL != "" {
		channel.BaseURL = input.BaseURL
	}
	if input.ManualEnabled != nil {
		channel.ManualEnabled = *input.ManualEnabled
	} else if input.Enabled != nil {
		channel.ManualEnabled = *input.Enabled
	}
	channel.SyncBilling = input.SyncBilling
	newBase := strings.TrimRight(channel.BaseURL, "/")
	if oldBase != newBase {
		// 挪到别的组：渠道名跟随新组（忽略前端回传的旧组名），避免破坏"同组一致"。
		channel.ChannelName = ""
		for j := range channels {
			if channels[j].ID != channel.ID && strings.TrimRight(channels[j].BaseURL, "/") == newBase && channels[j].ChannelName != "" {
				channel.ChannelName = channels[j].ChannelName
				break
			}
		}
		if channel.ChannelName == "" {
			channel.ChannelName = channel.Name
		}
	} else if input.ChannelName != "" && input.ChannelName != channel.ChannelName {
		// 渠道名称变化：同步同 Base URL 的全部渠道（一个 Base URL 一组，名称应一致）。
		channel.ChannelName = input.ChannelName
		applyChannelNameSync(channels, channel.ID, channel.BaseURL, input.ChannelName)
	}
	if input.APIKey != "" {
		channel.APIKeyCipher, err = s.st.Encrypt(input.APIKey)
		if err != nil {
			s.writeServerError(w, err)
			return
		}
		// 换了新 key：清掉旧 key 的自动熔断（auth/billing 等禁用的渠道状态），
		// 否则新 key 仍会被路由跳过。model-health 未装配时忽略。
		if s.health != nil {
			_ = s.health.RecoverChannel(r.Context(), channel.ID)
		}
	}
	plain := input.APIKey
	if plain == "" && channel.APIKeyCipher != "" {
		plain, _ = s.st.Decrypt(channel.APIKeyCipher)
	}
	// 模型列表策略：请求里带了模型列表 → 不请求上游探测（列表由前端通过
	// /models 接口显式管理），只更新时间戳；没带 → 自动探测填充。
	// 与创建保持一致：只要存在模型列表就不探测。
	if len(input.Models) > 0 {
		channel.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		_ = plain
	} else {
		models, modelsError := probeChannelModels(channel.BaseURL, plain)
		channel.ModelsError = modelsError
		now := time.Now().UTC().Format(time.RFC3339Nano)
		channel.UpdatedAt = now
		values := make([]db.ChannelModel, 0, len(models))
		for _, model := range models {
			values = append(values, db.ChannelModel{Model: model, Source: "probe", Enabled: true, FirstSeenAt: now, LastSeenAt: now})
		}
		// 合并保留手动配置的模型（探测结果只替换 probe 来源，manual 不丢）。
		channel.Models = mergeManualModels(channel.Models, values, now)
	}
	if err := s.routing.ReplaceChannels(r.Context(), channels); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, channelAPI(*channel))
}

func (s *Service) handleChannelDeleteDB(w http.ResponseWriter, r *http.Request) {
	channels, err := s.listDBChannels(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	id := r.PathValue("id")
	found := false
	out := channels[:0]
	for _, channel := range channels {
		if channel.ID == id {
			found = true
		} else {
			out = append(out, channel)
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "渠道不存在")
		return
	}
	if err := s.routing.ReplaceChannels(r.Context(), out); err != nil {
		writeError(w, http.StatusConflict, "渠道仍被聚合目标引用，请先移除目标")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Service) handleChannelRefreshModelsDB(w http.ResponseWriter, r *http.Request) {
	channel, _, err := s.channelByID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "渠道不存在")
		return
	}
	key, _ := s.st.Decrypt(channel.APIKeyCipher)
	models, modelsError := probeChannelModels(channel.BaseURL, key)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	values := make([]db.ChannelModel, 0, len(models))
	for _, model := range models {
		values = append(values, db.ChannelModel{Model: model, Source: "probe", Enabled: true, FirstSeenAt: now, LastSeenAt: now})
	}
	// 合并保留手动配置的模型（探测结果只替换 probe 来源，manual 不丢）。
	values = mergeManualModels(channel.Models, values, now)
	if err := s.routing.ReplaceChannelModels(r.Context(), channel.ID, values); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models, "models_error": modelsError})
}

// channelModelInput 渠道模型编辑项。
type channelModelInput struct {
	Model   string `json:"model"`
	Enabled *bool  `json:"enabled"`
}

// handleChannelModelsReplaceDB 全量编辑渠道模型清单（添加/删除/禁用/启用一接口搞定）。
// 语义：渠道中「设置的模型」= 提交清单里 enabled=true 的模型；enabled=false 的
// 视为删除（不再写入 channel_models，同时清理其历史状态），保证模型状态
// 严格以模型渠道为数据源。现有模型的 source 保留（探测的仍为 probe），新增模型默认 source=manual。
func (s *Service) handleChannelModelsReplaceDB(w http.ResponseWriter, r *http.Request) {
	var input []channelModelInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if len(input) == 0 {
		writeError(w, http.StatusBadRequest, "模型清单不能为空")
		return
	}
	channel, _, err := s.channelByID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "渠道不存在")
		return
	}
	existing := make(map[string]string, len(channel.Models))
	for _, m := range channel.Models {
		existing[m.Model] = m.Source
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	values := make([]db.ChannelModel, 0, len(input))
	enabledModels := make([]string, 0, len(input))
	for _, in := range input {
		if strings.TrimSpace(in.Model) == "" {
			writeError(w, http.StatusBadRequest, "模型名不能为空")
			return
		}
		enabled := true
		if in.Enabled != nil {
			enabled = *in.Enabled
		}
		if !enabled {
			// 未勾选 = 从渠道删除（不写入目录），无需保留 enabled=0 的残行。
			continue
		}
		source := existing[in.Model]
		if source == "" {
			source = "manual"
		}
		values = append(values, db.ChannelModel{Model: in.Model, Source: source, Enabled: true, FirstSeenAt: now, LastSeenAt: now})
		enabledModels = append(enabledModels, in.Model)
	}
	if err := s.routing.ReplaceChannelModels(r.Context(), channel.ID, values); err != nil {
		s.writeServerError(w, err)
		return
	}
	// 目录被清空（用户删光所有模型）：自动重新探测填充，避免渠道晾在无模型状态。
	// 与「编辑已有目录不探测」互补：非空目录由用户显式管理，空目录自动兜底刷新。
	if len(values) == 0 {
		key := ""
		if channel.APIKeyCipher != "" {
			if k, err := s.st.Decrypt(channel.APIKeyCipher); err == nil {
				key = k
			}
		}
		probed, modelsError := probeChannelModels(channel.BaseURL, key)
		now = time.Now().UTC().Format(time.RFC3339Nano)
		values = make([]db.ChannelModel, 0, len(probed))
		for _, model := range probed {
			values = append(values, db.ChannelModel{Model: model, Source: "probe", Enabled: true, FirstSeenAt: now, LastSeenAt: now})
		}
		// 探测失败也落库 error，UI 显示「探测失败」而非「0 个」。
		if err := s.routing.ReplaceChannelModels(r.Context(), channel.ID, values); err != nil {
			s.writeServerError(w, err)
			return
		}
		if err := s.updateChannelModelsError(r.Context(), channel.ID, modelsError); err != nil {
			s.lg.Warn("更新渠道模型探测错误失败", "channel_id", channel.ID, "err", err)
		}
		enabledModels = enabledModels[:0]
		for _, model := range probed {
			enabledModels = append(enabledModels, model)
		}
	}
	// 清理被删除模型的历史状态（幽灵），模型状态与渠道目录立即一致；
	// 未装配 model-health 时跳过（不影响目录本身）。
	if s.health != nil && len(enabledModels) > 0 {
		if err := s.health.PurgeChannelStates(r.Context(), channel.ID, enabledModels); err != nil {
			s.lg.Warn("清理渠道幽灵模型状态失败", "channel_id", channel.ID, "err", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "models": values})
}

// updateChannelModelsError 更新渠道的 models_error 字段（探测失败状态展示用）。
func (s *Service) updateChannelModelsError(ctx context.Context, channelID, modelsError string) error {
	return s.routing.UpdateChannelModelsError(ctx, channelID, modelsError)
}

// mergeManualModels 合并渠道模型：保留 Source=manual 的现有模型，
// 探测结果只补充新探测到的模型（probe 来源）。用于刷新/更新渠道时避免手动配置丢失。
func mergeManualModels(existing []db.ChannelModel, probed []db.ChannelModel, now string) []db.ChannelModel {
	probeNames := make(map[string]bool, len(probed))
	for _, m := range probed {
		probeNames[m.Model] = true
	}
	out := make([]db.ChannelModel, 0, len(probed)+len(existing))
	out = append(out, probed...)
	for _, m := range existing {
		if m.Source == "manual" && !probeNames[m.Model] {
			out = append(out, m)
		}
	}
	return out
}

func (s *Service) handleChannelMoveDB(w http.ResponseWriter, r *http.Request) {
	channels, err := s.listDBChannels(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	index := -1
	for i := range channels {
		if channels[i].ID == r.PathValue("id") {
			index = i
			break
		}
	}
	var body struct {
		Direction string `json:"direction"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	target := index
	if body.Direction == "up" {
		target--
	} else if body.Direction == "down" {
		target++
	} else {
		writeError(w, http.StatusBadRequest, "方向必须是 up 或 down")
		return
	}
	if index < 0 || target < 0 || target >= len(channels) {
		writeError(w, http.StatusBadRequest, "无法移动：已在边界或渠道不存在")
		return
	}
	channels[index], channels[target] = channels[target], channels[index]
	if err := s.routing.ReplaceChannels(r.Context(), channels); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, channels)
}

// handleChannelsReorderDB 全量重排渠道顺序：按提交的 id 数组顺序设置 position。
// 前端按 base_url 分组后整组移动，提交全量顺序即可。未知/重复 id 忽略（不报错），
// 未提交的记录保持原相对顺序追加到尾部。
func (s *Service) handleChannelsReorderDB(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	channels, err := s.listDBChannels(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	byID := make(map[string]int, len(channels))
	for i := range channels {
		byID[channels[i].ID] = i
	}
	out := make([]db.Channel, 0, len(channels))
	seen := make(map[string]bool, len(body.IDs))
	for _, id := range body.IDs {
		idx, ok := byID[id]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, channels[idx])
	}
	for i := range channels {
		if !seen[channels[i].ID] {
			out = append(out, channels[i])
		}
	}
	if err := s.routing.ReplaceChannels(r.Context(), out); err != nil {
		s.writeServerError(w, err)
		return
	}
	result := make([]types.Channel, 0, len(out))
	for i := range out {
		result = append(result, channelAPI(out[i]))
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Service) handleChannelTestDB(w http.ResponseWriter, r *http.Request) {
	// Reuse the tested request implementation by exposing a temporary JSON
	// snapshot only in memory is unnecessary; this path mirrors its lookup and
	// delegates to the same upstream request body logic.
	var req struct {
		ID     string `json:"id"`
		Model  string `json:"model"`
		Vision bool   `json:"vision"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	channel, _, err := s.channelByID(r.Context(), req.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "渠道不存在")
		return
	}
	key, _ := s.st.Decrypt(channel.APIKeyCipher)
	if req.Model == "" {
		req.Model = "gpt-4o"
	}
	payload := map[string]any{"model": req.Model, "messages": []map[string]any{{"role": "user", "content": "ping"}}, "stream": false}
	body, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(channel.BaseURL, "/")+"/chat/completions", strings.NewReader(string(body)))
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	request.Header.Set("Content-Type", "application/json")
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	start := time.Now()
	response, err := (&http.Client{Timeout: config.VisionTimeout}).Do(request)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "latency_ms": latency})
		return
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 8192))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "status": response.StatusCode, "latency_ms": latency, "body": string(data)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": response.StatusCode, "latency_ms": latency, "reply": extractReply(data)})
}

func (s *Service) handleAggregatesListDB(w http.ResponseWriter, r *http.Request) {
	values, err := s.routing.ListAggregates(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, values)
}
func (s *Service) handleAggregateCreateDB(w http.ResponseWriter, r *http.Request) {
	var value db.Aggregate
	if !decodeJSON(w, r, &value) {
		return
	}
	// 新建聚合模型：前端未显式传 enabled（Go 零值 false）时，有目标即默认启用，
	// 避免落库即禁用、导致 /v1/models 不暴露该虚拟模型。
	if !value.Enabled && len(value.Targets) > 0 {
		value.Enabled = true
	}
	values, err := s.routing.ListAggregates(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	values = append(values, value)
	if err := s.routing.ReplaceAggregates(r.Context(), values); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}
func (s *Service) handleAggregatesReplaceDB(w http.ResponseWriter, r *http.Request) {
	var values []db.Aggregate
	if !decodeJSON(w, r, &values) {
		return
	}
	// 前端（尤其旧版本）可能漏发 enabled（Go 零值 false）：有目标即默认启用，
	// 避免整体替换后虚拟模型被意外禁用、从 /v1/models 消失。
	// 注：当前 UI 无「禁用聚合模型」开关，不存在显式 false 语义；将来若加禁用功能，
	// 建议改用 *bool 或独立 PATCH 端点，避免零值歧义。
	for i := range values {
		if !values[i].Enabled && len(values[i].Targets) > 0 {
			values[i].Enabled = true
		}
	}
	if err := s.routing.ReplaceAggregates(r.Context(), values); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, values)
}
func (s *Service) handleAggregateDeleteDB(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	values, err := s.routing.ListAggregates(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	out := values[:0]
	for _, value := range values {
		if value.Name != input.Name {
			out = append(out, value)
		}
	}
	if err := s.routing.ReplaceAggregates(r.Context(), out); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Service) handleModelStatusList(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	values, err := s.health.List(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, flattenStatus(values))
}
func (s *Service) handleModelStatusSet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ManualEnabled bool `json:"manual_enabled"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.health.SetModelEnabled(r.Context(), r.PathValue("channel_id"), r.PathValue("model"), body.ManualEnabled); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func (s *Service) handleModelStatusSetBatch(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	var body struct {
		Models        []string `json:"models"`
		ManualEnabled bool     `json:"manual_enabled"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.Models) == 0 {
		writeError(w, http.StatusBadRequest, "models 不能为空")
		return
	}
	if err := s.health.SetModelsEnabled(r.Context(), r.PathValue("channel_id"), body.Models, body.ManualEnabled); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func (s *Service) handleModelStatusDelete(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	if err := s.health.DeleteModel(r.Context(), r.PathValue("channel_id"), r.PathValue("model")); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "only manual models can be deleted") || strings.Contains(msg, "not in channel") {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func (s *Service) handleModelStatusDeleteBatch(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	var body struct {
		Models []string `json:"models"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.Models) == 0 {
		writeError(w, http.StatusBadRequest, "models 不能为空")
		return
	}
	if err := s.health.DeleteModels(r.Context(), r.PathValue("channel_id"), body.Models); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "only manual models can be deleted") || strings.Contains(msg, "not in channel") {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func (s *Service) handleChannelStatusSet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ManualEnabled bool `json:"manual_enabled"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.health.SetChannelEnabled(r.Context(), r.PathValue("channel_id"), body.ManualEnabled); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func (s *Service) handleModelStatusRecover(w http.ResponseWriter, r *http.Request) {
	if err := s.health.RecoverModel(r.Context(), r.PathValue("channel_id"), r.PathValue("model")); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func (s *Service) handleModelStatusRecoverBatch(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	var body struct {
		Models []string `json:"models"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.Models) == 0 {
		writeError(w, http.StatusBadRequest, "models 不能为空")
		return
	}
	if err := s.health.RecoverModels(r.Context(), r.PathValue("channel_id"), body.Models); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func (s *Service) handleChannelStatusRecover(w http.ResponseWriter, r *http.Request) {
	if err := s.health.RecoverChannel(r.Context(), r.PathValue("channel_id")); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func (s *Service) handleModelStatusCheck(w http.ResponseWriter, r *http.Request) {
	if err := s.health.CheckNow(r.Context(), false); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func (s *Service) handleModelStatusRecoverAll(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	affected, err := s.health.RecoverAllModels(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "affected": affected})
}
func (s *Service) handleModelStatusRecoverAllChannel(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	affected, err := s.health.RecoverAllModelsByChannel(r.Context(), r.PathValue("channel_id"))
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "affected": affected})
}
func (s *Service) handleModelStatusRecoverAllChannels(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "model-health 未装配")
		return
	}
	affected, err := s.health.RecoverAllChannels(r.Context())
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "affected": affected})
}

func flattenStatus(values []contracts.ChannelStatus) []map[string]any {
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		models := make([]map[string]any, 0, len(value.Models))
		for _, model := range value.Models {
			models = append(models, map[string]any{
				"model": model.Model, "manual_enabled": model.ManualEnabled,
				"health_status": model.Health.HealthStatus, "effective_available": model.Health.EffectiveAvailable,
				"reason": model.Health.Reason, "last_error": model.LastError, "fail_count": model.FailCount,
				"last_success_at": model.LastSuccessAt, "disabled_until": model.DisabledUntil, "source": model.Source,
			})
		}
		result = append(result, map[string]any{"channel": map[string]any{"id": value.ID, "name": value.Name, "channel_name": value.ChannelName, "base_url": value.BaseURL, "manual_enabled": value.ManualEnabled, "sync_billing": value.SyncBilling}, "manual_enabled": value.ManualEnabled, "health_status": value.Health.HealthStatus, "effective_available": value.Health.EffectiveAvailable, "reason": value.Health.Reason, "models": models})
	}
	return result
}

func (s *Service) handleRouteLogsList(w http.ResponseWriter, r *http.Request) {
	if s.routeLog == nil {
		writeError(w, http.StatusServiceUnavailable, "route-log 未装配")
		return
	}
	filter := contracts.RouteLogFilter{Model: r.URL.Query().Get("model"), ChannelID: r.URL.Query().Get("channel_id"), ChannelName: r.URL.Query().Get("channel_name"), Result: r.URL.Query().Get("result")}
	if value := r.URL.Query().Get("from"); value != "" {
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			filter.StartedAfter = &parsed
		}
	}
	if value := r.URL.Query().Get("to"); value != "" {
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			filter.StartedBefore = &parsed
		}
	}
	// 分页：page 从 1 起，pageSize 默认 20、上限 100（与 DataPagination 的 pageSizes 对齐）。
	// pageSize 超上限钳到 100 而非静默回退默认值，避免请求 50 却拿到 20 的错觉。
	page := 1
	if v := r.URL.Query().Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			page = n
		}
	}
	pageSize := 20
	if v := r.URL.Query().Get("pageSize"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 100 {
				n = 100
			}
			pageSize = n
		}
	}
	filter.Limit = pageSize
	filter.Offset = (page - 1) * pageSize
	values, err := s.routeLog.List(r.Context(), filter)
	if err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, values)
}
func (s *Service) handleRouteLogDetail(w http.ResponseWriter, r *http.Request) {
	if s.routeLog == nil {
		writeError(w, http.StatusServiceUnavailable, "route-log 未装配")
		return
	}
	requestID := r.PathValue("request_id")
	// 自我修复：仅当前端带 repair=1 主动触发时，先让后端兜底卡死的 running 记录。
	// 判定 = 活跃登记表（IsActive false → 转发已结束/进程已崩/超时兜底）+ 时间兜底
	// （started_at 超阈值仍 running）。仅当 result='running' 且 finished_at 为空才动。
	if r.URL.Query().Get("repair") == "1" {
		if threshold := config.RouteLogSelfHealTimeout; threshold > 0 {
			if err := s.routeLog.SelfHeal(r.Context(), requestID, threshold); err != nil {
				s.lg.Warn("route-log self-heal failed", "request_id", requestID, "err", err)
			}
		}
	}
	value, err := s.routeLog.Detail(r.Context(), requestID)
	if err != nil {
		writeError(w, http.StatusNotFound, "转发日志不存在")
		return
	}
	writeJSON(w, http.StatusOK, value)
}
func (s *Service) handleRouteLogsClear(w http.ResponseWriter, r *http.Request) {
	if s.routeLog == nil {
		writeError(w, http.StatusServiceUnavailable, "route-log 未装配")
		return
	}
	// 清空全部转发日志（route-log 的 Clear 当前为全量清空，参数无实际作用）
	if err := s.routeLog.Clear(r.Context(), time.Time{}); err != nil {
		s.writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func errNotFound(name string) error { return errors.New(name + " not found") }

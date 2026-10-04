package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"pi-gateway/internal/store"
)

func seedModelCatalog(t *testing.T, h *testHarness, accountID int64, ids ...string) {
	t.Helper()
	models := make([]store.CatalogModel, 0, len(ids))
	for _, id := range ids {
		models = append(models, store.CatalogModel{ID: id, Name: "Display " + id})
	}
	if err := h.store.SaveAccountModelCatalog(context.Background(), accountID, &store.ModelCatalog{
		Models: models, FetchedAt: store.NowMS(), AttemptedAt: store.NowMS(),
	}); err != nil {
		t.Fatal(err)
	}
}

func readModelResponse(t *testing.T, h *testHarness, suffix, key string) (int, http.Header, map[string]json.RawMessage) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.server.URL+"/v1/models"+suffix, nil)
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, result
}

func readModelEntries(t *testing.T, h *testHarness, suffix string) []map[string]any {
	t.Helper()
	code, headers, result := readModelResponse(t, h, suffix, h.key)
	query, err := url.ParseQuery(strings.TrimPrefix(suffix, "?"))
	if err != nil {
		t.Fatal(err)
	}
	dataRaw := result["data"]
	if query.Get("client_version") != "" {
		dataRaw = result["models"]
		if code != http.StatusOK || len(result) != 1 {
			t.Fatalf("invalid Codex models envelope: status=%d body=%s", code, result)
		}
	} else if code != http.StatusOK || len(result) != 2 || string(result["object"]) != `"list"` {
		t.Fatalf("invalid OpenAI list envelope: status=%d body=%s", code, result)
	}
	if headers.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("unsafe catalog cache policy: %q", headers.Get("Cache-Control"))
	}
	var data []map[string]any
	if err := json.Unmarshal(dataRaw, &data); err != nil {
		t.Fatal(err)
	}
	if data == nil {
		t.Fatal("empty models must be an array, not null")
	}
	status := "cached"
	if len(data) == 0 {
		status = "empty"
	}
	if got := headers.Get("X-Model-Catalog-Status"); got != status {
		t.Fatalf("catalog status=%q want=%q", got, status)
	}
	return data
}

func modelEntryFields(t *testing.T, model store.CatalogModel, id string, codex bool) map[string]any {
	t.Helper()
	fields := map[string]any{"object": "model", "owned_by": "openai", "name": model.Name, "source": model.Source}
	if len(model.Origins) > 0 {
		origins := make([]any, len(model.Origins))
		for i, origin := range model.Origins {
			origins[i] = origin
		}
		fields["origins"] = origins
	}
	if model.Description != "" {
		fields["description"] = model.Description
	}
	if codex {
		fields["display_name"] = model.Name
	}
	for key, value := range model.Metadata {
		var decoded any
		if err := json.Unmarshal(value, &decoded); err != nil {
			t.Fatal(err)
		}
		fields[key] = decoded
	}
	fields["id"] = id
	if _, exists := fields["slug"]; exists || codex {
		fields["slug"] = id
	}
	if fields["model"] == model.ID {
		fields["model"] = id
	}
	return fields
}

func readModelIDs(t *testing.T, h *testHarness, key string) (int, []string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.server.URL+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, model := range result.Data {
		ids = append(ids, model.ID)
	}
	return resp.StatusCode, ids
}

func expectModelIDs(t *testing.T, h *testHarness, key string, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	sort.Strings(want)
	for _, suffix := range []string{"", "?client_version=0.120.0"} {
		status, headers, payload := readModelResponse(t, h, suffix, key)
		data := payload["data"]
		if suffix != "" {
			data = payload["models"]
		}
		var entries []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(data, &entries); err != nil {
			t.Fatalf("models%s: status=%d body=%v err=%v", suffix, status, payload, err)
		}
		got := make([]string, 0, len(entries))
		for _, entry := range entries {
			got = append(got, entry.ID)
		}
		if status != http.StatusOK || !reflect.DeepEqual(got, want) || headers.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("models%s: status=%d got=%v want=%v", suffix, status, got, want)
		}
	}
}

func TestModelsRequireAuthAndNeverInventStaticModels(t *testing.T) {
	h := newHarness(t, http.NotFoundHandler(), "sse")
	if status, _ := readModelIDs(t, h, ""); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated models status=%d", status)
	}
	expectModelIDs(t, h, h.key)
	seedModelCatalog(t, h, h.accountID, "model-allowed", "model-blocked", "native-remapped")
	if err := h.store.PatchAccountManagementFields(context.Background(), h.accountID, store.AccountManagementPatch{DisabledModels: []string{"model-blocked"}}); err != nil {
		t.Fatal(err)
	}
	runtime := h.dataPlane.settings.Get()
	runtime.ModelMappings = map[string]string{
		"friendly-alias": "model-allowed", "blocked-alias": "model-blocked",
		"missing-alias": "not-observed", "native-remapped": "model-blocked",
	}
	if err := h.dataPlane.settings.Set(context.Background(), runtime); err != nil {
		t.Fatal(err)
	}
	expectModelIDs(t, h, h.key, "friendly-alias", "model-allowed")
	// A failed refresh retains the last observed list; no fake fallback is added.
	if err := h.store.SaveAccountModelCatalog(context.Background(), h.accountID, &store.ModelCatalog{AttemptedAt: store.NowMS() + 1, Error: "upstream unavailable"}); err != nil {
		t.Fatal(err)
	}
	expectModelIDs(t, h, h.key, "friendly-alias", "model-allowed")
}

func TestModelsHonorGroupScopePublicAccountsAndInheritedRestrictions(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, http.NotFoundHandler(), "sse")
	seedModelCatalog(t, h, h.accountID, "public-allowed", "public-denied")
	makeAccount := func(name string) *store.Account {
		a := &store.Account{Name: name, Enabled: true, Status: store.AccountStatusReady}
		if err := h.store.CreateAccount(ctx, a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	a, b := makeAccount("Team Alpha"), makeAccount("Team Beta")
	seedModelCatalog(t, h, a.ID, "alpha-model", "alpha-denied")
	seedModelCatalog(t, h, b.ID, "beta-model")
	ga := &store.AccountGroup{Name: "Alpha", Enabled: true, AccountIDs: []int64{a.ID}, DisabledModels: []string{"alpha-denied", "public-denied"}}
	gb := &store.AccountGroup{Name: "Beta", Enabled: true, AccountIDs: []int64{b.ID}}
	for _, group := range []*store.AccountGroup{ga, gb} {
		if err := h.store.CreateAccountGroup(ctx, group); err != nil {
			t.Fatal(err)
		}
	}
	key := &store.APIKey{Name: "Alpha key", Key: "sk-model-alpha", Enabled: true, GroupIDs: []int64{ga.ID}}
	if err := h.store.CreateKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	expectModelIDs(t, h, key.Key, "alpha-model", "public-allowed")
	// An unscoped key can reach all accounts, but Alpha's member still inherits
	// Alpha's restrictions. The public account has no such membership restriction.
	expectModelIDs(t, h, h.key, "alpha-model", "beta-model", "public-allowed", "public-denied")
	ga.Enabled = false
	if err := h.store.UpdateAccountGroup(ctx, ga); err != nil {
		t.Fatal(err)
	}
	// A disabled group does not turn its members into public accounts or expand
	// the scoped key to Beta; only genuinely ungrouped accounts remain reachable.
	expectModelIDs(t, h, key.Key, "public-allowed", "public-denied")
}

func TestModelsCatalogReadFailureDoesNotFallBackToStaticList(t *testing.T) {
	h := newHarness(t, http.NotFoundHandler(), "sse")
	seedModelCatalog(t, h, h.accountID, "observed-model")
	if _, err := h.store.DB().Exec(`UPDATE account_model_catalog SET models_json='not-json' WHERE account_id=?`, h.accountID); err != nil {
		t.Fatal(err)
	}
	if status, ids := readModelIDs(t, h, h.key); status != http.StatusInternalServerError || len(ids) != 0 {
		t.Fatalf("failed catalog leaked fallback: status=%d models=%v", status, ids)
	}
}

func TestMappedModelRestrictionPreventsUpstreamAndClearsImmediately(t *testing.T) {
	var calls atomic.Int32
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(deltaSSE + terminalSSE))
	}), "sse")
	ctx := context.Background()
	runtime := h.dataPlane.settings.Get()
	runtime.ModelMappings = map[string]string{"alias": "blocked-model"}
	if err := h.dataPlane.settings.Set(ctx, runtime); err != nil {
		t.Fatal(err)
	}
	if err := h.store.PatchAccountManagementFields(ctx, h.accountID, store.AccountManagementPatch{DisabledModels: []string{"blocked-model"}}); err != nil {
		t.Fatal(err)
	}
	send := func() int {
		req, err := http.NewRequest(http.MethodPost, h.server.URL+"/v1/responses", strings.NewReader(`{"model":"alias","input":"Hi","stream":false}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+h.key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode
	}
	if status := send(); status != http.StatusForbidden || calls.Load() != 0 {
		t.Fatalf("alias bypassed restriction: status=%d upstream calls=%d", status, calls.Load())
	}
	if err := h.store.PatchAccountManagementFields(ctx, h.accountID, store.AccountManagementPatch{DisabledModels: []string{}}); err != nil {
		t.Fatal(err)
	}
	if status := send(); status != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("clear did not apply immediately: status=%d upstream calls=%d", status, calls.Load())
	}
}

func TestModelsManualOnlyCatalogFiltersMappingsAndUpdatesImmediately(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}), "sse")
	if err := h.store.PatchAccountManagementFields(ctx, h.accountID, store.AccountManagementPatch{
		SupplementalModels: []string{"manual-allowed", "manual-blocked", "manual-remapped"},
		DisabledModels:     []string{"manual-blocked"},
	}); err != nil {
		t.Fatal(err)
	}
	catalog, err := h.store.GetAccountModelCatalog(ctx, h.accountID)
	if err != nil || catalog == nil || catalog.FetchedAt != 0 || len(catalog.Models) != 3 {
		t.Fatalf("manual-only catalog=%+v err=%v", catalog, err)
	}
	runtime := h.dataPlane.settings.Get()
	runtime.ModelMappings = map[string]string{
		"manual-alias": "manual-allowed", "blocked-alias": "manual-blocked",
		"missing-alias": "not-configured", "manual-remapped": "manual-blocked",
	}
	if err := h.dataPlane.settings.Set(ctx, runtime); err != nil {
		t.Fatal(err)
	}
	expectModelIDs(t, h, h.key, "manual-alias", "manual-allowed")
	if err := h.store.SaveAccountModelCatalog(ctx, h.accountID, &store.ModelCatalog{AttemptedAt: store.NowMS(), Error: "initial upstream failure"}); err != nil {
		t.Fatal(err)
	}
	expectModelIDs(t, h, h.key, "manual-alias", "manual-allowed")
	// A genuine successful but empty upstream list does not erase manual IDs.
	now := store.NowMS() + 1
	if err := h.store.SaveAccountModelCatalog(ctx, h.accountID, &store.ModelCatalog{FetchedAt: now, AttemptedAt: now}); err != nil {
		t.Fatal(err)
	}
	expectModelIDs(t, h, h.key, "manual-alias", "manual-allowed")
	if err := h.store.PatchAccountManagementFields(ctx, h.accountID, store.AccountManagementPatch{DisabledModels: []string{}}); err != nil {
		t.Fatal(err)
	}
	expectModelIDs(t, h, h.key, "blocked-alias", "manual-alias", "manual-allowed", "manual-blocked", "manual-remapped")
	if err := h.store.PatchAccountManagementFields(ctx, h.accountID, store.AccountManagementPatch{SupplementalModels: []string{"manual-allowed"}}); err != nil {
		t.Fatal(err)
	}
	expectModelIDs(t, h, h.key, "manual-alias", "manual-allowed")
	if err := h.store.PatchAccountManagementFields(ctx, h.accountID, store.AccountManagementPatch{SupplementalModels: []string{}}); err != nil {
		t.Fatal(err)
	}
	expectModelIDs(t, h, h.key)
	if calls.Load() != 0 {
		t.Fatalf("manual model list unexpectedly probed upstream: %d", calls.Load())
	}
}

func TestModelsManualHonorAccountHealthAndEnabledState(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, http.NotFoundHandler(), "sse")
	if err := h.store.PatchAccountManagementFields(ctx, h.accountID, store.AccountManagementPatch{SupplementalModels: []string{"manual-model"}}); err != nil {
		t.Fatal(err)
	}
	expectModelIDs(t, h, h.key, "manual-model")
	for _, status := range []string{store.AccountStatusExpired, store.AccountStatusInvalid, store.AccountStatusBanned, "quota_exhausted"} {
		t.Run(status, func(t *testing.T) {
			if err := h.store.SetAccountStatus(ctx, h.accountID, status, ""); err != nil {
				t.Fatal(err)
			}
			expectModelIDs(t, h, h.key)
		})
	}
	if err := h.store.SetAccountStatus(ctx, h.accountID, store.AccountStatusReady, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.DB().Exec("UPDATE accounts SET cooldown_until=? WHERE id=?", store.NowMS()+60000, h.accountID); err != nil {
		t.Fatal(err)
	}
	expectModelIDs(t, h, h.key)
	if _, err := h.store.DB().Exec("UPDATE accounts SET cooldown_until=0 WHERE id=?", h.accountID); err != nil {
		t.Fatal(err)
	}
	no := false
	if err := h.store.PatchAccountManagementFields(ctx, h.accountID, store.AccountManagementPatch{Enabled: &no}); err != nil {
		t.Fatal(err)
	}
	expectModelIDs(t, h, h.key)
	yes := true
	if err := h.store.PatchAccountManagementFields(ctx, h.accountID, store.AccountManagementPatch{Enabled: &yes}); err != nil {
		t.Fatal(err)
	}
	expectModelIDs(t, h, h.key, "manual-model")
}

func TestModelsReturnPublicCatalogFieldsAndAliases(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, http.NotFoundHandler(), "sse")
	if got := readModelEntries(t, h, ""); len(got) != 0 {
		t.Fatalf("empty catalog=%v", got)
	}
	target := store.CatalogModel{
		ID: "target", Name: "Target display name", Description: "Target description", Source: "upstream", Origins: []string{store.ModelSourceChatGPT},
		Metadata: map[string]json.RawMessage{
			"id":                                   json.RawMessage(`"target"`),
			"model":                                json.RawMessage(`"target"`),
			"name":                                 json.RawMessage(`"Original upstream name"`),
			"display_name":                         json.RawMessage(`"Original upstream display"`),
			"description":                          json.RawMessage(`null`),
			"owned_by":                             json.RawMessage(`"upstream-owner"`),
			"metadata":                             json.RawMessage(`{"native_extension":true}`),
			"slug":                                 json.RawMessage(`"target"`),
			"supported_reasoning_levels":           json.RawMessage(`[{"effort":"low","description":"Fast"},{"effort":"max","description":"Deep"}]`),
			"default_reasoning_level":              json.RawMessage(`"max"`),
			"supports_reasoning_summary_parameter": json.RawMessage(`false`),
			"context_window":                       json.RawMessage(`400000`),
			"input_modalities":                     json.RawMessage(`["text","image"]`),
			"experimental":                         json.RawMessage(`{"enabled":false,"options":[],"limit":null}`),
		},
	}
	now := store.NowMS()
	if err := h.store.SaveAccountModelCatalog(ctx, h.accountID, &store.ModelCatalog{
		Models:    []store.CatalogModel{target, {ID: "native", Name: "Do not use native metadata"}},
		FetchedAt: now, AttemptedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	// Refresh diagnostics and the account's credentials are never public fields.
	if err := h.store.SaveAccountModelCatalog(ctx, h.accountID, &store.ModelCatalog{
		AttemptedAt: now + 1, Error: "private account refresh-token diagnostic",
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.PatchAccountManagementFields(ctx, h.accountID, store.AccountManagementPatch{SupplementalModels: []string{"manual"}}); err != nil {
		t.Fatal(err)
	}
	runtime := h.dataPlane.settings.Get()
	runtime.ModelMappings = map[string]string{
		"alias": "target", "native": "target", "manual-alias": "manual", "missing": "not-observed",
	}
	if err := h.dataPlane.settings.Set(ctx, runtime); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "?client_version=0.120.0"} {
		want := []map[string]any{}
		for _, id := range []string{"alias", "manual", "manual-alias", "native", "target"} {
			model := target
			if strings.HasPrefix(id, "manual") {
				model = store.NewManualCatalogModel("manual")
			}
			want = append(want, modelEntryFields(t, model, id, suffix != ""))
		}
		if got := readModelEntries(t, h, suffix); !reflect.DeepEqual(got, want) {
			t.Fatalf("public fields or alias capabilities differ: got=%#v want=%#v", got, want)
		}
	}
	// Serving aliases must not modify their stored targets, including raw metadata.
	catalog, err := h.store.GetAccountModelCatalog(ctx, h.accountID)
	if err != nil {
		t.Fatal(err)
	}
	var storedTarget *store.CatalogModel
	for i := range catalog.Models {
		if catalog.Models[i].ID == target.ID {
			storedTarget = &catalog.Models[i]
			break
		}
	}
	if storedTarget == nil || !reflect.DeepEqual(*storedTarget, target) {
		t.Fatalf("alias changed stored target: %+v", catalog.Models)
	}
}

func TestModelsClientVersionValidationAndAuth(t *testing.T) {
	h := newHarness(t, http.NotFoundHandler(), "sse")
	for _, version := range []string{"bad_version", "bad/version", strings.Repeat("a", 65), "版本"} {
		status, headers, _ := readModelResponse(t, h, "?client_version="+url.QueryEscape(version), h.key)
		if status != http.StatusBadRequest || headers.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("client_version %q: status=%d cache=%q", version, status, headers.Get("Cache-Control"))
		}
	}
	for _, suffix := range []string{"?client_version=%ZZ", "?client_version=1.0&client_version=2.0", "?client_version=1.0;bad"} {
		if status, _, _ := readModelResponse(t, h, suffix, h.key); status != http.StatusBadRequest {
			t.Fatalf("malformed query %q returned %d", suffix, status)
		}
	}
	for _, version := range []string{"", "0.120.0+dev", "V1-alpha", strings.Repeat("a", 64)} {
		if got := readModelEntries(t, h, "?client_version="+url.QueryEscape(version)); len(got) != 0 {
			t.Fatalf("empty catalog version=%q models=%v", version, got)
		}
	}
	status, _, body := readModelResponse(t, h, "?client_version=stable-1.2%2Bcodex", "")
	if status != http.StatusUnauthorized || body == nil {
		t.Fatalf("unauthenticated client version status=%d body=%v", status, body)
	}
	status, _, body = readModelResponse(t, h, "?client_version=", h.key)
	if status != http.StatusOK || len(body["data"]) == 0 || len(body["models"]) != 0 {
		t.Fatalf("empty client_version did not preserve list format: status=%d body=%v", status, body)
	}
}

func TestModelsOmitPrivateMetadataWithoutMutatingCatalog(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, http.NotFoundHandler(), "sse")
	model := store.CatalogModel{ID: "safe", Name: "Safe model", Metadata: map[string]json.RawMessage{
		"context_window": json.RawMessage(`9007199254740993`),
		"access_token":   json.RawMessage(`"private-access-token"`),
		"account_id":     json.RawMessage(`"private-account-id"`),
		"headers":        json.RawMessage(`{"Authorization":"Bearer private-header"}`),
		"nested":         json.RawMessage(`{"items":[{"refreshToken":"private-refresh","supported":false,"empty":[],"nullable":null}]}`),
	}}
	if err := h.store.SaveAccountModelCatalog(ctx, h.accountID, &store.ModelCatalog{Models: []store.CatalogModel{model}, FetchedAt: store.NowMS(), AttemptedAt: store.NowMS()}); err != nil {
		t.Fatal(err)
	}
	got := readModelEntries(t, h, "")
	if len(got) != 1 {
		t.Fatalf("models=%v", got)
	}
	for _, key := range []string{"access_token", "account_id", "headers", "metadata"} {
		if _, exists := got[0][key]; exists {
			t.Fatalf("private or invented metadata field %q leaked: %#v", key, got[0])
		}
	}
	nested := map[string]any{"items": []any{map[string]any{"supported": false, "empty": []any{}, "nullable": nil}}}
	if !reflect.DeepEqual(got[0]["nested"], nested) {
		t.Fatalf("nested metadata filtering changed public fields: %#v", got[0]["nested"])
	}
	// The raw JSON sanitizer must not round large numbers through float64.
	public := publicModelMetadata(model.Metadata)
	if string(public["context_window"]) != "9007199254740993" {
		t.Fatalf("large integer changed: %s", public["context_window"])
	}
	for _, suffix := range []string{"", "?client_version=0.120.0"} {
		_, _, payload := readModelResponse(t, h, suffix, h.key)
		data := payload["data"]
		if suffix != "" {
			data = payload["models"]
		}
		var rawEntries []map[string]json.RawMessage
		if err := json.Unmarshal(data, &rawEntries); err != nil {
			t.Fatal(err)
		}
		if string(rawEntries[0]["context_window"]) != "9007199254740993" {
			t.Fatalf("large integer was not preserved in API JSON: %s", rawEntries[0]["context_window"])
		}
		for _, field := range []string{"access_token", "account_id", "headers"} {
			if _, exists := rawEntries[0][field]; exists {
				t.Fatalf("private field in models%s: %s", suffix, data)
			}
		}
	}
	catalog, err := h.store.GetAccountModelCatalog(ctx, h.accountID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(catalog.Models[0].Metadata, model.Metadata) {
		t.Fatalf("API mutated the raw cached metadata: %#v", catalog.Models[0].Metadata)
	}
}

func TestModelsDuplicateIDsChooseDeterministicEligibleMetadata(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, http.NotFoundHandler(), "sse")
	second := &store.Account{Name: "second private account", AccountID: "private-account-id", Enabled: true, Status: store.AccountStatusReady, Weight: 100}
	if err := h.store.CreateAccount(ctx, second); err != nil {
		t.Fatal(err)
	}
	firstModel := store.CatalogModel{ID: "shared", Name: "First account model", Description: "First description", Source: "upstream", Origins: []string{store.ModelSourceChatGPT}, Metadata: map[string]json.RawMessage{
		"context_window": json.RawMessage(`1000`), "nullable": json.RawMessage(`null`), "supported": json.RawMessage(`false`),
		"empty": json.RawMessage(`[]`), "levels": json.RawMessage(`["low"]`), "nested": json.RawMessage(`{"first":true}`),
	}}
	secondModel := store.CatalogModel{ID: "shared", Name: "Second account model", Description: "Second description", Source: "upstream", Origins: []string{store.ModelSourceChatGPT}, Metadata: map[string]json.RawMessage{
		"context_window": json.RawMessage(`2000`), "nullable": json.RawMessage(`"replacement"`), "supported": json.RawMessage(`true`),
		"empty": json.RawMessage(`["new"]`), "levels": json.RawMessage(`["high"]`), "nested": json.RawMessage(`{"second":true}`),
		"new_capability": json.RawMessage(`{"enabled":true}`),
	}}
	mergedFirst := firstModel
	mergedFirst.Metadata = make(map[string]json.RawMessage)
	for key, value := range firstModel.Metadata {
		mergedFirst.Metadata[key] = value
	}
	mergedFirst.Metadata["new_capability"] = secondModel.Metadata["new_capability"]
	for _, entry := range []struct {
		id    int64
		model store.CatalogModel
	}{{second.ID, secondModel}, {h.accountID, firstModel}} {
		if err := h.store.SaveAccountModelCatalog(ctx, entry.id, &store.ModelCatalog{Models: []store.CatalogModel{entry.model}, FetchedAt: store.NowMS(), AttemptedAt: store.NowMS()}); err != nil {
			t.Fatal(err)
		}
	}
	runtime := h.dataPlane.settings.Get()
	runtime.ModelMappings = map[string]string{"alias": "shared"}
	if err := h.dataPlane.settings.Set(ctx, runtime); err != nil {
		t.Fatal(err)
	}
	check := func(model store.CatalogModel) {
		t.Helper()
		for _, suffix := range []string{"", "?client_version=0.120.0"} {
			want := []map[string]any{modelEntryFields(t, model, "alias", suffix != ""), modelEntryFields(t, model, model.ID, suffix != "")}
			for i := 0; i < 4; i++ {
				if got := readModelEntries(t, h, suffix); !reflect.DeepEqual(got, want) {
					t.Fatalf("non-deterministic or ineligible metadata: got=%#v want=%#v", got, want)
				}
			}
		}
	}
	check(mergedFirst)
	if err := h.store.PatchAccountManagementFields(ctx, h.accountID, store.AccountManagementPatch{DisabledModels: []string{"shared"}}); err != nil {
		t.Fatal(err)
	}
	check(secondModel)
	if err := h.store.PatchAccountManagementFields(ctx, h.accountID, store.AccountManagementPatch{DisabledModels: []string{}}); err != nil {
		t.Fatal(err)
	}
	check(mergedFirst)
	if err := h.store.SetAccountStatus(ctx, h.accountID, store.AccountStatusBanned, "private status diagnostic"); err != nil {
		t.Fatal(err)
	}
	check(secondModel)
	if err := h.store.SetAccountStatus(ctx, h.accountID, store.AccountStatusReady, ""); err != nil {
		t.Fatal(err)
	}
	check(mergedFirst)
}

func TestModelsPreferUpstreamWithoutFillingManualDefaults(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, http.NotFoundHandler(), "sse")
	if err := h.store.PatchAccountManagementFields(ctx, h.accountID, store.AccountManagementPatch{SupplementalModels: []string{"shared"}}); err != nil {
		t.Fatal(err)
	}
	upstreamAccount := &store.Account{Name: "upstream account", Enabled: true, Status: store.AccountStatusReady}
	laterManual := &store.Account{Name: "later manual account", Enabled: true, Status: store.AccountStatusReady, SupplementalModels: []string{"shared"}}
	for _, account := range []*store.Account{upstreamAccount, laterManual} {
		if err := h.store.CreateAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
	}
	// Legacy upstream snapshots can legitimately know no capabilities. Neither
	// earlier nor later manual accounts may inject guessed reasoning defaults.
	upstreamModel := store.CatalogModel{ID: "shared", Name: "Observed name", Description: "Observed description", Source: "upstream", Origins: []string{store.ModelSourceChatGPT}}
	if err := h.store.SaveAccountModelCatalog(ctx, upstreamAccount.ID, &store.ModelCatalog{Models: []store.CatalogModel{upstreamModel}, FetchedAt: store.NowMS(), AttemptedAt: store.NowMS()}); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "?client_version=0.120.0"} {
		want := []map[string]any{modelEntryFields(t, upstreamModel, "shared", suffix != "")}
		if got := readModelEntries(t, h, suffix); !reflect.DeepEqual(got, want) {
			t.Fatalf("upstream entry was replaced or enriched with manual defaults: got=%#v want=%#v", got, want)
		}
	}
	if err := h.store.SetAccountStatus(ctx, upstreamAccount.ID, store.AccountStatusBanned, ""); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "?client_version=0.120.0"} {
		want := []map[string]any{modelEntryFields(t, store.NewManualCatalogModel("shared"), "shared", suffix != "")}
		if got := readModelEntries(t, h, suffix); !reflect.DeepEqual(got, want) {
			t.Fatalf("ineligible upstream hid manual capabilities: got=%#v want=%#v", got, want)
		}
	}
}

func TestModelsManualHonorGroupScopeAndInheritedRestrictions(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, http.NotFoundHandler(), "sse")
	if err := h.store.PatchAccountManagementFields(ctx, h.accountID, store.AccountManagementPatch{SupplementalModels: []string{"public-manual", "public-denied"}}); err != nil {
		t.Fatal(err)
	}
	alpha := &store.Account{Name: "manual alpha", Enabled: true, Status: store.AccountStatusReady, SupplementalModels: []string{"alpha-manual", "alpha-denied"}}
	beta := &store.Account{Name: "manual beta", Enabled: true, Status: store.AccountStatusReady, SupplementalModels: []string{"beta-secret"}}
	for _, account := range []*store.Account{alpha, beta} {
		if err := h.store.CreateAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
	}
	ga := &store.AccountGroup{Name: "manual Alpha", Enabled: true, AccountIDs: []int64{alpha.ID}, DisabledModels: []string{"alpha-denied", "public-denied"}}
	gb := &store.AccountGroup{Name: "manual Beta", Enabled: true, AccountIDs: []int64{beta.ID}}
	for _, group := range []*store.AccountGroup{ga, gb} {
		if err := h.store.CreateAccountGroup(ctx, group); err != nil {
			t.Fatal(err)
		}
	}
	key := &store.APIKey{Name: "manual Alpha key", Key: "sk-manual-alpha", Enabled: true, GroupIDs: []int64{ga.ID}}
	if err := h.store.CreateKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	runtime := h.dataPlane.settings.Get()
	runtime.ModelMappings = map[string]string{"alpha-alias": "alpha-manual", "secret-alias": "beta-secret"}
	if err := h.dataPlane.settings.Set(ctx, runtime); err != nil {
		t.Fatal(err)
	}
	expectModelIDs(t, h, key.Key, "alpha-alias", "alpha-manual", "public-manual")
	expectModelIDs(t, h, h.key, "alpha-alias", "alpha-manual", "beta-secret", "secret-alias", "public-manual", "public-denied")
	ga.Enabled = false
	if err := h.store.UpdateAccountGroup(ctx, ga); err != nil {
		t.Fatal(err)
	}
	expectModelIDs(t, h, key.Key, "public-manual", "public-denied")
}

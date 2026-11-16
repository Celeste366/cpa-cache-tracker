package main_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	main "cpa-cache-tracker"
	"cpa-cache-tracker/abi"
	"cpa-cache-tracker/storage"
)

func TestNonGenerateUsageExcludedFromMainKPIs(t *testing.T) {
	d := main.NewDispatcher()
	defer d.HandleCall(abi.MethodPluginShutdown, nil)
	cfg, _ := json.Marshal(abi.LifecycleRequest{ConfigYAML: []byte(fmt.Sprintf("data_dir: %q\n", t.TempDir()))})
	if _, err := d.HandleCall(abi.MethodPluginRegister, cfg); err != nil {
		t.Fatal(err)
	}
	for i, generate := range []bool{true, false} {
		u, _ := json.Marshal(abi.UsageRecord{RequestID: fmt.Sprint(i), Model: "test-model", Generate: generate, Detail: abi.UsageDetail{InputTokens: 100, CacheReadTokens: 50}})
		if _, err := d.HandleCall(abi.MethodUsageHandle, u); err != nil {
			t.Fatal(err)
		}
	}
	resp := callManagement(t, d, http.MethodGet, "/v0/management/plugins/cpa-cache-tracker/summary?range=today", nil)
	var summary storage.SummaryResponse
	if err := json.Unmarshal(resp.Body, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Totals.Requests != 1 || summary.Totals.EffectiveInput != 100 {
		t.Fatalf("non-generation traffic polluted main KPIs: %+v", summary.Totals)
	}
	resp = callManagement(t, d, http.MethodGet, "/v0/management/plugins/cpa-cache-tracker/records?kind=non_generate", nil)
	var records storage.RecordsResponse
	if err := json.Unmarshal(resp.Body, &records); err != nil {
		t.Fatal(err)
	}
	if records.Total != 1 || records.Items[0].RecordKind != "non_generate" {
		t.Fatalf("non-generation usage must remain inspectable: %+v", records)
	}
}

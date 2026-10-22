# 合併後截圖問題審查

## 來源與限制

- 本輪程式基準：PR #3 合併 commit `3eb7abb5b273c00079f0ba6268d70925c9705c89`，tree `d2861a09cdae7389e0fbc019d37960d49e9794de`；481 個來源檔案逐一核對 Git blob SHA。
- 使用者提供六張 `master` Issues 截圖。頁尾是 **24 of 24 shown**，只代表當時篩選列表，不是未篩選專案總數，也不能由此推算評級。
- 圖中沒有 issue ID、規則 ID、行號、分析 SHA 或分析時間；三個 analysis warnings 未展開。30 個可讀卡片出現次數整理成 12 組，可靠的 distinct 下限為 22；SQL 與 await 的跨圖重疊不可精確分配。故 [findings.csv](findings.csv) 以來源群組逐組追蹤，不虛構 24 個唯一識別碼。
- GitHub 對上述 merge commit 的 [Sonar 檢查](https://github.com/MindPerformer/Pi-Gateway/runs/119971627825) 顯示 **New Code Reliability C**，但沒有 annotations；不能僅憑截圖將每一項歸入 new code。
- 沒有修改 Sonar disposition、掃描範圍、規則或 Quality Gate；沒有使用 NOSONAR。下列分類是本機處理結論，不是伺服器結案狀態。

## 三項程式／資料建模調整

### G01：巢狀規則的 `then` 是 wire 資料

完整分支資料改為 `web/fixtures/nested-workflow-branch.json`，測試從 JSON 載入。保留 backend／已儲存規則所需的 `then` 欄位，不改名、不動態拼字。原有所有 graph round-trip、節點身分、巢狀分支增刪與複製斷言保留。

新增 `then` 為陣列、不可呼叫、`Promise.resolve(params)` 保留同一物件、canonical JSON fixture 序列化 byte 一致的斷言。這是測試資料建模及分析消歧，**不是宣稱原程式具有 thenable 漏洞**；正式掃描是否關閉該項仍待驗證。

### G03：舊 API key migration 的固定欄位複製

把既有編譯期固定欄位拼接改為完整 `INSERT INTO api_keys_new (...) SELECT ... FROM api_keys` 字面量。新 `TestLegacyAPIKeyCopyColumns` 驗證與 key scanner 的完整 canonical 欄位清單同序，防止日後欄位更新時漏資料或錯置。

執行 SQL 與原版本逐 byte 相同，不改 schema、資料內容、遷移時機、交易、備份、索引或高水位序號。原 migration race 測試包含冪等、WAL 備份、錯誤回滾、並行及序號保留。

### G08：測試 waiter 自有取消生命週期

原 adapter 保存 parent context，但四個實際使用位置均只需獨立取消的 Background 語義。本輪改為測試專用、自己擁有 done/waiting 與冪等 cancel 的 probe，不保留或用方法閉包隱藏 parent。保持首次 Done 的 readiness 訊號，Err/Deadline/Value 不提前觸發它。

四處呼叫立即註冊 cleanup，保留 coalescing、取消 waiter 不取消 leader、取消 leader 可重試及真 HTTP body cancellation 的原斷言；自測涵蓋穩定 Done、取消前後 Err、無值／無 deadline、重複取消與標準 context.WithCancel 傳播。原實際 HTTP deadline 測試保留。沒有增加 production observer hook 或改 production Context API。

## 其餘項目與功能取捨

- **G02 SQL wrappers**：六個來源位置可由目前程式定位，但不能把它們直接當作截圖中六個唯一 issue。完整 caller／值綁定證據見 [前輪安全審查](../overall/security-review.md) 與 [呼叫點索引](../overall/sql-call-audit.csv)。`Rebind` 只轉 placeholder，不是 sanitizer。沒有確認請求值進入 SQL 語法；不能僅為讓告警消失而把危險邊界藏到另一層。
- **G04 可捲動差異區**：保留具名 `<section tabindex="0">`，讓沒有內部控制的長差異內容能以鍵盤捲動。MDN 明確建議為跨瀏覽器鍵盤捲動提供 tabindex 與可理解的名稱；具名 section 自帶 region 語意。刪 tabindex 或偽造 button/listbox role 會損害功能，不採用。[MDN overflow accessibility](https://developer.mozilla.org/en-US/docs/Web/CSS/Reference/Properties/overflow#accessibility)、[具名 region 與捲動區](https://developer.mozilla.org/en-US/docs/Web/Accessibility/ARIA/Reference/Roles/region_role#scrolling_content_areas_with_overflow_text)。
- **G05–G07 HTTP／WS**：保留正式 HTTP 服務、健康檢查與操作者明確配置的 HTTP 上游；HTTP→WS 不會自動加密。啟動訊息不是傳輸 sink，但真實遠端明文 upstream 有憑證／內容曝露風險。詳見前輪逐條網路資料流與部署 TLS 建議；不宣稱全部誤報。
- **G09 reduced-motion**：四項 !important 是保護使用者減少動態偏好的全域覆寫。移除可能讓高優先度元件樣式恢復動畫，不為分數破壞此契約。
- **G10 await 順序**：事件 callbacks、同一 enum 控制、同一 action 的選項按鈕、同一 reference 的 source／encoding 切換依賴前一步更新；不是獨立工作。前輪已將獨立 mounts 並行化；不能將剩餘共享狀態操作全改 Promise.all。
- **G11 JSON oracle**：測試驗證實際 JSON wire 語義，非通用記憶體深複製。structuredClone 保留 undefined／NaN 等非 JSON 行為，且 Vue proxy 相容性不同；重用 production clone 也會失去獨立 oracle。
- **G12 clipboard**：保留 HTTP 環境的 legacy copy fallback。WebCrypto 的 getRandomValues 可用不代表 Clipboard API 可在非 secure context 使用；移除 fallback 是另一次產品相容性改變，未擅自執行。

## 影響與驗證

本輪三項改動不改產品 UI、鍵盤操作、HTTP/WS、剪貼簿或 DB schema 契約，不增加依賴，不聲稱已使 overall A。另新增測試驗證 async event listeners 必須依序完成，以及 JSON normalize 對 undefined／NaN／Infinity／Vue reactive 資料的 wire 語義；這兩項為審查證據強化，不列為告警已修復。新增 JSON fixture 必須隨來源一起提交。另新增 reduced-motion 瀏覽器測試驗證 inline animation/transition 的覆寫、animationend 與一般模式還原；既有 diff 捲動 E2E 補 ArrowDown 與 Tab 離開，沒有把這些保留項列為已清除。

- `npm run check:rules` 通過：51 個 graph round-trips、490 個雙語 labels、28 個實際元件 code/visual round-trips 及既有 API／simulation 契約。
- `typecheck`、`check:i18n`、`check:capture`、`check:sonar-regressions`、`check:ui-accessibility`、production build 通過。
- `go test -race -p=1 -count=1 ./internal/store` 通過，36.997 秒。未配置真 PostgreSQL／Redis，因此不能聲稱本輪服務矩陣已重跑。
- 本輪 59 個瀏覽器 E2E 已通過 discovery，新增／修改 E2E 通過 strict TypeScript；執行仍待 CI，本機 Chromium 啟動限制未改變。前輪 PR #3 的 58/58 不冒充本輪執行結果。
- 相關 accounts waiter／Codex version race 測試連跑 3 次通過，16.068 秒；完整 accounts race 通過，74.692 秒。
- `go vet -buildvcs=false -p=1 ./...` 與 `go build -buildvcs=false -p=1 ./...` 通過。首次未指定該參數因隔離來源匯出沒有 Git 工作樹、無法取得 VCS status 而失敗；此參數只取消版本資訊嵌入，不停用 vet、測試或安全檢查。

使用者已另行批准本輪盡可能修正後推送原分支並建立新 PR；不自動合併、不部署，也不改 Sonar disposition。PR #3 已合併，本輪是獨立變更。

## PR #4 第一輪掃描與修正

`6359e0db15b76f53c69b04836aff6a41f6f21df0` 的 push frontend 已實際通過 59/59 Chromium 測試，包含 reduced-motion 與鍵盤捲動補測。Sonar Quality Gate 通過，但新增一項 maintainability：`web/check-rule-graph.mjs` 的 JSON.parse(JSON.stringify(...)) fixture 契約被解讀為 memory clone（issue `AaFktPF2pc1P3dGzNRRi`）。

後續改為真正 wire golden-byte 比對：從 fixture JSON.parse 載入後，JSON.stringify 的 canonical pretty bytes 必須精確還原原 fixture。保留非 callable、Promise identity 及所有原 graph round-trip 斷言；不把 JSON wire 語義換成 structuredClone、不隱藏分析字串。規則全套重新驗證；新 head 的完整 CI／Sonar 仍需重新執行，第一輪 59/59 不冒充新 head 結果。該 Sonar 摘要 coverage 為 0.0%，不宣稱已有 coverage 達標證據。

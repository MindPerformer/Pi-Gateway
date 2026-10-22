# 整體安全發現：程式修正與待審證據

本文件不變更 SonarCloud 的 issue disposition，也不保證評級已變更。原報告為 2026-10-22 main 分支、沒有 commit SHA；對應清單見 [findings.csv](findings.csv)。正常 HTTP 產品相容性仍保留。

## SQL：3 項結構修正、6 項待人工審查

### 已改為靜態 DDL

- `OVERALL-0003`：`db.go` 的 `api_keys` 定義抽出為編譯期常數；不再透過執行期字串切割／替換重建舊表。
- `OVERALL-0010`：37 個已知 additive migration 各自具有完整 SQLite / PostgreSQL 字面量 SQL。`registeredAdditiveMigration` 僅接受已登錄 table/column 配對；未知名稱拒絕。沒有把任意 SQL 輸入改名或搬到另一層。
- `OVERALL-0011`：舊 API key 表重建直接使用 `legacyAPIKeyTableSQL` 常數。
- `TestAdditiveMigrationStatements` 逐條比對現行 canonical schema，`TestAdditiveMigrationRejectsUnregisteredNames` 驗證未知／注入樣式名稱拒絕。SQLite、真實 PostgreSQL 的完整 migration 與 store 測試涵蓋其執行。

### 六個方言 wrapper 為何仍需要審查

`OVERALL-0004` 至 `OVERALL-0009` 是 `Store` / `storeTx` 的 `ExecContext`、`QueryContext`、`QueryRowContext`。它們接收程式建立的 query，再把 placeholder 轉換為方言所需格式；**資料值仍獨立經 `args...` 傳給 database/sql driver**。`Rebind` 不是安全 sanitizer，不能令任意不可信 SQL 變安全。未改成字串內插、逃脫函式、反射、動態屬性或忽略規則。

審閱目前 production store 原始碼的 175 個 Exec/Query/QueryRow 呼叫點：137 個 query 運算式是 Go 編譯期常數，38 個不是常數（包括上述六個轉送邊界）。逐點索引見 [sql-call-audit.csv](sql-call-audit.csv)，行號為本輪原始碼；另外唯一跨 store package 的 production 呼叫是 `internal/accounts/scheduling.go` 的固定 UPDATE，帳號與時間值仍綁定。

非常數來源逐組追蹤如下；「固定」指來自程式碼封閉集合，不是使用者輸入被當作 SQL 語法：

| 呼叫群組 | SQL 結構來源 | 外部資料處理／邊界 |
|---|---|---|
| `accounts.UpdateAccountManagementFields`、`accountManagementUpdate`、`appendAccountCapacityUpdate` | 固定 `column=?` 片段的條件選擇與 Join | 所有 patch 欄位值在 args；不接受客製 column 名稱 |
| `accounts.deleteAccount`、`groups.deleteAccountGroup`、`keys.deleteKey` | 程式內固定 DELETE 清單 | ID 綁定 |
| `captures.ListCaptures`、`CaptureFilter.sqlWhere` | 固定 SELECT / WHERE / ORDER；欄位為常數或固定選項 | AccountID、APIKeyID、Outcome、時間、limit/offset 綁定 |
| `db.ensureColumns`、`applyPostgresAdditiveColumns` | 已登錄的完整字面量 DDL | table/column 未登錄即拒絕；沒有請求輸入 SQL |
| `dialect.migratePostgres` | canonical `schemaSQL` 轉換、固定 `postgresProxyBindingSchema` 清單 | 無請求資料；保留 migration 資料相容性測試 |
| `dialect.insertID` | 六個 caller 都是固定 INSERT；PostgreSQL 只附固定 `RETURNING id` | captures、usage、proxies、groups、accounts、keys 的值透過 args |
| `groups.DisabledModelsForAccounts`、`modelcatalog.GetAccountModelCatalogs` | `accountModelBatchArgs` 只依 ID 數量重複 `?` placeholder；每批最多 500 | ID 本身均綁定，不能變成 SQL 文字 |
| `groups.queryIDs` | 兩個 caller 的固定 SELECT | ID 綁定 |
| `groups.patchAccountGroup` | `accountGroupUpdate` 的封閉 SET 片段 | patch 值綁定 |
| `keys.loadKey` | 兩個 caller 的固定 SELECT / `keyColumns` 常數 | key / ID 綁定 |
| `modelcatalog.lockModelCatalogAccount`、`readLockedModelCatalog` | 固定 SELECT；僅 PostgreSQL 分支附固定 `FOR UPDATE` | ID 綁定 |
| `proxies.ListProxies`、`ListProxyAccounts` | 固定 WHERE / LIKE / LIMIT 結構 | `%` 包住的 search 仍是綁定值，不是 SQL 字串；limit/offset 綁定 |
| `rules.listRules`、`Store.ListRules` | 固定 phase/enabled/search 條件與排序 | 搜尋 wildcard 轉義後仍綁定；phase/enabled 綁定 |
| usage 的查詢、summary、cost、trend、dimensions | `usageWhere` 固定條件片段；`usageDimensions` 只允許 model/key/account 三種固定 expression | 模型、結果、搜尋、時間與 ID 均綁定；未知 dimension 拒絕 |
| `usage.UsageTrend` | bucket 整數僅來自 hour=3600000 / day=86400000 封閉分支 | 未知 size 拒絕，沒有把任意輸入帶入格式字串 |
| `usage.percentileSQL` | 六處呼叫使用固定 ttft/tps 表名與 0.5/0.9/0.95 常數 | 無請求輸入 table / percentile expression |
| 六個 Store / transaction query wrappers | 接收上面 caller 的 query；`Rebind` 僅處理 placeholder | 原 args 不內插、不轉換成 SQL 文字 |

`exerciseStoreSQLBoundaries` 在 SQLite 與真實 PostgreSQL 經六個 wrapper 往返 SQL-looking 值，包含引號、分號、DROP 樣式、`?` 與 `$1`；資料仍是值且資料表持續存在。Rebind 的測試也涵蓋字串、識別字、註解與 PostgreSQL dollar quote，不將其內的 `?` 誤改。

**結論與限制：** 目前呼叫圖支持六項作「安全人工審查候選」，不是聲稱 generic SQL API 天生無注入風險。新增 caller 若將請求輸入拼成語法，這個結論即不再成立。S2077 仍可能在 generic wrapper 回報；需要 Sonar 正式重掃與有權限者逐項審查，不能以本文件自行結案。

## HTTP / WS：1 項收窄契約，3 項保留且明示風險

| 項目 | 實際資料流 | 本輪處理與剩餘風險 |
|---|---|---|
| `OVERALL-0050` `main.printStartup` | 輸出 HTTP 啟動 URL；該字串本身不是發送請求的 sink | 保留真實產品地址，不能只把 log 改 HTTPS 假裝加密。實際 gateway 若對非可信網路開放 HTTP，仍有憑證／內容明文風險，應由部署者加 TLS 或限可信回環／私網。是否接受此產品契約需明確人工決策。 |
| `OVERALL-0051` `runtime.healthcheckURL` | GET `/healthz`，不帶認證資料、不使用環境 proxy、拒絕 redirect、2 秒 timeout；wildcard bind 轉 loopback | 具體非 wildcard 設定地址仍可能不是 loopback，不能宣稱所有 probe 必定隔離。維持現有本機健康檢查相容性；HTTP 傳輸仍未加密。 |
| `OVERALL-0052` `config.toWebSocketScheme` | HTTPS→WSS、HTTP→WS，跟隨操作者明確配置的上游 endpoint | 這是真正的產品取捨：遠端 WS 可攜帶憑證／內容，明文上游有實際風險。沒有將 HTTP 偽裝成 WSS，沒有悄悄禁止原本可用的 HTTP 上游。強制遠端 TLS 是相容性政策改變，需另外批准。 |
| `OVERALL-0053` OAuth redirect | ChatGPT 已註冊 `http://127.0.0.1:1455/auth/callback`，不是任意遠端 HTTP builder | 移除未使用的任意 host/port builder，改固定常數；listener host/port 不影響已註冊 redirect。測試維持回環／固定註冊契約；HTTP loopback 是 RFC 8252 §7.3 允許的 native-app redirect，是否仍被 scanner 命中需重掃。 |

OAuth loopback 協定依據：[RFC 8252 §7.3](https://www.rfc-editor.org/rfc/rfc8252#section-7.3)。回環例外不適用於任意遠端 gateway 或 upstream。以上三項保留不代表 SonarCloud 已 Accepted，也不是使用者已批准伺服器端 disposition。

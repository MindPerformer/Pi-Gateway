# SonarCloud 整體評級修復紀錄（第三輪）

> **以下是發布前的本機驗證紀錄；瀏覽器 E2E 未在本機執行，正式 CI／Sonar 結果以本次 PR 為準。** 不以本機推斷宣稱 SonarCloud 已達 A。前兩輪 PR／new-code 指標不等同 main 的整體評級。

## 基準與完整清單

- 原始 PDF：2026-10-22、main、25 頁，沒有 commit SHA。整體 Reliability **C**（4 impacts）、Security **C**（13）、Maintainability **A**（51），Quality Gate FAILED；new-code 是 C/A/A（1/0/5）。
- 本輪程式基準：PR #2 合併後的 master `73e53e97b72a972c4cd1e4174d007cd776d92a71`。
- 原報告 68 個 impact 列表去除跨 quality 重複後，共 **55 項**。不把不同品質重複計作不同漏洞，也不把前一份 PR 報告的 8 項當成整體 inventory。
- [findings.csv](findings.csv) 逐項保留 rule、原始檔／行號、報告頁碼、品質、修改／保留理由、證據與限制。行號屬報告基準，不代表修改後的目前行號。
- 分類 `code_fixed_pending_scan` 表示有實際程式修復，等待 Sonar 驗證；`safe_review_candidate` 僅代表有具體證據可人工審查；`accepted_product_risk` 表示目前保留的產品相容性風險，**不是**已在 Sonar Accepted；`genuinely_unresolved` 不假裝完成。

目前 55 項逐項分類：**32 項程式修復待掃描、19 項人工審查候選、4 項保留產品風險**。沒有未交代的項目；「沒有 genuinely_unresolved 列」不代表 23 項保留項目已在 Sonar 結案。所有 issue 的 Sonar server disposition 均未更動。

## 實際變更與影響

### 交易 ownership：避免失敗／提前返回遺留交易

移除回傳 live transaction 的 `Store.BeginTx` factory，改由 `withStoreTransaction` 取得交易後立即 `defer Rollback`，在 callback 內完成所有使用。34 處 production consumer 都保留原有 explicit Commit、early return、查詢／寫入次序；沒有自動提交成功 callback，也沒有取消必要的鎖。

- callback error、panic、沒有呼叫 Commit 卻回傳成功：都 rollback。
- `ListUsageRecords` 保留原本 Commit 失敗仍回傳已讀 page 的語意，其他讀取失敗仍回傳 zero page。
- PostgreSQL readonly transaction 的預設 isolation 仍提升為 repeatable-read；複製 options，不修改 caller 傳入物件。
- rule publication 只在取得 publication lock 失敗時重試；callback 內的 busy／業務錯誤不能重播，以免重複寫入。
- 保留 UpdateProxy、SetProxyAccounts、SaveSettings 原先 BeginTx 錯誤前綴；callback／Commit error 不被錯誤標為 BeginTx failure。
- 大型 callback body 抽成 transaction 方法，維持原控制流程；不以增加 anonymous nesting 引入新的 cognitive-complexity 問題。
- 限制：泛型 callback 在型別上仍可能逃出 transaction；本次審閱所有現行 consumer 確認沒有這種用法，不能宣稱型別系統禁止逃出。

### SQL 與 OAuth 安全邊界

- 37 個 additive migration 登錄完整雙方言字面量 SQL；未登錄 table/column 拒絕。舊 API key 表重建使用編譯期 schema 常數。
- OAuth redirect 移除未使用的任意 host/port builder，固定為既有註冊 loopback URL；不改實際 callback 註冊值，也不強迫正式部署使用 HTTPS。
- 六個 generic SQL wrapper 保留 query 與 args 分離，沒有轉移／掩飾 scanner sink；已追蹤 175 個 store 呼叫點及跨 package caller。詳細證據及三項 HTTP/WS 的實際曝露在 [security-review.md](security-review.md)、[sql-call-audit.csv](sql-call-audit.csv)。

### 測試 context 與前端測試隔離

- CodexVersion 測試 body 移除 context field，以 transport context 通道與首次 Read 訊號配對；保留 timeout、cancellation、重試斷言。另一個需要保留父 Deadline/Err/Value 的測試 context adapter 未偽裝消除。
- 23 個可真正獨立執行的前端 await：先隔離 locale、module、Vue tree、Pinia 狀態再平行等待。共享同一控制項狀態轉移的 5 個 await 仍順序執行，不能只是換 `Promise.all` 製造競態。
- JSON 序列化 oracle 是 wire 格式測試，不替換為不同契約的 structuredClone。
- `then` 是後端規則 wire format 的陣列，不是 callable thenable。本次命中的 fixture 與直接 graph 轉換鏈同步使用它；Promise assimilation 只查最外層、且只呼叫函式值，不會遞迴吞掉 nested array。完整呼叫鏈在 CSV，不改 wire 欄位名稱。

### 真實 UI 語意改造

- 新增節點搜尋是命令而非選值：改為原生 ul/li/button，保留富文字標籤／說明，方向鍵、Home/End 移動真實焦點，Enter/Space 使用原生按鈕啟動。搜尋輸入仍可 Enter 啟動首個候選，無結果訊息使用持續存在的 output 公告。
- 畫布以既有原生 Add 按鈕承接焦點、Escape 復原與 Ctrl+Z/D 快捷鍵，不再把非互動容器當焦點 widget。Space/Enter 保留原生 click，避免雙開搜尋。
- Delete/Backspace 僅由原有 VueFlow document keyboard owner 處理 nodes **與 edges**，沒有另加會重複／漏刪邊的刪除路徑；背景雙擊仍開搜尋，保留 node/edge/handle/control 排除 guard。
- diff 改成有 accessible name 的 section 捲動區，保留 tabindex 讓純文字長內容可鍵盤捲動。這符合 [MDN overflow accessibility 指引](https://developer.mozilla.org/en-US/docs/Web/CSS/Reference/Properties/overflow#accessibility)，但仍可能命中 S6845，故列人工審查候選而不是宣稱 scanner 已清除。
- 真實 ul 使用 role=list 保留 Safari 在 list-style:none 下的清單語意，依 [MDN list-style accessibility 指引](https://developer.mozilla.org/en-US/docs/Web/CSS/Reference/Properties/list-style#accessibility)。不是把任意 div 填入假互動 role。
- 新增／保留 browser 測試檢查命令清單與焦點、pane 雙擊、edge Delete／單次 undo、duplicate／undo／Space／Escape、長 diff PageDown。58 項 E2E 目前僅收集成功，執行尚待 CI。

### 回歸驗證另外發現的取消競態（非原 55 項之一）

既有 acquisition 在檢查 ctx 之後，DB query 仍可能回傳 SQLite `interrupted (9)`，同時 request context 已取消；原測試要求 `errors.Is(context.Canceled)` 因而暴露既有分類缺口。本輪只在五個**非 nil DB error** 出口把同時存在的 context error 與原 driver error `errors.Join`，讓兩者都可由 `errors.Is` 識別。

這是保留並暴露同時發生的取消，**不表示可以證明 driver error 必然由取消造成**。成功操作即使稍後 ctx cancel 仍不改判失敗，authorization/model domain denial 也不改。新增 nil、active、canceled、deadline、原 driver cause 測試，原 16-worker reservation/idempotent-release 測試未刪除或放寬，已連跑 20 次通過。

## 驗證狀態與未驗證限制

- SQLite + 真實 PostgreSQL 的完整 store race 兩次通過，包含 ownership、panic／未提交 rollback、Commit failure、options、已關閉 DB、publication callback 不重播、雙方言 migration 與 SQL value binding。
- OAuth race、CodexVersion race 通過；transaction diagnostic focused tests 通過。
- 第一次全 Go race 執行到結束：accounts 的既有取消分類競態已修正；API 的 continuation `read-timeout` 子案例遇到一次 WS 5 秒 read timeout。原 API 案例不改 timeout／斷言，5 個子案例連跑 3 次通過（40.8 秒）；不把這次重跑當作第一次全套已成功。
- 最終真實 PostgreSQL／Redis `go test -race -p 2 -json ./... ./.github/compose_tls -count=1 -timeout=30m` 中，API 全部 131 個 top-level 測試通過（668.595 秒），其餘 backend／TLS package 也通過。限制 package 同時數以減少資源競爭，沒有減少測試內容。該次命令因 Vite 同時重建 assets，webui 編譯讀到已移除的舊雜湊檔名而 exit 1；這是驗證安排造成的產物競態，不能把原命令寫成通過。最終 Vite build 凍結後，`go test -race -json ./internal/webui -count=1 -timeout=5m` 通過（1.012 秒），`go build ./... ./.github/compose_tls` 及 diff-check 通過。合併這些精確驗證紀錄，22 個有測試 package 均有成功結果，3 個 package 無測試；不是宣稱單次 aggregate exit 0。
- 前端最終程式已通過 typecheck（包括 E2E 型別）、Vite build、rules／graph／元件與靜態可及性檢查、核心 SonarJS 規則掃描及 diff-check；測試腳本的 capture／catalog／quota／billing 亦通過。
- 本輪 Chromium E2E 尚未執行：本機 browser process 被 sandbox socket 限制阻擋；未繞過拒絕。舊 commit 的 57 個通過不能冒充目前程式驗證。待獲准推送後由正式 CI 執行；仍需跨瀏覽器／assistive technology 審核。
- 尚未取得本輪 Sonar analysis SHA／Quality Gate／overall ratings。人工候選不能用本機測試代替正式 scanner 或授權 disposition。

## 相容性、效能與發布界線

DB schema、資料型別、statement 執行順序、wire `then`、OAuth 註冊 redirect、正常 HTTP／WS 支援維持。交易 callback 不額外自動 Commit，不重播業務 callback；新增 rollback defer 於已提交交易為無作用清理。前端測試平行化只針對隔離狀態，沒有宣稱量測到的 production 效能提升。

主要風險是 transaction 重構範圍較廣，以及前端焦點／螢幕閱讀器行為需要正式 browser 驗證。保留 reduced-motion 全域 override，不為移除 `!important` 破壞使用者動態效果偏好；保留 HTTP clipboard 相容 fallback，明示其 deprecated API 風險。

使用者已批准發布至既有 `fix/sonarcloud-report` 分支並建立新的草稿 PR。這份紀錄不授權自動合併、部署、改 Sonar 規則或伺服器端 disposition。

## Sonar 人工審查不是程式修復

[SonarQube Cloud 官方文件](https://docs.sonarsource.com/sonarqube-cloud/managing-your-projects/issues/editing) 區分 Accepted（決定稍後處理）與 False positive（分析判斷有誤），需要 Administer Issues 權限；這些狀態會影響報告／評級。不能為了 A 而混用兩者。19 項候選仍須對照正式重掃後的實際 issue、逐項確認理由；4 項產品取捨須明確接受剩餘風險。未取得授權前不改任何狀態，也不把這些項目算成 32 項程式修復。

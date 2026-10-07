# Pi Gateway 账号格式与迁移

本文件定义账号导入导出的标准 v1，账号记录只包含邮箱、主 ChatGPT 凭证和可选 Codex 凭证。普通账号列表始终省略凭证；只有管理员显式导出时包含原始
token。

## 在 Web 界面操作

账号页支持单选、本页全选、选择全部筛选结果。选择跨页保留；切换筛选不会自动取消已选账号，工具栏显示实际已选数量。清除选择后可导出全部账号，有选择时导出所选账号。

批量启用／禁用只修改启用状态。批量恢复清除错误、连续失败、健康冷却与失败率，保留启用状态、凭证、额度、使用记录和模型限制。批量加入分组追加成员关系，保留已有分组。操作按账号执行，失败不阻断其他账号，结果窗口逐项展示成功或失败；每次最多
1000 个账号。

导入支持多选 JSON 文件或粘贴 JSON。每个文件最多 16 MiB、1000 个账号。可自动识别格式，也可指定 Pi
Gateway、sub2api、CLIProxyAPI。指定目标账号只适用于单条外部 Codex 凭证。

Codex 凭证用于额度查询和模型目录，生成请求使用主 ChatGPT 凭证。未匹配的 Codex 凭证创建停用账号，列表提供“关联
ChatGPT”按钮。成功授权相同邮箱的 ChatGPT 凭证后，该账号启用，保留原 Codex 凭证、分组和管理配置；不能直接启用缺少主凭证的账号。

## 标准 v1 JSON

```json
{
  "type": "pi-gateway-accounts",
  "version": 1,
  "exported_at": "2026-10-07T08:00:00Z",
  "accounts": [
    {
      "email": "example@example.com",
      "chatgpt": {
        "access_token": "CHATGPT_ACCESS_TOKEN",
        "refresh_token": "CHATGPT_REFRESH_TOKEN",
        "client_id": "ACTUAL_ISSUED_CHATGPT_CLIENT_ID",
        "expires_at": 1791446400000
      },
      "codex": {
        "access_token": "CODEX_ACCESS_TOKEN",
        "refresh_token": "CODEX_REFRESH_TOKEN",
        "account_id": "CODEX_ACCOUNT_ID",
        "expires_at": 1791446400000
      }
    }
  ]
}
```

以上 token 只是占位示例，实际使用原始导出文件。`chatgpt` 和 `codex` 至少提供一种；只提供 Codex
的账号始终停用。原生凭证离线导入，不自动向上游请求刷新或验证 token 是否仍有效，后续刷新／请求会验证其可用性。

### 顶层字段

| 字段            | 含义                        |
|---------------|---------------------------|
| `type`        | 必须为 `pi-gateway-accounts` |
| `version`     | 必须为整数 `1`；未知版本拒绝导入        |
| `exported_at` | 导出时间，RFC 3339 UTC；导入不依赖此值 |
| `accounts`    | 1–1000 条账号记录              |

### 账号字段

| 字段        | 说明与默认值                                                                                  |
|-----------|-----------------------------------------------------------------------------------------|
| `email`   | 邮箱身份，最多 320 字节；匹配忽略大小写；可缺省并从凭证元数据补充                                                     |
| `chatgpt` | 主生成凭证，含 `access_token`、`refresh_token`、实际签发的 `client_id`；不得填 Codex client ID 或动态注册占位 ID |
| `codex`   | 可选额度／目录凭证，含 `access_token`、`refresh_token`、Codex `account_id`                           |

账号记录只有以上三个字段。凭证中的 `client_id`、Codex `account_id` 和 `expires_at` 是使用或刷新凭证所需的元数据，保留在凭证对象内。

导出不包含名称、订阅字段、本地账号 ID、启用状态、权重、并发数、代理、协议、冷却策略、分组、模型限制或任何其他账号配置。导入也不读取这些字段：兼容旧版
v1 文件及外部文件时，多余配置即使存在也会忽略。

新建账号使用本地默认配置：名称由邮箱生成，缺少邮箱时使用 Codex 身份或 ChatGPT；权重 1、并发 3、429
冷却继承全局、协议继承全局、无账号代理／分组／模型限制。包含主 ChatGPT 凭证的账号默认启用，Codex-only 账号停用。订阅类型仅从凭证元数据提取。

两个凭证对象的 `expires_at` 都使用 **Unix 毫秒**，不是秒。0 或缺省表示未知，后续按正常凭证刷新逻辑处理。`id_token` 可缺省。Codex
账号 ID 若能从 JWT 解析，必须与提供的 ID 一致。JWT 元数据解析不等于签名或有效性验证。

### 重复与恢复语义

原生备份按 Codex 账号 ID 或邮箱匹配；无邮箱的主 ChatGPT 凭证按 refresh token 匹配；已存在的账号返回 `skipped`
，不会覆盖正在轮换的凭证和配置。同一导入中的重复账号也按此规则处理。多条现有账号同时匹配时返回失败，避免关联到错误账号。

不迁移运行状态：数据库自增 ID、健康错误／冷却、统计计数、使用记录、缓存的额度／模型目录、API key、管理员会话均不在账号文件内。新建主凭证账号从
ready 状态开始；Codex-only 账号为 unknown 且停用。凭证导出不等同于完整数据库备份。

## sub2api Codex 导入

支持 `sub2api-data`／旧版 `sub2api-bundle` 的 version 1 文件、`{"data": <导出文件>}` 包装、账号数组或单账号对象。仅导入
`platform: "openai"`、`type: "oauth"` 的 Codex 凭证，其他平台、API key、PAT 或缺少 refresh token 的记录显示失败。

```json
{
  "type": "sub2api-data",
  "version": 1,
  "accounts": [
    {
      "platform": "openai",
      "type": "oauth",
      "credentials": {
        "access_token": "CODEX_ACCESS_TOKEN",
        "refresh_token": "CODEX_REFRESH_TOKEN",
        "id_token": "OPTIONAL_ID_TOKEN",
        "chatgpt_account_id": "CODEX_ACCOUNT_ID",
        "email": "example@example.com",
        "expires_at": "2026-10-08T08:00:00Z"
      }
    }
  ]
}
```

读取 `credentials` 中的凭证，`chatgpt_account_id` 兼容 `account_id`。邮箱、账号 ID 缺省时尝试从 token 元数据补充，订阅类型仅从
token 元数据提取。`credentials.expires_at` 支持 RFC 3339、Unix 秒或毫秒（数字或数字字符串），统一转换为本项目毫秒。缺省则尝试
JWT `exp`，仍未知时为 0。外部名称、订阅字段、代理、调度权重、分组、状态、计费配置不迁移。

来源：[sub2api 导出结构](https://github.com/Wei-Shaw/sub2api/blob/main/backend/internal/handler/admin/account_data.go)、[OAuth 凭证字段](https://github.com/Wei-Shaw/sub2api/blob/main/backend/internal/service/openai_oauth_service.go)。

## CLIProxyAPI Codex 导入

支持一个 `type: "codex"` 凭证 JSON、同格式数组，或在 Web 中多选多个凭证 JSON 文件。

```json
{
  "type": "codex",
  "access_token": "CODEX_ACCESS_TOKEN",
  "refresh_token": "CODEX_REFRESH_TOKEN",
  "id_token": "OPTIONAL_ID_TOKEN",
  "account_id": "CODEX_ACCOUNT_ID",
  "email": "example@example.com",
  "expired": "2026-10-08T08:00:00Z",
  "last_refresh": "2026-10-07T08:00:00Z"
}
```

`expired` 是凭证到期时间，兼容 `expires_at`。到期格式与 sub2api 相同。`last_refresh`、任意扩展元数据不迁移；这些文件只导入
Codex 槽位，不作为 ChatGPT 主凭证。

来源：[CLIProxyAPI CodexTokenStorage](https://github.com/router-for-me/CLIProxyAPI/blob/main/internal/auth/codex/token.go)。

### 外部凭证关联规则

自动匹配邮箱或已有 Codex 账号 ID；多重匹配失败。也可指定一个现有账号，但已知邮箱必须一致，已关联的非空 Codex 账号 ID
必须一致。匹配成功仅更新 Codex 凭证并使原 Codex 目录缓存失效，保留主 ChatGPT 凭证、启用状态、配置和使用记录。重复导入新建账号的凭证会关联同一账号，不重复创建。

## 管理 API

所有接口需要管理员 Bearer 会话。

| 接口                          | 请求                                                                                                                    | 响应                                                                          |
|-----------------------------|-----------------------------------------------------------------------------------------------------------------------|-----------------------------------------------------------------------------|
| `POST /api/accounts/batch`  | `{"ids":[1,2],"action":"enable"}`；action 支持 `enable`、`disable`、`recover`、`add_groups`，加入分组另传 `group_ids`              | `{"results":[{"index":0,"id":1,"status":"success"}]}`                       |
| `POST /api/accounts/export` | `{"ids":[1,2]}`；必填，导出全部时传全部 ID                                                                                        | 原生标准 v1 JSON，`Cache-Control: no-store`                                      |
| `POST /api/accounts/import` | `{"format":"auto","data":<JSON>}`；format 可为 `auto`、`pi-gateway`、`sub2api`、`cliproxyapi`；单个外部凭证可另传 `target_account_id` | `{"format":"cliproxyapi","results":[{"index":0,"id":1,"status":"linked"}]}` |

导入结果 status 为 `created`、`linked`、`skipped`、`failed`，失败有不包含凭证的 `error`
。格式／版本错误拒绝整个文件；逐条账号错误不阻断其他记录。审计仅记录来源、动作和数量，不写入凭证。

## 登录持久化

管理员会话保存在 SQLite／PostgreSQL 的 `admin_sessions` 中，只保存 token 的 SHA-256 摘要，浏览器继续保存原会话
token。网关重启后未到期会话继续有效；默认有效期 1440 分钟，沿用 `admin.session_ttl_minutes`。持久化管理会话不改变网关整体的单活动实例部署约束。

主动退出立即撤销该会话。密码改变或管理员用户名改变后，原会话不能继续认证；过期会话不延长。短暂网络错误或会话存储不可用返回非
401，前端保留 token；明确的 401 才清除登录。升级前的内存会话需重新登录一次，之后可跨重启保留。

# 规则 V2

规则引擎只执行 JSON、数组、字符串、条件、变量及流程操作。协议字段、客户端 SDK
元数据、模型映射与压缩请求字段都由普通规则描述。旧高级动作仅作为导入兼容格式，保存时展开为底层步骤；新增动作菜单不再提供这些动作。

## 流水线与输入

| 阶段                  | 当前数据                      | 使用方式                                                     |
|---------------------|---------------------------|----------------------------------------------------------|
| `client_request`    | 解开传输封装、展开网关压缩引用后的客户端 JSON | 在规范化之前修改客户端输入                                            |
| `request_normalize` | 上一阶段结果                    | 协议默认规则构造请求，应用模型映射及默认值                                    |
| `request`           | 规范化后的请求                   | 应用用户策略；在账号选择前确定模型                                        |
| `request_finalize`  | 策略处理后的请求                  | 协议默认规则选择普通创建或压缩 schema；随后可追加自定义步骤                        |
| `upstream_headers`  | 小写请求头名 → 字符串数组            | 发送前构造或修改客户端元数据；`context/transport` 为 `sse` 或 `websocket` |
| `response_event`    | 单个上游事件 JSON               | 在转发或聚合前处理事件                                              |
| `response_body`     | 非流式聚合 JSON                | 在返回客户端之前修改正文                                             |

同阶段按 priority 升序、order_index、ID 执行。默认协议规则 priority 为
-1000。将自定义规则放在默认规则之前可预处理，放在之后可覆盖其结果。默认规则可编辑或停用；编辑后的结果影响实际请求。压缩触发识别、鉴权、账户权限、传输握手及压缩引用存储仍由网关负责。

`current` 读取前序步骤结果；`original` 读取阶段开始的输入；`client` 读取最初客户端请求；`context` 读取网关事实；`vars`
读取本条规则变量。在遍历内部，`current` 和 `original` 分别变为元素结果和元素原值，`item` 指向元素原值。JSON Pointer 空串选择根，
`/` 分隔路径段，`~0` 转义 `~`，`~1` 转义 `/`。

## 组合方式

- `sequence`：按顺序执行 steps。
- `if`：按 predicate 选择 then 或 else。
- `for_each`：处理数组或对象的每个元素，keep 在处理后判断保留；bind 在 vars 中提供元素原值、原始下标、键及路径。
- `walk`：先子节点再父节点遍历子树；predicate 选择节点；keep 可删除子节点。
- `let`：保存计算值到变量；变量在规则、遍历、scope 之间隔离。
- `scope` / `call`：定义和调用局部片段；按定义位置解析片段，编译时拒绝直接或间接递归。

普通 JSON 对象是字面量。`{"$ref":{"source":"current","path":"/model"}}` 读取字段。
`{"$expr":{"op":"concat","args":["prefix-",{"$ref":{"path":"/model"}}]}}` 计算值。`$literal` 可转义 `$ref`、`$expr`、
`$literal` 键。`coalesce` 跳过缺失与 null，但保留 false、0、空字符串；`and` / `or` 短路求值。缺失引用用于赋值时报错。

例如，修改实际发送的客户端元数据，只需要一条 `upstream_headers` 规则：

```json
{
  "schema_version": 2,
  "name": "客户端元数据",
  "enabled": true,
  "priority": 100,
  "phase": "upstream_headers",
  "when": {"op": "always"},
  "actions": [
    {"id": "sdk", "type": "json_set", "params": {"path": "/x-stainless-package-version", "value": ["custom-version"]}},
    {"id": "agent", "type": "json_set", "params": {"path": "/user-agent", "value": ["custom-client"]}}
  ],
  "on_error": "abort"
}
```

环境上下文移除可组合 `for_each`、`if`、`text_replace` 和 keep；嵌套协议可用 `walk`
。完整兼容例子可展开已迁移规则查看，每个参数的悬浮帮助提供格式、默认值和示例，计算运算符提供参数顺序及结果示例。

例如下面的普通步骤清除 `/input` 子树中 `text` 字段的环境标签；如果清除后整段文本为空，则删除该内容节点。无匹配的节点保持原值。在步骤编辑器添加“递归遍历”，将路径填为
`/input`，节点条件选正则匹配 `/text`，内部步骤选“替换文本”，最后设置保留条件即可；不需要增加环境清理专用动作。

```json
{
  "id": "clean-text-nodes",
  "type": "walk",
  "params": {
    "path": "/input",
    "predicate": {
      "op": "regex",
      "path": "/text",
      "value": "(?is)<environment_context>.*?</environment_context>"
    },
    "steps": [{
      "id": "remove-tag",
      "type": "text_replace",
      "params": {
        "path": "/text",
        "match": "regex",
        "pattern": "(?is)<environment_context>.*?</environment_context>",
        "replacement": "",
        "replace_all": true
      }
    }],
    "keep": {"op": "ne", "path": "/text", "value": ""}
  }
}
```

`walk` 的 `original` / `item` 保存进入该节点时的原值；`current` 包含子节点及当前节点已完成的修改。需要同步内容数组与元数据数组时，可以先用
`let` 保存元数据，再以 `for_each` 的绑定下标读取对应值；这也是旧环境清理规则迁移后的实现方式。

## 协议配置与迁移

`internal/rules/profiles/chatgpt-v1.json` 是版本化协议配置，包含三个可编辑默认规则。`profiles.go`
只负责加载配置。协议字段变化优先修改该配置或管理界面中的规则，无需新增引擎高级动作。

数据库升级保留旧规则名称、启用状态、优先级和来源，将高级动作展开并安装协议规则。已编辑或删除的默认规则不会在重启时重新覆盖或安装。文件中的版本用于发布默认配置；对现有数据库的后续默认升级须使用新的迁移标记，并明确保留用户修改。

协议配置使用独立升级标记 `rules.protocol.chatgpt.v1`，与语言 V2 的安装标记分开。已经安装早期 V2 的数据库也会补齐缺失的三个协议默认规则；已有同
ID 规则的内容和启用状态保持原样。该次升级完成后，用户删除的规则不会再次安装。

编辑器默认使用嵌套步骤视图，可切换流程图或 JSON；后端 schema 提供参数帮助。试运行可选择真实捕获的阶段输入，查看嵌套步骤路径、元素路径和最终差异；旧捕获缺少新阶段
checkpoint 时会明确报告样本不可用。

每条规则原子提交；失败按 on_error 中止或回滚后跳过。执行预算为每次阶段调用 100000
步，遍历元素也计入预算。访问令牌不会进入规则上下文；规则不能写入凭据、传输握手头或受保护的响应身份与用量字段。请求头必须为字符串数组，名称和控制字符在发送前校验。

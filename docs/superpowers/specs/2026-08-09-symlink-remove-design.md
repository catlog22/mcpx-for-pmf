# MCPX 符号链接删除设计

## 目标

在不允许 symlink 跟随的前提下，支持删除 Workspace 内的符号链接入口，覆盖
`node_modules` 等常见依赖目录中的 workspace link。删除对象是链接本身，不是链接目标。

## 语义与边界

- `remove_prepare` 接受 `kind=symlink` 和 `kind=directory` 的显式目标；directory 只冻结目录根，不扫描、哈希或返回子树，目录内任何文件类型都不作为 prepare 拒绝条件。
- symlink 的 manifest 条目记录相对路径、`kind=symlink`、链接文本、链接文本 SHA-256 和链接占用字节数；不读取或哈希目标内容。directory manifest 仅记录根路径和 `kind=directory`。
- symlink 目标可以位于 Workspace 内或外；目标位置只用于展示和审计，不作为删除路径，也不执行目标检查。
- absolute path、`..` 越界、symlink 组件和 Workspace 外父路径仍然拒绝。只有最终目标节点是 symlink 或 directory 时允许删除。
- directory target 直接删除目录树，不要求目录下内容是 regular file；prepare 不扫描目录，提交的 `os.Root.RemoveAll` 不跟随 symlink，symlink 作为叶子删除，其他节点由服务端递归处理。
- `submit_remove` 按冻结的显式路径处理最终目录项：最终节点为 symlink 时直接删除链接入口，不校验、读取或进入链接目标；因此已冻结的 directory 在确认后被替换成 symlink 时，也只删除这个 symlink。
- directory 是一个已批准的删除根，而不是“内容必须保持不变”的 CAS 对象：提交时以 `os.Root.RemoveAll` 删除当前目录树，目录内容增减、类型变化或内部 symlink 不阻止删除；变化写入提交审计。最终节点从 directory 变为 regular file 时仍返回 `STALE_REVISION`，避免把目录授权扩大为任意文件删除。
- 提交使用 `os.Root.Remove` 删除文件或链接目录项、使用 `os.Root.RemoveAll` 删除真实目录树；不调用 shell，不递归进入链接目标；幂等 replay 和审计规则不变。
- `_meta.mcpx/safety` 改为表达 `no_symlink_following=true` 与 `symlink_entry_delete=true`，不再把 symlink 统一标记为拒绝对象。

## 数据流

```text
显式 symlink / 含 symlink 的 directory
  -> lstat + readlink（只读取链接自身）
  -> 冻结显式 path、kind、link text、link SHA（directory 不扫描子树）
  -> 网页端模型展示 manifest 并询问用户
  -> submit_remove
  -> lstat 二次校验最终目录项
  -> symlink 使用 os.Root.Remove；directory 使用 os.Root.RemoveAll
```

## 错误与恢复

- symlink 作为最终目标时不再返回 `SYMLINK_NOT_ALLOWED`；directory 目标不因子项类型而拒绝。
- symlink 作为中间路径组件时仍返回 `SYMLINK_NOT_ALLOWED`，恢复方式是改用链接入口的显式路径。
- symlink 的目标文本在确认后改变不影响删除，因为删除的是同一路径的链接入口；最终节点消失时返回结构化 `STALE_REVISION`。
- directory 在确认后被替换成 symlink 时，提交删除链接入口；directory 内容发生变化时，提交删除当前目录树。审计保留删除根、manifest SHA、提交结果和“目录内容未枚举”标识，而不写入海量子项。
- 目标缺失、目标越界或目标自身不可访问不影响删除链接入口；服务端不跟随目标，因此不把目标状态当作删除前提。

## 验证矩阵

- 单个 symlink 指向 Workspace 内文件：删除链接，目标仍存在。
- 单个 symlink 指向 Workspace 外文件：删除链接，外部目标仍存在。
- directory manifest 只包含 directory 根路径：删除整棵目录树，不跟随链接目标。
- directory 目标包含指向 Workspace 内外的 symlink：删除链接入口，所有链接目标保持不变。
- explicit file 被替换成 regular file 的不同内容：`STALE_REVISION` 且不修改节点；被替换成 symlink 时删除链接入口。
- explicit directory 被替换成 symlink：删除链接入口；被替换成 regular file：`STALE_REVISION` 且不修改节点。
- symlink 中间路径、absolute path、`..`：prepare 拒绝且无文件变更；目录内部的特殊文件不阻止目录删除。
- submit exact replay、并发提交、审计和目录统计保持现有契约。

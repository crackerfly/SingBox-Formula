# GitHub 网页创建文件的单 LF 兼容设计

## 背景

GitHub Actions 运行 `29325579331` 已证明上一轮网页上传权限修复生效：workflow 在测试前成功恢复了 13 个已审核可执行文件。新的失败发生在 `tests/shell/test_source_package.sh`，不是 OpenWrt SDK 编译本身。

GitHub 网页端拒绝通过 **Upload files** 直接上传隐藏文件。用户因此通过 **Create new file** 创建了上游源码中的 `.env` 和 `.github/workflows/go-release-docker.yml`。网页编辑器给这两个原本没有末尾换行的文件各追加了一个 LF，导致严格 SHA-256 校验失败；除此之外，文件内容和 mode 均与锁定的上游源码一致。

本修复只解决这一种可证明等价的网页变体。它不改变插件业务逻辑、OpenWrt 包内容、上游提交锁定值，或以下刻意保留的行为：

- converter 继续监听 `:<port>`；
- 默认密码继续为 `890716`；
- 日志继续保留密码、完整订阅 URL、令牌和 cache-buster。

## 目标与非目标

### 目标

1. 原始上游文件继续通过严格 SHA-256 校验。
2. 仅允许以下两个相对 `openwrt-feed/singbox-formula/src/` 的路径多出一个末尾 LF：
   - `.env`
   - `.github/workflows/go-release-docker.yml`
3. 当前 GitHub 仓库中已由网页创建的两个文件无需删除或重新上传。
4. 其他上游路径继续要求文件存在、mode 一致且字节哈希完全匹配。
5. 修改内容全部通过 GitHub 网页正常上传，不要求 CLI、API push 或其他上传方式。

### 非目标

- 不把隐藏文件改成可选文件。
- 不允许 CRLF、两个末尾 LF、末尾空格或任何内容修改。
- 不修改锁定上游 manifest 中的原始 SHA-256。
- 不把例外扩展到其他文件。

## 完整性判定

新增一个只包含上述两个路径的显式 allowlist。每个上游文件仍先执行现有的存在性和 mode 校验，然后按以下顺序判定内容：

1. 计算原文件 SHA-256；与 manifest 一致时立即通过。
2. 原文件哈希不一致且路径不在 allowlist 时失败。
3. allowlist 路径必须以字节 `0x0a` 结尾，否则失败。
4. 只移除最后一个字节并重新计算 SHA-256；只有结果与 manifest 中的原始哈希完全一致时才通过。

该算法不会存储或替换新的“上游哈希”，也不会对文件做持久化修改。它严格表达“原始字节或原始字节加一个 LF”这两个合法状态：

- 两个 LF：移除一个后仍比原始内容多一个 LF，失败；
- CRLF：移除 LF 后仍多一个 CR，失败；
- 内容被修改后再加 LF：移除 LF 后哈希仍不一致，失败；
- 非 allowlist 文件增加 LF：不进入兼容分支，失败。

## 组件与文件

- `tests/shell/source_integrity.sh`
  - 提供一个 POSIX shell 可调用的内容判定函数；不执行测试、不修改输入文件。
- `tests/shell/test_source_integrity.sh`
  - 独立覆盖原始内容、一个 LF、两个 LF、CRLF、内容篡改和非 allowlist 路径六类行为。
- `tests/shell/fixtures/singbox-subscribe-convert-8222509.web-single-lf-paths`
  - 精确列出两个兼容路径，方便审核并防止例外散落在脚本中。
- `tests/shell/test_source_package.sh`
  - 在现有 manifest 校验中调用 helper；路径、mode、文件白名单及安全扫描保持原样。
- `README.md`
  - 说明通过 **Create new file** 创建这两个隐藏路径时，一个自动追加的 LF 会被严格兼容；不建议更改其他字节。

## 测试策略

TDD 的 RED 阶段先以临时文件重现当前失败：原始字节加一个 LF 在现有严格哈希逻辑下被拒绝。GREEN 阶段接入 helper 后验证：

1. 原始字节通过；
2. allowlist 文件增加一个 LF 通过；
3. 增加两个 LF 失败；
4. 增加 CRLF 失败；
5. 修改正文后增加 LF 失败；
6. 非 allowlist 文件增加一个 LF 失败；
7. 完整 `test_source_package.sh` 在原始源码树上通过；
8. 将两个真实文件复制到临时源码树并各追加一个 LF 后，完整源码校验通过；
9. 现有全部 shell、Node、rpcd 语法和 Go 测试保持通过。

本地环境若没有 Go 工具链，必须明确记录未执行项；最终交付仍要求 GitHub Actions 使用 workflow 固定的 Go `1.26.4` 完成 `go test -race ./...` 与 `go vet ./...`。

## 网页交付

交付一个只包含非隐藏修复文件的网页上传包。用户在仓库根目录使用 **Add file → Upload files** 上传解压后的对应顶层目录；不得上传 ZIP 本身。当前远端 `.env` 和嵌套 workflow 保持不动。上传修复包后触发的新 Actions 结果才是远端完整验证依据。

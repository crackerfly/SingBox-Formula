# Liquid Formula 1.8.5

[简体中文](#简体中文) · [English](#english)

## 简体中文

### 主要变化

- Actions 现在先将 `.git` 外的所有普通文件规范化为 `0644`，再仅将经过审核的 36 个
  可执行路径恢复为 `0755`。
- 仓库中的所有跟踪文件必须以 `100644` 提交；若 Git 索引包含虚假 `100755` mode，源码
  包校验会失败并直接列出相关文件，而不是只报告 tree digest 不匹配。
- 订阅网关将不同 Linux 架构的 `Stat_t.Nlink` 显式转换为 `uint64`，并增加 `amd64`、
  `arm`、`arm64` 交叉编译覆盖。
- 转换器源码完整性校验使用离线的路径、mode 和 SHA-256 清单；浅克隆运行时不再依赖
  1.8.3 Git 对象。
- 冻结的 `openwrt-feed/liquid-formula/src` 转换器 Go 源码保持逐字节不变，运行时行为
  没有变化。

### 验证范围

- 源码与权限回归测试覆盖正确的全 `100644` 索引、对全 `100755` 错误提交的明确拒绝，
  以及只有单个提交历史的完整网页上传形态。
- 完整 Shell、LuCI、DPI、Go/C 与发布矩阵验证，以及交付归档验证，属于最终发布验证阶段
  并在该阶段单独记录。

## English

### Main changes

- Actions now normalizes every regular file outside `.git` to `0644`, then restores only the 36
  reviewed executable paths to `0755`.
- Every tracked repository file must be committed as `100644`. If the Git index contains false
  `100755` modes, source-package validation fails and lists the offending paths instead of only
  reporting a tree-digest mismatch.
- The subscription gateway explicitly converts architecture-dependent `Stat_t.Nlink` fields to
  `uint64` and adds `amd64`, `arm`, and `arm64` cross-compilation coverage.
- Converter-source integrity verification uses an offline path/mode/SHA-256 manifest and no
  longer requires a runtime 1.8.3 Git object in a shallow checkout.
- The frozen converter Go source under `openwrt-feed/liquid-formula/src` remains byte-for-byte
  unchanged, and runtime behavior is unchanged.

### Verification scope

- Source and permission regression tests cover a valid all-`100644` index, explicit rejection of
  an invalid all-`100755` commit, and a complete web-upload shape with a single commit of history.
- The full Shell, LuCI, DPI, Go/C, and release-matrix verification, along with delivery-archive
  verification, belongs to the final release-verification stage and is recorded there separately.

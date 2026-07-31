package util

// bcrypt 相关的 GeneratePasswordHash / CheckPasswordHash 已移除。
//
// 原因: 这两个函数在本仓库中没有任何调用者(pkg/util 整个包都没有被 import),
// 而它们唯一的依赖 golang.org/x/crypto 从 v0.43.0 起要求 Go >= 1.24。
// OpenWrt 24.10 只提供 Go 1.23.x, 保留该依赖会让 24.10 的全部架构编译失败。
//
// 如果将来确实需要密码哈希, 重新引入 golang.org/x/crypto 时请同步确认
// 最低支持的 OpenWrt 版本所带的 Go 版本。

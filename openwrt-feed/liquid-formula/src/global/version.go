package global

// Version defaults to the pinned upstream release so source/OpenWrt builds
// have a stable identity even when their build helper supplies no -X override.
// Version 带 -formula 后缀: 这份源码已不是纯上游 0.7.2, 本地修复了若干
// 竞态与 Windows/OpenWrt 兼容问题, 需要在 /health 和 --version 里可区分。
var Version = "0.7.2-formula"
var GitTag = "2000.01.01.release"
var BuildTime = "2000-01-01T00:00:00+0800"

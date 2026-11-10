//go:build windows

package main

// diskSpace 在 Windows 上（只有 npm run standalone 会在 Windows 上跑后端）不检查空间。
func diskSpace(string) (free, total int64, ok bool) { return 0, 0, false }

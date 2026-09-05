// Package version 保存建置時注入的版本資訊。
package version

// Version 由建置時的 -ldflags "-X …/internal/version.Version=<v>" 注入;
// 未注入(本機 go run / go test)時為 "dev"。
var Version = "dev"

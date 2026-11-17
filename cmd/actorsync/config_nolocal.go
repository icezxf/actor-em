//go:build !local

package main

// 默认构建（不带 -tags local）时没有任何本地配置覆盖。
// Actions 里就是走这条路：全部参数来自环境变量/Secrets。
func applyLocalFile(c *Config) {}

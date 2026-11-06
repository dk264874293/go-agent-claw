package runtimeenv

import (
	"errors"
	"os"

	"github.com/joho/godotenv"
)

// LoadDotenv 加载启动目录的可选配置，现有配置损坏时停止启动。
func LoadDotenv(overwrite bool) error {
	if _, err := os.Lstat(".env"); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return errors.New("无法检查启动目录的 .env 配置")
	}
	var err error
	if overwrite {
		err = godotenv.Overload()
	} else {
		err = godotenv.Load()
	}
	if err != nil {
		// 解析错误可能带有原始密钥行，使用不含配置正文的错误。
		return errors.New("无法加载启动目录的 .env，请检查文件格式、读取权限及符号链接目标")
	}
	return nil
}

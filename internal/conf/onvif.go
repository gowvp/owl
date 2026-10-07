package conf

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// onvifPasswordLength 是缺省 ONVIF 密码的数字位数。
const onvifPasswordLength = 10

// randomONVIFPassword 使用密码学随机源生成等概率数字，随机源失败时终止启动，避免使用弱密码。
func randomONVIFPassword() string {
	password := make([]byte, onvifPasswordLength)
	limit := big.NewInt(10)
	// 固定位数生成，时间复杂度 O(onvifPasswordLength)。
	for i := range password {
		digit, err := rand.Int(rand.Reader, limit)
		if err != nil {
			panic(fmt.Errorf("生成 ONVIF 随机密码失败: %w", err))
		}
		password[i] = '0' + byte(digit.Int64())
	}
	return string(password)
}

// InitONVIF 补全并保存缺失的独立凭据，保存成功后才更新内存，保证重启仍使用同一组凭据。
func (b *Bootstrap) InitONVIF() error {
	if b.ONVIF.Username != "" && b.ONVIF.Password != "" {
		return nil
	}
	next := *b
	if next.ONVIF.Username == "" {
		next.ONVIF.Username = "admin"
	}
	if next.ONVIF.Password == "" {
		next.ONVIF.Password = randomONVIFPassword()
	}
	if err := WriteConfig(&next, next.ConfigPath); err != nil {
		return fmt.Errorf("保存 ONVIF 凭据失败: %w", err)
	}
	b.ONVIF = next.ONVIF
	return nil
}

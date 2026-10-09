package main

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/golang/freetype/truetype"
	"github.com/patrickmn/go-cache"
	"github.com/wenlng/go-captcha-assets/bindata/chars"
	"github.com/wenlng/go-captcha-assets/resources/fonts/fzshengsksjw"
	"github.com/wenlng/go-captcha-assets/resources/imagesv2"
	"github.com/wenlng/go-captcha/v2/base/option"
	"github.com/wenlng/go-captcha/v2/click"
)

const loginCaptchaThreshold = 5

var textCapt click.Captcha

type captchaClickPoint struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// loginFailKey 生成登录失败计数键。
//
// 安全加固（v1.0.98）：此前只用 IP 作键，而本应用的两条入口通道都拿不到可靠 IP——
//   ① 统一网关（unix socket）下 RemoteAddr 为空，gin 的 ClientIP() 返回 ""，
//      旧代码 `if ip == "" { return 0 }` 直接不计数 → 对外网关上暴力破解零成本；
//   ② TCP 通道下 gin 默认信任 X-Forwarded-For，每请求换一个 XFF 即可换键绕过阈值。
// 因此改为「IP + 用户名」双键：任一为空时仍有另一个兜底，
// 且 IP 为空时用共享的全局键（"anon"）计数，确保永远不会出现"不计数"的通道。
func loginFailKey(ip, username string) string {
	if ip == "" {
		ip = "anon"
	}
	if username == "" {
		return "lf:" + ip
	}
	return "lf:" + ip + ":" + username
}

func setLoginFail(ip, username string) int {
	key := loginFailKey(ip, username)
	if v, ok := cc.Get(key); ok {
		if n, ok := v.(int); ok {
			n++
			cc.Set(key, n, 30*time.Minute)
			return n
		}
	}

	cc.Set(key, 1, 30*time.Minute)

	return 1
}

// getLoginFail 取失败次数：返回 IP 键与用户名键中的较大值。
// 只查 IP 会让攻击者换 IP 后清零；只查用户名会让撞同一个账号的分布式攻击漏网。
func getLoginFail(ip, username string) int {
	n := 0
	for _, k := range []string{"lf:" + orDefault(ip, "anon"), loginFailKey(ip, username)} {
		if v, ok := cc.Get(k); ok {
			if m, ok := v.(int); ok && m > n {
				n = m
			}
		}
	}

	return n
}

func resetLoginFail(ip, username string) {
	cc.Delete("lf:" + orDefault(ip, "anon"))
	cc.Delete(loginFailKey(ip, username))
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func init() {
	builder := click.NewBuilder(
		click.WithRangeLen(option.RangeVal{Min: 4, Max: 6}),
		click.WithRangeVerifyLen(option.RangeVal{Min: 2, Max: 4}),
	)

	fonts, err := fzshengsksjw.GetFont()
	if err != nil {
		log.Fatalln(err)
	}

	imgs, err := imagesv2.GetImages()
	if err != nil {
		log.Fatalln(err)
	}

	builder.SetResources(
		click.WithChars(chars.GetChineseChars()),
		click.WithFonts([]*truetype.Font{fonts}),
		click.WithBackgrounds(imgs),
	)

	textCapt = builder.Make()
}

func getCaptcha() (string, string, string, error) {
	captData, err := textCapt.Generate()
	if err != nil {
		return "", "", "", err
	}

	dotData := captData.GetData()
	if dotData == nil {
		return "", "", "", fmt.Errorf("生成验证码数据失败")
	}

	var mBase64, tBase64 string
	mBase64, err = captData.GetMasterImage().ToBase64()
	if err != nil {
		return "", "", "", err
	}
	tBase64, err = captData.GetThumbImage().ToBase64()
	if err != nil {
		return "", "", "", err
	}

	key := "captcha:" + genRandomString(16)
	cc.Set(key, dotData, cache.DefaultExpiration)

	return key, mBase64, tBase64, nil
}

func checkCaptcha(key, dots string) bool {
	v, ok := cc.Get(key)
	if !ok {
		return false
	}

	cc.Delete(key)

	dotData, ok := v.(map[int]*click.Dot)
	if !ok {
		return false
	}

	var userPoints []captchaClickPoint
	if err := json.Unmarshal([]byte(dots), &userPoints); err != nil {
		return false
	}

	if len(userPoints) != len(dotData) {
		return false
	}

	for i := 0; i < len(dotData); i++ {
		dot, exists := dotData[i]
		if !exists {
			return false
		}
		p := userPoints[i]
		if !click.Validate(p.X, p.Y, dot.X, dot.Y, dot.Width, dot.Height, 5) {
			return false
		}
	}

	return true
}

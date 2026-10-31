package api

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"
	"time"
)

// Plan 是一个套餐的资源上限。接入付费后按用户的套餐返回不同的 Plan。
type Plan struct {
	Name           string   `json:"name"`
	MaxSide        int      `json:"maxSide"`        // 地图边长上限
	MaxFinalPixels int      `json:"maxFinalPixels"` // 放大后的像素上限
	MaxDrops       int      `json:"maxDrops"`
	MaxTimes       int      `json:"maxTimes"`
	MaxEnlarge     int      `json:"maxEnlarge"`
	MaxLive        int      `json:"maxLive"` // 同时在内存中运行的模拟数
	Exports        []string `json:"exports"` // 允许的导出格式
}

var Plans = map[string]Plan{
	"free": {
		Name: "free", MaxSide: 800, MaxFinalPixels: 1024 * 1024, MaxDrops: 200_000, MaxTimes: 200, MaxEnlarge: 2, MaxLive: 3,
		Exports: []string{"png", "height16", "r16", "flow16", "erosion16", "watermask"},
	},
}

func (p Plan) allowsExport(f string) bool {
	for _, e := range p.Exports {
		if e == f {
			return true
		}
	}
	return false
}

// Principal 是发起请求的人
type Principal struct {
	ID   string `json:"id"`
	Plan Plan   `json:"plan"`
}

// Authenticator 从请求识别用户；必要时可以写响应(例如下发 cookie)
type Authenticator interface {
	Identify(w http.ResponseWriter, r *http.Request) Principal
}

// VisitorAuth 用 cookie 中的随机访客 ID 识别匿名用户，全部使用 free 套餐。
// 接入真实账号时替换为基于登录会话的实现即可。
type VisitorAuth struct {
	Secure bool // 只通过 HTTPS 发送 cookie
}

const visitorCookie = "tg_vid"

var validVisitor = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (a VisitorAuth) Identify(w http.ResponseWriter, r *http.Request) Principal {
	id := ""
	if c, err := r.Cookie(visitorCookie); err == nil && validVisitor.MatchString(c.Value) {
		id = c.Value
	} else {
		b := make([]byte, 16)
		rand.Read(b)
		id = hex.EncodeToString(b)
		http.SetCookie(w, &http.Cookie{
			Name: visitorCookie, Value: id, Path: "/",
			Expires: time.Now().AddDate(1, 0, 0), HttpOnly: true, Secure: a.Secure, SameSite: http.SameSiteLaxMode,
		})
	}
	return Principal{ID: id, Plan: Plans["free"]}
}

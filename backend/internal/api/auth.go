package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"dianjing/backend/internal/store"
)

// ---- auth ----

type loginReq struct {
	Code string `json:"code"`
}

type loginResp struct {
	Token    string `json:"token"`
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
	Avatar   string `json:"avatar"`
	Mobile   string `json:"mobile"`
}

// handleLogin exchanges a wx.login code for openid via WeChat API and
// issues a signed token. The session_key returned by WeChat is stored
// server-side only; it is required to sign xpay payments (M2).
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
		failMsg(w, "缺少 code")
		return
	}
	sess, err := s.wxCodeToSession(req.Code)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	if sess.Openid == "" {
		fail(w, http.StatusBadGateway, "微信返回缺少 openid")
		return
	}

	u, err := s.St.UserByOpenid(sess.Openid)
	now := store.Now()
	switch {
	case err == nil:
		if _, e := s.St.DB.Exec(`UPDATE users SET session_key=?, last_login_at=? WHERE id=?`,
			sess.SessionKey, now, u.ID); e != nil {
			fail(w, http.StatusInternalServerError, e.Error())
			return
		}
	case isNoRows(err):
		res, e := s.St.DB.Exec(`INSERT INTO users (openid, session_key, nickname, avatar, created_at, last_login_at)
			VALUES (?,?,?,?,?,?)`, sess.Openid, sess.SessionKey, "微信用户", "", now, now)
		if e != nil {
			fail(w, http.StatusInternalServerError, e.Error())
			return
		}
		id, _ := res.LastInsertId()
		u = &store.User{ID: id, Openid: sess.Openid, Nickname: "微信用户"}
	default:
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if u.Status != 1 {
		fail(w, http.StatusForbidden, "账号已被封禁")
		return
	}
	s.St.Event("login", "user", u.ID, fmt.Sprintf(`{"openid":%q}`, sess.Openid))
	okJSON(w, loginResp{
		Token:    s.signToken(u.ID, now+s.Cfg.TokenTTL),
		UserID:   u.ID,
		Nickname: u.Nickname,
		Avatar:   u.Avatar,
		Mobile:   u.Mobile,
	})
}

type wxSession struct {
	Openid     string `json:"openid"`
	SessionKey string `json:"session_key"`
	UnionID    string `json:"unionid"`
	ErrCode    int    `json:"errcode"`
	ErrMsg     string `json:"errmsg"`
}

func (s *Server) wxCodeToSession(code string) (*wxSession, error) {
	url := fmt.Sprintf("https://api.weixin.qq.com/sns/jscode2session?appid=%s&secret=%s&js_code=%s&grant_type=authorization_code",
		s.Cfg.WxAppID, s.Cfg.WxAppSecret, code)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("请求微信登录接口失败: %w", err)
	}
	defer resp.Body.Close()
	var sess wxSession
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		return nil, fmt.Errorf("解析微信登录响应失败: %w", err)
	}
	if sess.ErrCode != 0 {
		return nil, fmt.Errorf("微信登录失败(%d): %s", sess.ErrCode, sess.ErrMsg)
	}
	return &sess, nil
}

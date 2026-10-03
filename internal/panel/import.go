// import.go 凭证导入：把在别处登录得到的 auth 块搬进这台服务器的面板（面板唯一写入口）。
//
//	POST /panel/api/import/config      Authorization: Bearer <api_key>
//	Body: {"auth":{jwt_token,access_token,...}} 或同名扁平对象；
//	      base64(上面任一) 也可以（text/plain / application/json 均可）
//
// 只收 auth 块：整份配置会连带 server.api_key 与端口的远程改写权，不收。
// 语义是覆盖：响应回显被替换掉的旧身份——导入前能确认"我在改哪台机器上的哪个号"。
// 令牌不进日志，只记 user_id。
package panel

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/misakano7545/deveco2api-panel/internal/auth"
	"github.com/misakano7545/deveco2api-panel/internal/logfmt"
)

// authBlock 可导入的凭证字段（与 config.AuthConfig / Python [deveco.auth] 同名）。
type authBlock struct {
	JWTToken     string `json:"jwt_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	UserID       string `json:"user_id"`
	UserName     string `json:"user_name"`
}

// maxImportBytes 导入体上限：一份 auth 块几百字节，64KB 足够且挡住大包。
const maxImportBytes = 64 << 10

func (p *Panel) importConfig(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxImportBytes))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "读取请求体失败: "+err.Error())
		return
	}
	body := bytes.TrimSpace(raw)
	if len(body) == 0 {
		writeErr(w, http.StatusBadRequest, "请求体为空")
		return
	}
	// 非 JSON 开头就当 base64 试解（Base64 只是传输编码，不是加密）
	if body[0] != '{' {
		dec, ok := decodeAnyBase64(string(body))
		if !ok {
			writeErr(w, http.StatusBadRequest, "既不是 JSON 也不是可解码的 base64")
			return
		}
		body = bytes.TrimSpace(dec)
	}

	var doc struct {
		Auth *authBlock `json:"auth"`
		authBlock
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 解析失败: "+err.Error())
		return
	}
	blk := doc.Auth
	if blk == nil {
		blk = &doc.authBlock
	}
	next := auth.Tokens{
		JWTToken:     strings.TrimSpace(blk.JWTToken),
		AccessToken:  strings.TrimSpace(blk.AccessToken),
		RefreshToken: strings.TrimSpace(blk.RefreshToken),
		UserID:       strings.TrimSpace(blk.UserID),
		UserName:     strings.TrimSpace(blk.UserName),
	}
	if next.JWTToken == "" || next.AccessToken == "" {
		writeErr(w, http.StatusBadRequest, "缺少 jwt_token 或 access_token")
		return
	}
	if p.cfg.ImportAuth == nil {
		writeErr(w, http.StatusNotImplemented, "本实例未启用导入")
		return
	}

	prev, err := p.cfg.ImportAuth(next)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "保存失败: "+err.Error())
		return
	}
	logfmt.Infof("面板导入凭证: %s → %s", accountLabel(prev.UserName, prev.UserID), accountLabel(next.UserName, next.UserID))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"replaced": map[string]any{"user_id": prev.UserID, "user_name": prev.UserName},
		"account": map[string]any{
			"user_id": next.UserID, "user_name": next.UserName,
			"jwt_days_left": auth.JWTDaysLeft(next.JWTToken),
		},
	})
}

func accountLabel(name, id string) string {
	if name == "" && id == "" {
		return "（空）"
	}
	if name == "" {
		return id
	}
	return name + "(" + id + ")"
}

// decodeAnyBase64 依次尝试标准 / 无填充 / URL 三种 base64 变体。
func decodeAnyBase64(s string) ([]byte, bool) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if dec, err := enc.DecodeString(s); err == nil {
			return dec, true
		}
	}
	return nil, false
}

package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/x509"
	"embed"
	"encoding/csv"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"crypto/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gavintan/gopkg/aes"
	"github.com/gavintan/gopkg/tools"
	"github.com/gin-contrib/sessions"
	gormsessions "github.com/gin-contrib/sessions/gorm"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/patrickmn/go-cache"
	"github.com/robfig/cron/v3"
	"github.com/spf13/viper"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	gLogger "gorm.io/gorm/logger"
)

type ClientData struct {
	ID             string  `json:"id"`
	Rip            string  `json:"rip"`
	Vip            string  `json:"vip"`
	Vip6           string  `json:"vip6"`
	RecvBytes      float64 `json:"recvBytes"`
	SendBytes      float64 `json:"sendBytes"`
	ConnDate       string  `json:"connDate"`
	OnlineTime     string  `json:"onlineTime"`
	Username       string  `json:"username"`
	CommonName     string  `json:"commonName"`
	IsNftBlacklist bool    `json:"isNftBlacklist"`
}

type ServerData struct {
	RunDate    string
	Status     string
	StatusDesc string
	Address    string
	Nclients   string
	BytesIn    string
	BytesOut   string
	Mode       string
	Version    string // OpenVPN 语义版本号（已裁掉编译平台/特性后缀）
	FpkVersion string // 本 FPK 自身版本
}

type ClientConfigData struct {
	Name      string  `json:"name"`
	FullName  string  `json:"fullName"`
	File      string  `json:"file"`
	Date      string  `json:"date"`
	Vip       string  `json:"vip"`
	RecvBytes float64 `json:"recvBytes"`
	SendBytes float64 `json:"sendBytes"`
	LastSeen  string  `json:"lastSeen"`
	Online    bool    `json:"online"`
}

type Params struct {
	Draw        int    `json:"draw" form:"draw"`
	Offset      int    `json:"offset" form:"offset"`
	Start       int    `json:"start" form:"start"`
	Limit       int    `json:"limit" form:"limit"`
	Length      int    `json:"length" form:"length"`
	OrderColumn string `json:"orderColumn" form:"orderColumn"`
	Order       string `json:"order" form:"order"`
	Search      string `json:"search" form:"search"`
	Qt          string `json:"qt" form:"qt"`
}

type CertData struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Kind      string `json:"kind"` // 结构化类型：ca / server / client / crl（前端计数用，避免依赖中文 type）
	Revoked   bool   `json:"revoked"`
	Subject   string `json:"subject"`
	Issuer    string `json:"issuer"`
	NotBefore string `json:"notBefore"`
	NotAfter  string `json:"notAfter"`
	ExpiresIn string `json:"expiresIn"`
	Status    string `json:"status"`
	SerialNo  string `json:"serialNo"`
}

type ovpn struct {
	address string
}

var (
	version = "1.0.0"
	//go:embed templates
	FS embed.FS

	cc     = cache.New(5*time.Minute, 10*time.Minute)
	db     *gorm.DB
	logger = gLogger.New(
		log.New(os.Stdout, "[OPENVPN-WEB] "+time.Now().Format("2006-01-02 15:04:05.000")+" MAIN ", 0),
		gLogger.Config{
			SlowThreshold:             time.Second,
			LogLevel:                  gLogger.Error,
			IgnoreRecordNotFoundError: true,
			Colorful:                  true,
		},
	)
	ovData = os.Getenv("OVPN_DATA")
	conf   config

	// reInfoLine 预编译：sendCommand 读取循环内每次都要剥离 >INFO 心跳行，提到包级只编译一次
	reInfoLine = regexp.MustCompile(">INFO(.)*\r\n")
)

// ovpnHelper 是证书/客户端生成辅助脚本（替代原 docker-entrypoint.sh 的调用，
// 适配纯 FPK 无 docker 环境）。可通过 OVPN_HELPER 环境变量覆盖路径。
var ovpnHelper = func() string {
	if v := os.Getenv("OVPN_HELPER"); v != "" {
		return v
	}
	return "ovpn-helper.sh"
}()

func (ov *ovpn) sendCommand(command string) (string, error) {
	var data string
	var sb strings.Builder

	conn, err := net.DialTimeout("tcp", ov.address, time.Second*3)
	if err != nil {
		logger.Error(context.Background(), err.Error())
		return data, err
	}

	defer conn.Close()

	conn.SetDeadline(time.Now().Add(time.Second * 3))
	conn.Write([]byte(fmt.Sprintf("%s\n", command)))

	for {
		buf := make([]byte, 1024)
		n, err := conn.Read(buf)

		if str := reInfoLine.ReplaceAllString(string(buf[:n]), ""); str != "" {
			sb.Write([]byte(str))
		}

		if err != nil || strings.HasSuffix(sb.String(), "\r\nEND\r\n") || strings.HasPrefix(sb.String(), "SUCCESS:") {
			break
		}
	}

	data = strings.TrimPrefix(strings.TrimSuffix(strings.TrimSuffix(sb.String(), "\r\nEND\r\n"), "\r\n"), "SUCCESS: ")

	return data, nil
}

func (ov *ovpn) getClient() []ClientData {
	clients := make([]ClientData, 0)

	data, err := ov.sendCommand("status 3")
	if err != nil {
		return clients
	}

	for _, v := range strings.Split(data, "\r\n") {
		cdSlice := strings.Split(v, "\t")

		if cdSlice[0] == "CLIENT_LIST" {
			if len(cdSlice) < 11 {
				continue
			}
			recv, _ := strconv.ParseFloat(cdSlice[5], 64)
			send, _ := strconv.ParseFloat(cdSlice[6], 64)
			connDate, _ := time.ParseInLocation("2006-01-02 15:04:05", cdSlice[7], time.Local)

			rip := cdSlice[2]
			if strings.Count(cdSlice[2], ":") == 1 {
				rip = cdSlice[2][:strings.IndexByte(cdSlice[2], ':')]
			}

			cd := ClientData{
				Rip:            rip,
				Vip:            cdSlice[3],
				Vip6:           cdSlice[4],
				RecvBytes:      recv,
				SendBytes:      send,
				ConnDate:       cdSlice[7],
				Username:       cdSlice[9],
				CommonName:     cdSlice[1],
				ID:             cdSlice[10],
				OnlineTime:     (time.Duration(time.Now().Unix()-connDate.Unix()) * time.Second).String(),
				IsNftBlacklist: getNftTableSetElement("blacklist", cdSlice[3]) || getNftTableSetElement("blacklist", cdSlice[4]),
			}

			clients = append(clients, cd)
		}
	}

	return clients

}

// validClientName 校验客户端名称，仅允许字母/数字/下划线/连字符，长度 1-64。
// 防止路径遍历写（如 ../../etc/x）与 easyrsa 命令注入。
var clientNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func validClientName(name string) bool {
	return clientNameRe.MatchString(name)
}

// isCertNotFound 判断 easyrsa revoke 失败是否因证书本就不存在（空壳客户端 / 之前已吊销过）
func isCertNotFound(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "not found") ||
		strings.Contains(lower, "no such") ||
		strings.Contains(lower, "no certificate") ||
		strings.Contains(lower, "has no cert") ||
		strings.Contains(lower, "unknown certificate") ||
		strings.Contains(lower, "nothing to revoke")
}

func (ov *ovpn) getServer() ServerData {
	var sd ServerData

	data, err := ov.sendCommand("state")
	if err != nil {
		return sd
	}

	sateSlice := strings.Split(data, ",")
	if len(sateSlice) >= 3 {
		runDate, _ := strconv.ParseInt(sateSlice[0], 10, 64)
		sd.RunDate = time.Unix(runDate, 0).Format("2006-01-02 15:04:05")
		sd.Status = sateSlice[1]
		sd.StatusDesc = sateSlice[2]
		sd.Address = sateSlice[3]
	}

	data, err = ov.sendCommand("load-stats")
	if err != nil {
		return sd
	}

	statsSlice := strings.Split(data, ",")
	for _, v := range statsSlice {
		statsKeySlice := strings.Split(v, "=")

		switch statsKeySlice[0] {
		case "nclients":
			sd.Nclients = statsKeySlice[1]
		case "bytesin":
			in, _ := strconv.ParseFloat(statsKeySlice[1], 64)
			sd.BytesIn = tools.FormatBytes(in)
		case "bytesout":
			out, _ := strconv.ParseFloat(statsKeySlice[1], 64)
			sd.BytesOut = tools.FormatBytes(out)
		}
	}

	data, err = ov.sendCommand("version")
	if err != nil {
		return sd
	}

	for _, v := range strings.Split(data, "\n") {
		if strings.HasPrefix(v, "OpenVPN Version: ") {
			raw := strings.TrimPrefix(v, "OpenVPN Version: ")
			// 仅取语义化版本号，去掉编译平台/特性后缀（如 x86_64-pc-linux-gnu [SSL ...]）
			if m := regexp.MustCompile(`\d+\.\d+\.\d+`).FindString(raw); m != "" {
				sd.Version = m
			} else {
				sd.Version = raw
			}
		}
	}

	sd.FpkVersion = version

	return sd

}

func (ov *ovpn) killClient(cid string) {
	ov.sendCommand(fmt.Sprintf("client-kill %s HALT", cid))
}

// revokedSerials 解析 crl.pem，返回被吊销证书的序列号集合（十进制字符串，与 cert.SerialNumber.String() 对齐）。
// 无 CRL 文件或解析失败时返回空集合（不报错，仅意味“无吊销记录”）。
func revokedSerials(pkiDir string) map[string]bool {
	set := make(map[string]bool)
	crlPath := filepath.Join(pkiDir, "crl.pem")
	data, err := os.ReadFile(crlPath)
	if err != nil {
		return set
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return set
	}
	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil {
		return set
	}
	for _, rc := range crl.RevokedCertificates {
		set[rc.SerialNumber.String()] = true
	}
	return set
}

func parseCrl(crlPath string) (*CertData, error) {
	crlData, err := os.ReadFile(crlPath)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(crlData)
	if block == nil {
		return nil, fmt.Errorf("无法解析证书文件")
	}

	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	expiresIn := crl.NextUpdate.Sub(now)

	var status string
	var expiresInStr string

	if now.After(crl.NextUpdate) {
		status = "已过期"
		expiresInStr = fmt.Sprintf("已过期 %d 天", int(now.Sub(crl.NextUpdate).Hours()/24))
	} else if expiresIn < 30*24*time.Hour {
		status = "即将过期"
		expiresInStr = fmt.Sprintf("%d 天后过期", int(expiresIn.Hours()/24))
	} else {
		status = "正常"
		expiresInStr = fmt.Sprintf("%d 天后过期", int(expiresIn.Hours()/24))
	}

	return &CertData{
		Name:      strings.TrimSuffix(filepath.Base(crlPath), filepath.Ext(crlPath)),
		Type:      "CRL证书",
		Kind:      "crl",
		Subject:   "",
		Issuer:    crl.Issuer.String(),
		NotBefore: crl.ThisUpdate.In(cst).Format("2006-01-02 15:04:05"),
		NotAfter:  crl.NextUpdate.In(cst).Format("2006-01-02 15:04:05"),
		ExpiresIn: expiresInStr,
		Status:    status,
		SerialNo:  "",
	}, nil
}

func parseCert(certPath string, revoked map[string]bool) (*CertData, error) {
	certData, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(certData)
	if block == nil {
		return nil, fmt.Errorf("无法解析证书文件")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	expiresIn := cert.NotAfter.Sub(now)

	var status string
	var expiresInStr string

	if now.After(cert.NotAfter) {
		status = "已过期"
		expiresInStr = fmt.Sprintf("已过期 %d 天", int(now.Sub(cert.NotAfter).Hours()/24))
	} else if expiresIn < 30*24*time.Hour {
		status = "即将过期"
		expiresInStr = fmt.Sprintf("%d 天后过期", int(expiresIn.Hours()/24))
	} else {
		status = "正常"
		expiresInStr = fmt.Sprintf("%d 天后过期", int(expiresIn.Hours()/24))
	}

	certType := "客户端证书"
	certKind := "client"
	if cert.IsCA {
		certType = "CA证书"
		certKind = "ca"
	} else if strings.Contains(cert.Subject.CommonName, "server") {
		certType = "服务器证书"
		certKind = "server"
	}

	serial := cert.SerialNumber.String()
	isRevoked := revoked[serial]
	// 已吊销优先级最高：覆盖过期/正常等状态，让前端和列表一眼可见
	if isRevoked {
		status = "已吊销"
	}

	return &CertData{
		Name:      strings.TrimSuffix(filepath.Base(certPath), filepath.Ext(certPath)),
		Type:      certType,
		Kind:      certKind,
		Revoked:   isRevoked,
		Subject:   cert.Subject.String(),
		Issuer:    cert.Issuer.String(),
		NotBefore: cert.NotBefore.In(cst).Format("2006-01-02 15:04:05"),
		NotAfter:  cert.NotAfter.In(cst).Format("2006-01-02 15:04:05"),
		ExpiresIn: expiresInStr,
		Status:    status,
		SerialNo:  serial,
	}, nil
}

func getCerts(ovData string) []CertData {
	cers := make([]CertData, 0)
	pkiDir := filepath.Join(ovData, "pki")

	// 一次性构建吊销序列号集合，所有证书共用，避免重复解析 CRL
	revoked := revokedSerials(pkiDir)

	caPath := filepath.Join(pkiDir, "ca.crt")
	if cert, err := parseCert(caPath, revoked); err == nil {
		cers = append(cers, *cert)
	} else {
		logger.Error(context.Background(), err.Error())
	}

	crlPath := filepath.Join(pkiDir, "crl.pem")
	if cert, err := parseCrl(crlPath); err == nil {
		cers = append(cers, *cert)
	} else {
		logger.Error(context.Background(), err.Error())
	}

	issuedDir := filepath.Join(pkiDir, "issued")
	if files, err := os.ReadDir(issuedDir); err == nil {
		for _, file := range files {
			if strings.HasSuffix(file.Name(), ".crt") {
				certPath := filepath.Join(issuedDir, file.Name())
				if cert, err := parseCert(certPath, revoked); err == nil {
					cers = append(cers, *cert)
				} else {
					logger.Error(context.Background(), err.Error())
				}
			}
		}
	} else {
		logger.Error(context.Background(), err.Error())
	}

	return cers
}

// isValidPassword 密码策略（v1.0.40 按用户要求放宽）：管理员/用户密码统一至少 8 位即可。
func isValidPassword(pw string) bool {
	return len(pw) >= 8
}

func genRandomString(length int) string {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	result := make([]byte, length)
	// 敏感密钥（secretKey / token / server_cn 等）均由此生成，必须用密码学安全 RNG
	if _, err := rand.Read(result); err != nil {
		// 极端降级：RNG 不可用时用首字符填充，保证不崩溃（安全性降级但避免 panic）
		for i := range result {
			result[i] = charset[0]
		}
		return string(result)
	}
	for i, b := range result {
		result[i] = charset[int(b)%len(charset)]
	}
	return string(result)
}

func IsLocalRequest(c *gin.Context) bool {
	ip, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		return false
	}

	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return false
	}

	return parsedIP.IsLoopback()
}

func AuthMiddleWare() gin.HandlerFunc {
	return func(c *gin.Context) {
		session := sessions.Default(c)
		user := session.Get("user")

		if c.GetHeader("O-Token") == viper.GetString("system.base.token") {
			if c.Request.URL.Path == "/ovpn/login" || c.Request.URL.Path == "/ovpn/history" || c.Request.URL.Path == "/ovpn/firewall" {
				if IsLocalRequest(c) {
					c.Next()
					return
				}
			}
		}

		if user == nil {
			// 放行首页与登录页给未登录访问：fnOS 的“打开”检测会探测入口 URL（/），
			// 若返回 302 会被判定为未就绪（isOpen=false）。首页对未登录返回 200，
			// 由前端引导登录；其余受保护路由仍强制跳转 /login。
			if c.Request.URL.Path == "/" || c.Request.URL.Path == "/login" {
				c.Header("X-OVPN-Public", "1")
				c.Next()
				return
			}
			c.Redirect(302, "/login")
			c.Abort()
			return
		}

		if user, ok := user.(string); ok {
			if c.Request.URL.Path != "/" && !strings.HasPrefix(c.Request.URL.Path, "/client") && user != adminUsername {
				c.Redirect(302, "/")
				c.Abort()
				return
			}
		}

		c.Next()
	}
}

func init() {
	initConfig()
	loadConfig()
}

// gwResponseWriter 在 3xx 重定向响应中，把 Location 的 "/" 前缀重写为 GATEWAY_PREFIX，
// 使浏览器在 fnOS 统一网关 iframe 内正确跳转（HTTP 重定向场景；JSON 中的 redirect 字段由 redirPath 处理）。
type gwResponseWriter struct {
	http.ResponseWriter
	prefix string
}

func (w *gwResponseWriter) WriteHeader(code int) {
	if w.prefix != "" && code >= 300 && code < 400 {
		if loc := w.Header().Get("Location"); loc != "" && strings.HasPrefix(loc, "/") {
			w.Header().Set("Location", w.prefix+loc)
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func main() {
	ov := ovpn{
		address: ovManage,
	}

	// 统一网关支持：网关转发时【保留前缀】，后端需剥离前缀做路由，并对重定向 Location 加回前缀。
	// SOCKET_PATH 非空时额外监听 unix socket（由 fnOS 统一网关转发）。
	gwPrefix := os.Getenv("GATEWAY_PREFIX")
	// redirPath 为 JSON 响应中的 redirect 字段加回网关前缀（HTTP 重定向由 gwResponseWriter 处理）
	redirPath := func(p string) string {
		if gwPrefix != "" && strings.HasPrefix(p, "/") {
			return gwPrefix + p
		}
		return p
	}

	var err error
	db, err = gorm.Open(sqlite.Open(filepath.Join(ovData, "ovpn.db")+"?_pragma=foreign_keys(1)"), &gorm.Config{
		Logger: logger,
	})

	if err != nil {
		panic(err)
	}

	c := cron.New()
	c.AddFunc("@daily", func() {
		err := History{}.Clear()
		if err != nil {
			logger.Error(context.Background(), err.Error())
		}
	})
	c.Start()

	store := gormsessions.NewStore(db, true, []byte(secretKey))

	db.AutoMigrate(&Group{})
	db.FirstOrCreate(&Group{Name: "Default", ParentID: nil})
	db.AutoMigrate(&User{}, &History{}, &Firewall{})

	// one-shot user fix: if ovData/.fixuser exists, enable (and optionally set the password of)
	// the listed users, then remove the flag. Format: one "username[:password]" per line.
	// The password written here uses the app's own AES(secretKey), identical to what
	// openvpn-auth decrypts on the client-auth path — so it round-trips correctly.
	if fb, ferr := os.ReadFile(filepath.Join(ovData, ".fixuser")); ferr == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(fb)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, ":", 2)
			uname := strings.TrimSpace(parts[0])
			var fu User
			if derr := db.First(&fu, "username = ?", uname).Error; derr != nil {
				logger.Error(context.Background(), "fixuser: user not found: "+uname)
				continue
			}
			fu.IsEnable = true
			if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
				fu.Password = strings.TrimSpace(parts[1]) // plain; BeforeSave encrypts with secretKey
			}
			if serr := db.Save(&fu).Error; serr != nil {
				logger.Error(context.Background(), "fixuser: save failed: "+serr.Error())
			} else {
				logger.Info(context.Background(), "fixuser: enabled user "+uname)
			}
		}
		os.Remove(filepath.Join(ovData, ".fixuser"))
	}

	r := gin.New()
	r.Use(gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {

		var statusColor, methodColor, resetColor string
		if param.IsOutputColor() {
			statusColor = param.StatusCodeColor()
			methodColor = param.MethodColor()
			resetColor = param.ResetColor()
		}

		if param.Latency > time.Minute {
			param.Latency = param.Latency.Truncate(time.Second)
		}
		return fmt.Sprintf("[OPENVPN-WEB] %v GIN |%s %3d %s| %13v | %15s |%s %-7s %s %#v\n%s",
			param.TimeStamp.Format("2006-01-02 15:04:05.000"),
			statusColor, param.StatusCode, resetColor,
			param.Latency,
			param.ClientIP,
			methodColor, param.Method, resetColor,
			param.Path,
			param.ErrorMessage,
		)
	}))

	r.Use(sessions.Sessions("user_session", store))

	r.Use(gin.Recovery())

	templ := template.Must(template.New("").ParseFS(FS, "templates/*.html"))
	r.SetHTMLTemplate(templ)
	f, _ := fs.Sub(FS, "templates/static")
	r.StaticFS("/static", http.FS(f))

	r.GET("/login", func(c *gin.Context) {
		c.HTML(http.StatusOK, "login.html", gin.H{})
	})

	r.POST("/login", func(c *gin.Context) {
		var err error

		cip := c.ClientIP()
		key := c.PostForm("c_key")
		dots := c.PostForm("c_dots")
		passcode := c.PostForm("passcode")

		n := getLoginFail(cip)
		if passcode == "" && n >= loginCaptchaThreshold {
			if key == "" && dots == "" {
				c.JSON(401, gin.H{"message": "需要进行人机验证", "needCaptcha": true})
				return
			}

			if !checkCaptcha(key, dots) {
				c.JSON(http.StatusInternalServerError, gin.H{"message": "验证码错误"})
				return
			}
		}

		session := sessions.Default(c)
		remember7d := c.PostForm("remember7d")

		if remember7d == "on" {
			session.Options(sessions.Options{
				HttpOnly: true,
				MaxAge:   3600 * 24 * 7,
			})
		} else {
			session.Options(sessions.Options{
				HttpOnly: true,
				MaxAge:   3600 * 1,
			})
		}

		var u User
		c.ShouldBind(&u)

		if u.Username == adminUsername {
			if dp, err := aes.AesDecrypt(adminPassword, secretKey); err == nil {
				if subtle.ConstantTimeCompare([]byte(dp), []byte(u.Password)) == 1 {
					passwd, _ := bcrypt.GenerateFromPassword([]byte("admin"), 12)
					viper.Set("system.base.admin_password", string(passwd))
					viper.WriteConfig()

					c.JSON(401, gin.H{"message": "检测到旧的密码加密格式已重置为默认密码，请使用默认密码admin登录后进行修改"})
					return
				}
			}

			if bcrypt.CompareHashAndPassword([]byte(adminPassword), []byte(u.Password)) == nil {
				session.Set("user", u.Username)
				session.Save()

				resetLoginFail(cip)
				c.JSON(200, gin.H{"message": "登录成功", "redirect": redirPath("/admin")})
				return
			} else {
				err = fmt.Errorf("密码错误")
			}
		} else {
			if passcode != "" {
				if validUser, ok := cc.Get("valid_user"); ok {
					if u.Username == validUser.(string) {
						if ValidateMfa(passcode, u.Info().MfaSecret) {
							cc.Delete("valid_user")
							session.Set("user", u.Username)
							session.Save()
							resetLoginFail(cip)
							c.JSON(200, gin.H{"message": "登录成功", "redirect": redirPath("/")})
						} else {
							c.JSON(401, gin.H{"message": "MFA验证失败"})
						}

						return
					}
				}

				c.JSON(401, gin.H{"message": "登录超时", "redirect": redirPath("/login")})
				return
			}

			if err = u.Login(false); err == nil {
				user := u.Info()
				if user.MfaSecret != "" {
					cc.Set("valid_user", u.Username, 1*time.Minute)
					c.JSON(200, gin.H{"message": "需要MFA验证"})
					return
				}

				session.Set("user", u.Username)
				session.Save()

				resetLoginFail(cip)

				c.JSON(200, gin.H{"message": "登录成功", "redirect": redirPath("/"), "user": gin.H{"id": user.ID, "isFirstLogin": *user.IsFirstLogin}})
				return
			}
		}

		setLoginFail(cip)

		c.JSON(401, gin.H{"message": err.Error()})
	})

	r.GET("/logout", func(c *gin.Context) {
		session := sessions.Default(c)
		session.Clear()
		session.Options(sessions.Options{MaxAge: -1})
		session.Save()
		c.Redirect(302, "/login")
	})

	r.GET("/captcha", func(c *gin.Context) {
		key, mBase64, tBase64, err := getCaptcha()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"key": key, "master": mBase64, "thumb": tBase64})
	})

	r.Use(AuthMiddleWare())

	r.GET("/", func(c *gin.Context) {
		session := sessions.Default(c)
		if user, ok := session.Get("user").(string); ok {
			if user == adminUsername {
				c.Redirect(302, "/admin")
				return
			}

			u := User{Username: user}.Info()
			if u.IsFirstLogin == nil || *u.IsFirstLogin {
				c.Redirect(302, "/login")
				return
			}
		}

		c.HTML(http.StatusOK, "client.html", conf.Client)
	})

	r.GET("/admin", func(c *gin.Context) {
		session := sessions.Default(c)
		if user, ok := session.Get("user").(string); ok {
			if user != adminUsername {
				c.Redirect(302, "/")
				return
			}
		}

		c.HTML(http.StatusOK, "index.html", gin.H{
			"server":   ov.getServer(),
			"sysUser":  adminUsername,
			"ldapAuth": ldapAuth,
			"version":  "v" + version,
		})
	})

	r.GET("/settings", func(c *gin.Context) {
		// 仅返回前端需要的非敏感字段，绝不下发密码/密钥/绑定口令
		c.JSON(http.StatusOK, gin.H{
			"system": gin.H{
				"base": gin.H{
					"server_addr":    viper.GetString("system.base.server_addr"),
					"web_port":       viper.GetString("system.base.web_port"),
					"admin_username": viper.GetString("system.base.admin_username"),
					"init_done":      viper.GetBool("system.base.init_done"),
				},
			},
			"openvpn": gin.H{
				"ovpn_port":        viper.GetInt("openvpn.ovpn_port"),
				"ovpn_proto":       viper.GetString("openvpn.ovpn_proto"),
				"ovpn_subnet":      viper.GetString("openvpn.ovpn_subnet"),
				"ovpn_max_clients": viper.GetInt("openvpn.ovpn_max_clients"),
			},
		})
	})

	r.GET("/api/bootstrap", func(c *gin.Context) {
		// 兼容存量安装：init_done 历史上被 forbidden 挡住从未写入（恒 false），
		// 若 pki CA 已存在说明实际已完成初始化，视为已初始化，避免每次打开都自动弹向导。
		initDone := viper.GetBool("system.base.init_done")
		if !initDone {
			if st, err := os.Stat(filepath.Join(ovData, "pki", "ca.crt")); err == nil && !st.IsDir() {
				initDone = true
			}
		}
		c.JSON(http.StatusOK, gin.H{
			"init_done":     initDone,
			"server_addr":   viper.GetString("system.base.server_addr"),
			"admin_username": viper.GetString("system.base.admin_username"),
			"ovpn_port":     viper.GetString("openvpn.ovpn_port"),
			"ovpn_proto":    viper.GetString("openvpn.ovpn_proto"),
		})
	})

	r.POST("/settings", func(c *gin.Context) {
		c.Request.ParseForm()

		// 禁止通过 /settings 表单改写敏感/控制键，避免越权或系统失稳。
		// 注意：system.base.init_done 不能进 forbidden —— 初始化向导 finish() 依赖它，
		// 若被禁会导致 init_done 永远 false、每次打开都自动弹向导。
		forbidden := map[string]bool{
			"system.base.token":      true,
			"system.base.secret_key": true,
		}

		for k, vs := range c.Request.PostForm {
			if forbidden[k] {
				continue
			}
			val := vs[0]

			switch k {
			case "system.base.admin_password":
				if !isValidPassword(val) {
					c.JSON(http.StatusBadRequest, gin.H{"message": "管理员密码至少 8 位"})
					return
				}
				ep, _ := bcrypt.GenerateFromPassword([]byte(val), 12)
				val = string(ep)
			case "system.email.password":
				val, _ = aes.AesEncrypt(val, secretKey)
			case "system.base.max_duplicate_login":
				n, err := strconv.Atoi(val)
				if err != nil {
					n = 0
				}

				if n > 0 {
					cfg, err := initOvpnConfig()
					if err != nil {
						logger.Error(context.Background(), err.Error())
						return
					}

					statusLogPath := filepath.Join(ovData, "openvpn-status.log")
					if cfg.Get("status-version") != "3" || cfg.Get("status") != statusLogPath+" 1" {
						cfg.Set("status", statusLogPath+" 1")
						cfg.Set("status-version", "3")
						cfg.Save()

						ov.sendCommand("signal SIGHUP")
					}
				}
			case "openvpn.ovpn_subnet", "openvpn.ovpn_subnet6":
				_, _, err := net.ParseCIDR(val)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
					return
				}
			case "openvpn.ovpn_push_dns1", "openvpn.ovpn_push_dns2":
				if net.ParseIP(val) == nil {
					c.JSON(http.StatusInternalServerError, gin.H{"message": "invalid IP address: " + val})
					return
				}
			}

			switch val {
			case "true":
				viper.Set(k, true)
			case "false":
				viper.Set(k, false)
			default:
				viper.Set(k, val)
			}
		}
		if err := viper.WriteConfig(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "更新成功"})
	})

	r.POST("/email/send", func(c *gin.Context) {
		email := c.PostForm("email")
		subject := c.PostForm("subject")
		content := c.PostForm("content")

		err := sendEmail(email, subject, content)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
		} else {
			c.JSON(http.StatusOK, gin.H{"message": "发送成功"})
		}
	})

	ovpn := r.Group("/ovpn")
	{
		ovpn.StaticFS("/download", http.Dir(filepath.Join(ovData, "clients")))

		ovpn.POST("/server", func(c *gin.Context) {
			a := c.PostForm("action")

			switch a {
			case "settings":
				k := c.PostForm("key")
				v := c.PostForm("value")

				if k == "auth-user" {
					msg := "停用"
					if v == "true" {
						msg = "启用"
					}
					cmd := exec.Command(ovpnHelper, "auth", v)
					if out, err := cmd.CombinedOutput(); err != nil {
						if len(out) == 0 {
							out = []byte(err.Error())
						}
						logger.Error(context.Background(), string(out))
						c.JSON(http.StatusInternalServerError, gin.H{"message": fmt.Sprintf("%s用户认证失败", msg)})
					} else {
						ov.sendCommand("signal SIGHUP")
						c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("%s用户认证成功", msg)})
					}
				}
			case "renewCert":
				day := c.PostForm("day")

				cmd := exec.Command(ovpnHelper, "renewcert", day)
				if out, err := cmd.CombinedOutput(); err != nil {
					if len(out) == 0 {
						out = []byte(err.Error())
					}
					logger.Error(context.Background(), string(out))
					c.JSON(http.StatusInternalServerError, gin.H{"message": "更新证书失败"})
					return
				}

				ov.sendCommand("signal SIGHUP")
				c.JSON(http.StatusOK, gin.H{"message": "更新证书成功"})
			case "restartSrv":
				_, err := ov.sendCommand("signal SIGHUP")
				if err != nil {
					logger.Error(context.Background(), err.Error())
					c.JSON(http.StatusInternalServerError, gin.H{"message": "重启服务失败"})
					return
				}

				c.JSON(http.StatusOK, gin.H{"message": "重启服务成功"})
			case "getConfig":
				data, err := os.ReadFile(filepath.Join(ovData, "server.conf"))
				if err != nil {
					logger.Error(context.Background(), err.Error())
					c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
					return
				}

				c.JSON(http.StatusOK, gin.H{"content": string(data)})
			case "updateConfig":
				content := c.PostForm("content")

				file, err := os.OpenFile(filepath.Join(ovData, "server.conf"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
				if err != nil {
					logger.Error(context.Background(), err.Error())
					c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
					return
				}
				defer file.Close()

				_, err = file.WriteString(content)
				if err != nil {
					logger.Error(context.Background(), err.Error())
					c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
					return
				}

				c.JSON(http.StatusOK, gin.H{"message": "配置更新成功"})
			default:
				c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "未知操作"})
			}

		})

		ovpn.POST("/kill", func(c *gin.Context) {
			cid := c.PostForm("cid")
			ov.killClient(cid)
			c.JSON(http.StatusOK, gin.H{"code": http.StatusOK})
		})

		ovpn.GET("/firewall", FirewallHandler)
		ovpn.POST("/firewall", FirewallHandler)
		ovpn.PATCH("/firewall", FirewallHandler)
		ovpn.DELETE("/firewall/:id", FirewallHandler)

		ovpn.POST("/login", func(c *gin.Context) {
			var u User
			c.ShouldBind(&u)
			u.OvpnConfig = c.PostForm("common_name")

			err := u.Login(true)
			if err != nil {
				c.JSON(http.StatusUnauthorized, gin.H{"message": err.Error()})
			} else {
				c.JSON(http.StatusOK, gin.H{"message": "登录成功"})
			}
		})

		ovpn.GET("/online-client", func(c *gin.Context) {
			// 已配置客户端总数：按 pki 中客户端证书数（Kind=client）统计，作为仪表盘「在线/总数」分母。
			// 勿用 clients/ 下载目录条目数——该目录可能混入杂项/调试残留文件，会污染计数。
			total := 0
			for _, cert := range getCerts(ovData) {
				if cert.Kind == "client" {
					total++
				}
			}
			c.JSON(http.StatusOK, gin.H{"server": ov.getServer(), "clients": ov.getClient(), "total": total, "today": History{}.TodayCount()})
		})

		ovpn.GET("/group", func(c *gin.Context) {
			var g Group
			c.JSON(http.StatusOK, g.All())
		})

		ovpn.GET("/group/:id", func(c *gin.Context) {
			var g Group
			c.JSON(http.StatusOK, g.Get(c.Param("id")))
		})

		ovpn.GET("/group/:id/users", func(c *gin.Context) {
			var auth bool
			var g Group

			gid := c.Param("id")

			cmd := exec.Command("egrep", "^auth-user-pass-verify", filepath.Join(ovData, "server.conf"))
			if err := cmd.Run(); err != nil {
				auth = false
			} else {
				auth = true
			}

			users := g.GetUsers(gid)
		// 累计流量 = 历史会话汇总 + 当前在线会话实时值（下线后不归零）
		histUser := History{}.TrafficByUsername()
		for i := range users {
			if t, ok := histUser[users[i].Username]; ok {
				users[i].RecvBytes = t.Recv
				users[i].SendBytes = t.Sent
			}
		}

		// 关联在线流量：按用户名匹配当前连接的客户端，累计收发字节
		if online := ov.getClient(); len(online) > 0 {
			byUser := make(map[string]ClientData, len(online))
			for _, c := range online {
				if c.Username != "" {
					byUser[c.Username] = c
				}
			}
			for i := range users {
				if c, ok := byUser[users[i].Username]; ok {
					users[i].RecvBytes += c.RecvBytes
					users[i].SendBytes += c.SendBytes
					users[i].Online = true
				}
			}
		}
		c.JSON(http.StatusOK, gin.H{"users": users, "authUser": auth})
		})

		ovpn.POST("/group", func(c *gin.Context) {
			var g Group
			c.ShouldBind(&g)

			err := g.Create()
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
				return
			}

			c.JSON(http.StatusOK, gin.H{"message": "添加成功"})
		})

		ovpn.PATCH("/group", func(c *gin.Context) {
			var g Group
			c.ShouldBind(&g)

			if config, ok := c.Request.PostForm["config"]; ok {
				if config[0] == "" {
					db.Model(&g).Update("config", nil)
				}
			}

			err := g.Update()
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
				return
			}

			c.JSON(http.StatusOK, gin.H{"message": "更新成功"})
		})

		ovpn.DELETE("/group/:id", func(c *gin.Context) {
			var g Group
			c.ShouldBind(&g)

			err := g.Delete(c.Param("id"))
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
				return
			}

			c.JSON(http.StatusOK, gin.H{"message": "删除成功"})
		})

		ovpn.GET("/user", func(c *gin.Context) {
			var u User

			username := c.Query("username")
			if username != "" {
				u.Username = username
			}

			c.JSON(http.StatusOK, u.Info())
		})

		ovpn.GET("/user/:id", func(c *gin.Context) {
			var u User
			c.JSON(http.StatusOK, u.Get(c.Param("id")))
		})

		r.GET("/user/template", func(c *gin.Context) {
			c.Header("Content-Type", "text/csv")
			c.Header("Content-Disposition", "attachment; filename=user_template.csv")

			c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})

			writer := csv.NewWriter(c.Writer)
			defer writer.Flush()

			writer.Write([]string{"username", "password", "name", "email", "is_enable", "expire_date", "ip_addr", "ovpn_config"})
			writer.Write([]string{"zhangsan", "123456", "张三", "zhangsan@example.com", "1", "2025-12-01/00:00:00", "10.8.0.222", "tt-gz.ovpn"})
			writer.Write([]string{"lisi", "123456", "李四", "lisi@example.com", "0", "", "", "tt-sh.ovpn"})
		})

		ovpn.GET("/user/export", func(c *gin.Context) {
			gid := c.Query("gid")

			fileName := fmt.Sprintf("user_%s.csv", time.Now().Format("20060102150405"))

			c.Header("Content-Type", "text/csv; charset=utf-8")
			c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", fileName))
			c.Header("Cache-Control", "no-cache")

			c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})

			writer := csv.NewWriter(c.Writer)
			header := []string{"ID", "用户名", "密码", "姓名", "节点", "启用", "过期时间", "IP地址", "配置文件", "MFA", "创建时间"}
			if err := writer.Write(header); err != nil {
				logger.Error(context.Background(), err.Error())
				return
			}
			writer.Flush()

			gQuery := db.Model(&Group{}).
				Select("id").
				Where(`
				parent_id = ?
				OR EXISTS (
					SELECT 1 FROM `+"`group`"+`
					WHERE id = ? AND parent_id IS NULL
				)
				`, gid, gid)

			rows, err := db.Model(&User{}).Where("gid = ? OR gid IN (?)", gid, gQuery).Rows()
			if err != nil {
				return
			}
			defer rows.Close()

			for rows.Next() {
				var u User
				var g Group

				db.ScanRows(rows, &u)

			enable := "0"
			if u.IsEnable {
				enable = "1"
			}

			record := []string{
				strconv.Itoa(int(u.ID)),
				u.Username,
				"******", // 密码脱敏，不导出明文
				u.Name,
				g.Get(strconv.Itoa(int(u.Gid))).Name,
				enable,
				u.ExpireDate,
				u.IpAddr,
				u.OvpnConfig,
				"", // MFA 密钥不导出
				u.CreatedAt.Format("2006-01-02 15:04:05"),
			}

				if err := writer.Write(record); err != nil {
					logger.Error(context.Background(), err.Error())
					return
				}
			}
			writer.Flush()

			if err := writer.Error(); err != nil {
				logger.Error(context.Background(), err.Error())
			}
		})

		ovpn.POST("/user", func(c *gin.Context) {
			var u User
			c.ShouldBind(&u)

			file, err := c.FormFile("file")
			if err != nil {
				if strings.Contains(err.Error(), "no such file") {
					c.JSON(http.StatusInternalServerError, gin.H{"message": "没有上传文件"})
					return
				}
			} else {
				gid := c.PostForm("gid")
				f, _ := file.Open()

				defer f.Close()

				reader := csv.NewReader(f)

				header, err := reader.Read()
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
					return
				}

				if len(header) != 8 {
					c.JSON(http.StatusInternalServerError, gin.H{"message": "导入文件格式错误"})
					return
				}

				for {
					record, err := reader.Read()
					if err == io.EOF {
						break
					}

					if err != nil {
						c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
						return
					}

					enable := record[4] == "1"
					gid64, err := strconv.ParseUint(gid, 10, 64)
					u := User{
						Username:   record[0],
						Password:   record[1],
						Name:       record[2],
						Email:      record[3],
						IsEnable:   enable,
						ExpireDate: strings.Replace(record[5], "/", " ", 1),
						IpAddr:     record[6],
						OvpnConfig: record[7],
						Gid:        uint(gid64),
					}

					err = u.Create()
					if err != nil {
						c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
						return
					}
				}

				c.JSON(http.StatusOK, gin.H{"message": "导入用户成功"})
				return
			}

			if isFirstLogin, ok := c.Request.PostForm["isFirstLogin"]; ok {
				val := isFirstLogin[0] == "true"
				u.IsFirstLogin = &val
			}

			err = u.Create()
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
			} else {
				sendNotifyEmail := c.PostForm("sendNotifyEmail")
				if sendNotifyEmail == "true" {
					go func() {
						var tpl *template.Template
						var buf bytes.Buffer

						tpl, err = template.ParseFS(FS, "templates/email.html")
						if err == nil {
							err = tpl.Execute(&buf, struct {
								Type     string
								Name     string
								Username string
								Password string
								SiteUrl  string
							}{
								Type:     "addUser",
								Name:     u.Name,
								Username: u.Username,
								Password: c.PostForm("password"),
								SiteUrl:  viper.GetString("system.base.site_url"),
							})
						}

						if err != nil {
							logger.Error(context.Background(), err.Error())
							return
						}

						sendEmail(u.Email, "用户开通通知", buf.String())
					}()
				}

				c.JSON(http.StatusOK, gin.H{"message": "添加用户成功"})
			}
		})

		ovpn.PATCH("/user", func(c *gin.Context) {
			// 手动解析请求体，兼容 axios 默认的 JSON 提交与表单提交两种方式。
			// 旧实现用 c.ShouldBind(&u) 在 PATCH+JSON 场景下绑定失败，导致 u.ID=0、
			// Update() 命中 0 行 —— 密码重置与启用/禁用全部静默失效。
			fields := map[string]string{}

			ct := c.ContentType()
			if strings.HasPrefix(ct, "application/json") {
				if b, _ := io.ReadAll(c.Request.Body); len(b) > 0 {
					var j map[string]interface{}
					if json.Unmarshal(b, &j) == nil {
						for k, v := range j {
							switch val := v.(type) {
							case string:
								fields[k] = val
							case bool:
								fields[k] = strconv.FormatBool(val)
							case float64:
								fields[k] = strconv.FormatFloat(val, 'f', -1, 64)
							default:
								fields[k] = fmt.Sprintf("%v", val)
							}
						}
					}
				}
			} else {
				c.Request.ParseForm()
				for k := range c.Request.PostForm {
					fields[k] = c.Request.PostFormValue(k)
				}
				if c.Request.MultipartForm != nil {
					for k, v := range c.Request.MultipartForm.Value {
						if len(v) > 0 {
							fields[k] = v[0]
						}
					}
				}
			}

			idStr, ok := fields["id"]
			if !ok || idStr == "" {
				c.JSON(http.StatusBadRequest, gin.H{"message": "缺少用户ID"})
				return
			}
			uid, err := strconv.ParseUint(idStr, 10, 64)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"message": "非法用户ID"})
				return
			}

			var exist User
			if r := db.First(&exist, uid); r.Error != nil {
				c.JSON(http.StatusNotFound, gin.H{"message": "用户不存在"})
				return
			}

			updates := map[string]interface{}{}
			if v, ok := fields["password"]; ok && v != "" {
				ep, e := aes.AesEncrypt(v, secretKey)
				if e != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"message": "密码加密失败"})
					return
				}
				updates["password"] = ep
			}
			if v, ok := fields["isEnable"]; ok && v != "" {
				updates["is_enable"] = (v == "true" || v == "1")
			}
			if v, ok := fields["expireDate"]; ok {
				if v == "" {
					updates["expire_date"] = nil
				} else {
					updates["expire_date"] = v
				}
			}
			if v, ok := fields["ipAddr"]; ok {
				if v == "" {
					updates["ip_addr"] = nil
				} else {
					updates["ip_addr"] = v
				}
			}
			if v, ok := fields["name"]; ok {
				updates["name"] = v
			}
			if v, ok := fields["email"]; ok {
				updates["email"] = v
			}
			if v, ok := fields["ovpnConfig"]; ok {
				updates["ovpn_config"] = v
			}
			if v, ok := fields["gid"]; ok && v != "" {
				if gid, e := strconv.ParseUint(v, 10, 64); e == nil {
					updates["gid"] = gid
				}
			}

			if len(updates) == 0 {
				c.JSON(http.StatusOK, gin.H{"message": "用户更新成功"})
				return
			}

			if err := db.Model(&User{}).Where("id = ?", uid).Updates(updates).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
				return
			}

			// 仅当本次包含密码重置时才发送通知邮件
			if _, isReset := updates["password"]; isReset {
				if fields["sendNotifyEmail"] == "true" {
					go func() {
						var cu User
						db.First(&cu, uid)

						if cu.Email != "" {
							var tpl *template.Template
							var buf bytes.Buffer

							tpl, err = template.ParseFS(FS, "templates/email.html")
							if err == nil {
								err = tpl.Execute(&buf, struct {
									Type     string
									Name     string
									Username string
									Password string
									SiteUrl  string
								}{
									Type:     "resetPass",
									Name:     cu.Name,
									Username: cu.Username,
									Password: fields["password"],
									SiteUrl:  viper.GetString("system.base.site_url"),
								})
							}

							if err != nil {
								logger.Error(context.Background(), err.Error())
								return
							}

							sendEmail(cu.Email, "用户密码重置通知", buf.String())
						} else {
							logger.Error(context.Background(), "发送邮件通知失败，用户没有配置邮箱地址")
						}
					}()
				}
			}

			c.JSON(http.StatusOK, gin.H{"message": "用户更新成功"})
		})

		ovpn.DELETE("/user/:id", func(c *gin.Context) {
			var u User
			id := c.Param("id")

			err := u.Delete(id)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
			} else {
				c.JSON(http.StatusOK, gin.H{"message": "删除用户成功"})
			}
		})

		ovpn.GET("/client", func(c *gin.Context) {
			c.Header("Cache-Control", "no-store")
			clients := make([]ClientConfigData, 0)

			files, _ := os.ReadDir(filepath.Join(ovData, "clients"))
			for _, file := range files {
				// 只把 .ovpn 文件当作客户端配置；clients 目录里可能残留
				// server.conf 副本(.conf)、config.json 副本(.json)、日志(.log)等，
				// 这些不是客户端，误列会导致“删除后还在”的假象。
				if file.IsDir() || filepath.Ext(file.Name()) != ".ovpn" {
					continue
				}
				finfo, _ := file.Info()

				f := ClientConfigData{
					Name:     strings.TrimSuffix(file.Name(), filepath.Ext(file.Name())),
					FullName: file.Name(),
					File:     fmt.Sprintf("/ovpn/download/%s", file.Name()),
					Date:     finfo.ModTime().Local().Format("2006-01-02 15:04:05"),
				}
				clients = append(clients, f)
			}

		sort.Slice(clients, func(i, j int) bool {
			return clients[i].Date < clients[j].Date
		})

		// 累计流量 = 历史会话汇总(history 表) + 当前在线会话实时值。
		// 这样客户端下线后统计不会归零，仍展示历史累计。
		hist := History{}.TrafficByCommonName()
		for i := range clients {
			if t, ok := hist[clients[i].Name]; ok {
				clients[i].RecvBytes = t.Recv
				clients[i].SendBytes = t.Sent
				clients[i].Vip = t.Vip
				if t.LastSeen > 0 {
					clients[i].LastSeen = time.Unix(t.LastSeen, 0).Format("2006-01-02 15:04:05")
				}
			}
		}

		// 关联在线流量：客户端文件名(去扩展) == 证书 CN == getClient().CommonName
		if online := ov.getClient(); len(online) > 0 {
			byCN := make(map[string]ClientData, len(online))
			for _, c := range online {
				byCN[c.CommonName] = c
			}
			for i := range clients {
				if c, ok := byCN[clients[i].Name]; ok {
					clients[i].RecvBytes += c.RecvBytes
					clients[i].SendBytes += c.SendBytes
					clients[i].Vip = c.Vip
					clients[i].LastSeen = c.ConnDate
					clients[i].Online = true
				}
			}
		}

		c.JSON(http.StatusOK, clients)

	})

		ovpn.GET("/client/:name/ccd", func(c *gin.Context) {
			name := c.Param("name")
			if !validClientName(name) {
				c.JSON(http.StatusBadRequest, gin.H{"message": "非法客户端名称"})
				return
			}
			ccdDir := filepath.Join(ovData, "ccd")

			os.MkdirAll(ccdDir, 0755)

			ccdRoot, err := os.OpenRoot(ccdDir)
			if err != nil {
				logger.Error(context.Background(), err.Error())
				c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
				return
			}
			defer ccdRoot.Close()

			data, err := ccdRoot.ReadFile(name)
			if err != nil {
				if os.IsNotExist(err) {
					c.JSON(http.StatusOK, gin.H{"content": ""})
				} else {
					c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
				}
				return
			}

			c.JSON(http.StatusOK, gin.H{"content": string(data)})
		})

		ovpn.GET("/client/:name/config", func(c *gin.Context) {
			name := c.Param("name")
			if !validClientName(name) {
				c.JSON(http.StatusBadRequest, gin.H{"message": "非法客户端名称"})
				return
			}
			clientsDir := filepath.Join(ovData, "clients")

			clientsRoot, err := os.OpenRoot(clientsDir)
			if err != nil {
				logger.Error(context.Background(), err.Error())
				c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
				return
			}
			defer clientsRoot.Close()

			data, err := clientsRoot.ReadFile(name + ".ovpn")
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
				return
			}

			c.JSON(http.StatusOK, gin.H{"content": string(data)})
		})

		ovpn.PUT("/client/:name/ccd", func(c *gin.Context) {
			name := c.Param("name")
			if !validClientName(name) {
				c.JSON(http.StatusBadRequest, gin.H{"message": "非法客户端名称"})
				return
			}
			content := c.PostForm("content")
			msg := "客户端更新成功"
			ccdDir := filepath.Join(ovData, "ccd")

			os.MkdirAll(ccdDir, 0755)

			cfg, err := initOvpnConfig()
			if err != nil {
				logger.Error(context.Background(), err.Error())
				c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
				return
			}

			if cfg.Get("client-config-dir") == "" {
				cfg.Set("client-config-dir", ccdDir)
				cfg.Save()

				msg += "（未启用CCD需要重启服务生效）"
			}

			ccdRoot, err := os.OpenRoot(ccdDir)
			if err != nil {
				logger.Error(context.Background(), err.Error())
				c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
				return
			}
			defer ccdRoot.Close()

			err = ccdRoot.WriteFile(name, []byte(content), 0644)
			if err != nil {
				logger.Error(context.Background(), err.Error())
				c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
				return
			}

			c.JSON(http.StatusOK, gin.H{"message": msg})
		})

		ovpn.PUT("/client/:name/config", func(c *gin.Context) {
			name := c.Param("name")
			if !validClientName(name) {
				c.JSON(http.StatusBadRequest, gin.H{"message": "非法客户端名称"})
				return
			}
			content := c.PostForm("content")
			clientsDir := filepath.Join(ovData, "clients")

			clientsRoot, err := os.OpenRoot(clientsDir)
			if err != nil {
				logger.Error(context.Background(), err.Error())
				c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
				return
			}
			defer clientsRoot.Close()

			err = clientsRoot.WriteFile(name+".ovpn", []byte(content), 0644)
			if err != nil {
				logger.Error(context.Background(), err.Error())
				c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
			} else {
				c.JSON(http.StatusOK, gin.H{"message": "客户端配置更新成功"})
			}
		})

		ovpn.POST("/client", func(c *gin.Context) {
			name := c.PostForm("name")
			if !validClientName(name) {
				c.JSON(http.StatusBadRequest, gin.H{"message": "非法客户端名称"})
				return
			}
			serverAddr := c.PostForm("serverAddr")
			serverPort := c.PostForm("serverPort")
			config := c.PostForm("config")
			ccdConfig := c.PostForm("ccdConfig")
			mfa := c.PostForm("mfa")

			clientsDir := filepath.Join(ovData, "clients")

			os.MkdirAll(clientsDir, 0755)

			clientsRoot, err := os.OpenRoot(clientsDir)
			if err != nil {
				logger.Error(context.Background(), err.Error())
				c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
				return
			}
			defer clientsRoot.Close()

			_, err = clientsRoot.Stat(name + ".ovpn")
			if err != nil {
				if os.IsNotExist(err) {
					cmd := exec.Command(ovpnHelper, "genclient", name, serverAddr, serverPort, config, ccdConfig, mfa)
					if out, err := cmd.CombinedOutput(); err != nil {
						if len(out) == 0 {
							out = []byte(err.Error())
						}
						logger.Error(context.Background(), string(out))
						c.JSON(http.StatusInternalServerError, gin.H{"message": "客户端添加失败"})
						return
					}

					c.JSON(http.StatusOK, gin.H{"message": "客户端添加成功"})
					return
				}

				logger.Error(context.Background(), err.Error())
				c.JSON(http.StatusInternalServerError, gin.H{"message": "非法客户端名称"})
				return
			}

			c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "客户端已存在"})
		})

		ovpn.DELETE("/client/:name", func(c *gin.Context) {
			name := c.Param("name")
			if !validClientName(name) {
				c.JSON(http.StatusBadRequest, gin.H{"message": "非法客户端名称"})
				return
			}

		cmd := exec.Command("easyrsa", "--batch", "revoke", name)
		out, err := cmd.CombinedOutput()
		if err != nil {
			// 证书不存在/已吊销/空壳客户端等情况：忽略 revoke 错误，继续清理关联文件（不再返回 500）
			fmt.Printf("[DELETE-CLIENT] revoke %s 失败（已忽略，继续清理）: %s\n", name, string(out))
		} else {
				cmd = exec.Command("easyrsa", "gen-crl")
				if out, err = cmd.CombinedOutput(); err != nil {
					logger.Error(context.Background(), string(out))
					c.JSON(http.StatusInternalServerError, gin.H{"message": "更新CRL证书失败"})
					return
				}
			}

		// 直接以绝对路径删除关联文件，检查 error 并明确返回，避免“已吊销但列表仍在”的假成功
		ovpnFile := filepath.Join(ovData, "clients", fmt.Sprintf("%s.ovpn", name))
		if err := os.Remove(ovpnFile); err != nil && !os.IsNotExist(err) {
			logger.Error(context.Background(), "删除客户端配置文件失败: "+err.Error())
			c.JSON(http.StatusInternalServerError, gin.H{"message": "删除客户端配置文件失败: " + err.Error()})
			return
		}
		ccdFile := filepath.Join(ovData, "ccd", name)
		if err := os.Remove(ccdFile); err != nil && !os.IsNotExist(err) {
			logger.Error(context.Background(), "删除客户端 CCD 配置失败: "+err.Error())
		}

		c.JSON(http.StatusOK, gin.H{"message": "删除客户端成功"})
		})

		ovpn.GET("/history", func(c *gin.Context) {
			var h History
			var p Params

			c.ShouldBindQuery(&p)

			c.JSON(http.StatusOK, h.Query(p))
		})

		ovpn.POST("/history", func(c *gin.Context) {
			var h History
			c.ShouldBind(&h)

			var err error
			if h.Action == "connect" {
				err = h.RecordConnect()
			} else {
				err = h.RecordDisconnect()
			}

			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
			} else {
				c.JSON(http.StatusOK, gin.H{"message": "添加记录成功"})
			}
		})

		ovpn.GET("/history/export", func(c *gin.Context) {
			var p Params
			c.ShouldBindQuery(&p)

			fileName := fmt.Sprintf("history_%s.csv", time.Now().Format("20060102150405"))

			c.Header("Content-Type", "text/csv; charset=utf-8")
			c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", fileName))
			c.Header("Cache-Control", "no-cache")

			c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})

			writer := csv.NewWriter(c.Writer)
			header := []string{"ID", "用户名", "客户端", "VPN IP", "用户 IP", "下载流量", "上传流量", "上线时间", "在线时长", "创建时间"}
			if err := writer.Write(header); err != nil {
				logger.Error(context.Background(), err.Error())
				return
			}
			writer.Flush()

			query := db.Model(&History{})
			if p.Qt != "" {
				qt := strings.Split(p.Qt, ",")
				if len(qt) == 2 {
					query = query.Where("time_unix BETWEEN ? AND ?", qt[0], qt[1])
				}
			}

			rows, err := query.Rows()
			if err != nil {
				return
			}
			defer rows.Close()

			for rows.Next() {
				var h History

				db.ScanRows(rows, &h)
				record := []string{
					strconv.Itoa(int(h.ID)),
					h.Username,
					h.CommonName,
					h.Vip,
					h.Rip,
					tools.FormatBytes(h.BytesReceived),
					tools.FormatBytes(h.BytesSent),
				time.Unix(h.TimeUnix, 0).In(cst).Format("2006-01-02 15:04:05"),
				(time.Duration(h.TimeDuration) * time.Second).String(),
				h.CreatedAt.In(cst).Format("2006-01-02 15:04:05"),
				}

				if err := writer.Write(record); err != nil {
					logger.Error(context.Background(), err.Error())
					return
				}
			}
			writer.Flush()

			if err := writer.Error(); err != nil {
				logger.Error(context.Background(), err.Error())
			}
		})

		ovpn.GET("/certs", func(c *gin.Context) {
			c.JSON(http.StatusOK, getCerts(ovData))
		})

		ovpn.GET("/ca", func(c *gin.Context) {
			caPath := filepath.Join(ovData, "pki", "ca.crt")
			if _, err := os.Stat(caPath); err != nil {
				c.JSON(http.StatusNotFound, gin.H{"message": "CA 证书不存在"})
				return
			}
			c.FileAttachment(caPath, "ca.crt")
		})
	}

	client := r.Group("/client")
	{
		client.GET("/userinfo", func(c *gin.Context) {
			var u User

			session := sessions.Default(c)
			if user, ok := session.Get("user").(string); ok {
				u.Username = user
			}

			if ldapAuth {
				l, err := InitLdap()
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
					return
				}

				lu, err := l.Get(u.Username)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
					return
				}

				c.JSON(http.StatusOK, lu)
				return
			}

			c.JSON(http.StatusOK, u.Info())
		})

		client.POST("/modifyPass", func(c *gin.Context) {
			var u User
			c.ShouldBind(&u)

			session := sessions.Default(c)
			if user, ok := session.Get("user").(string); ok {
				cu := User{Username: user}.Info()
				if u.ID != cu.ID {
					c.JSON(http.StatusInternalServerError, gin.H{"message": "非法请求"})
					return
				}
			}

			if !isValidPassword(u.Password) {
				c.JSON(http.StatusInternalServerError, gin.H{"message": "密码至少 8 位"})
				return
			}

			if currentPass, ok := c.Request.PostForm["currentPass"]; ok {
				if u.Info().Password != currentPass[0] {
					c.JSON(http.StatusUnauthorized, gin.H{"message": "当前密码错误"})
					return
				}
			}

			err := db.Transaction(func(tx *gorm.DB) error {
				data := User{
					Password: u.Password,
				}

				if isFirstLogin, ok := c.Request.PostForm["isFirstLogin"]; ok {
					val := isFirstLogin[0] == "true"
					data.IsFirstLogin = &val
				}

				if err := tx.Model(&u).Updates(data).Error; err != nil {
					return err
				}

				return nil
			})

			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
			} else {
				c.JSON(http.StatusOK, gin.H{"message": "密码修改成功"})
			}
		})

		client.GET("/userConfig", func(c *gin.Context) {
			var u User
			session := sessions.Default(c)
			if user, ok := session.Get("user").(string); ok {
				u.Username = user
			}

			u = u.Info()
			configName := u.OvpnConfig

			if ldapAuth {
				l, err := InitLdap()
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
					return
				}

				lu, err := l.Get(u.Username)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
					return
				}

				configName = lu.OvpnConfig
			}

			if configName == "" {
				c.JSON(http.StatusInternalServerError, gin.H{"message": "该账号未指定配置文件，请联系管理员"})
				return
			}

			clientsDir := filepath.Join(ovData, "clients")

			clientsRoot, err := os.OpenRoot(clientsDir)
			if err != nil {
				logger.Error(context.Background(), err.Error())
				c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
				return
			}
			defer clientsRoot.Close()

			data, err := clientsRoot.ReadFile(configName)
			if err != nil {
				logger.Error(context.Background(), err.Error())
				c.JSON(http.StatusInternalServerError, gin.H{"message": "读取配置文件失败"})
				return
			}

			challengeLine := `static-challenge "Enter MFA code" 1`
			content := string(data)

			if u.MfaSecret != "" {
				if !strings.Contains(content, challengeLine) {
					if !strings.HasSuffix(content, "\n") {
						content += "\n"
					}
					content += challengeLine + "\n"
				}
			} else {
				content = strings.ReplaceAll(content, challengeLine+"\n", "")
			}

			cfg, err := initOvpnConfig()
			if err != nil {
				logger.Error(context.Background(), err.Error())
				c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
				return
			}

			if cfg.Get("auth-user-pass-verify") != "" {
				if strings.Contains(content, "#auth-user-pass") {
					content = strings.ReplaceAll(content, "#auth-user-pass", "auth-user-pass")
				}
			} else {
				if !strings.Contains(content, "#auth-user-pass") {
					content = strings.ReplaceAll(content, "auth-user-pass", "#auth-user-pass")
				}
			}

			c.JSON(http.StatusOK, gin.H{"filename": configName, "content": content})
		})

		client.GET("/mfa", func(c *gin.Context) {
			if ldapAuth {
				c.JSON(http.StatusInternalServerError, gin.H{"message": "LDAP用户不支持设置MFA"})
				return
			}

			var u User

			session := sessions.Default(c)
			if user, ok := session.Get("user").(string); ok {
				u.Username = user
			}

			u = u.Info()
			if u.MfaSecret == "" {
				secret, err := GenMfa(u.Username)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"message": fmt.Errorf("MFA: %w", err).Error()})
				} else {
					u.MfaSecret = secret
					c.JSON(http.StatusOK, gin.H{"mfaEnable": false, "user": u})
				}
			} else {
				c.JSON(http.StatusOK, gin.H{"mfaEnable": true, "user": u})
			}
		})

		client.POST("/mfa", func(c *gin.Context) {
			var u User
			c.ShouldBind(&u)

			session := sessions.Default(c)
			if user, ok := session.Get("user").(string); ok {
				cu := User{Username: user}.Info()
				if u.ID != cu.ID {
					c.JSON(http.StatusInternalServerError, gin.H{"message": "非法请求"})
					return
				}
			}

			passcode := c.PostForm("passcode")

			vaild := ValidateMfa(passcode, u.MfaSecret)
			if !vaild {
				c.JSON(http.StatusInternalServerError, gin.H{"message": "验证码错误"})
			} else {
				db.Model(&User{}).Where("id = ?", u.ID).Update("mfa_secret", u.MfaSecret)
				c.JSON(http.StatusOK, gin.H{"message": "MFA已启用"})
			}
		})

		client.DELETE("/mfa/:id", func(c *gin.Context) {
			var u User
			c.ShouldBindUri(&u)

			session := sessions.Default(c)
			if user, ok := session.Get("user").(string); ok {
				cu := User{Username: user}.Info()
				if !(u.ID == cu.ID || cu.Username == adminUsername) {
					c.JSON(http.StatusInternalServerError, gin.H{"message": "非法请求"})
					return
				}
			}

			db.Model(&User{}).Where("id = ?", u.ID).Update("mfa_secret", nil)

			c.JSON(http.StatusOK, gin.H{"message": "MFA已停用"})
		})
	}

	// ---------- 统一网关：双通道监听 ----------
	// TCP 监听（保留）：供本地 O-Token 钩子 / openvpn 回调使用（http://127.0.0.1:webPort）。
	// 网关监听：若 SOCKET_PATH 非空，额外监听该 unix socket，由 fnOS 统一网关转发。
	// 前置网关中间件：剥离 GATEWAY_PREFIX；响应 3xx 时把 Location 的 "/" 前缀重写为 GATEWAY_PREFIX。
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if gwPrefix != "" {
			if req.URL.Path == gwPrefix {
				// 规范化：补尾斜杠，保证相对静态资源路径在网关前缀下正确解析
				http.Redirect(w, req, gwPrefix+"/", 302)
				return
			}
			if strings.HasPrefix(req.URL.Path, gwPrefix) {
				req.URL.Path = strings.TrimPrefix(req.URL.Path, gwPrefix)
				if req.URL.Path == "" {
					req.URL.Path = "/"
				}
			}
		}
		rw := &gwResponseWriter{ResponseWriter: w, prefix: gwPrefix}
		r.ServeHTTP(rw, req)
	})

	// TCP 通道（仅回环）：供本地 O-Token 钩子 / openvpn 回调使用（http://127.0.0.1:webPort）。
	// 纯统一网关模式下 Web UI 只经 /app/openvpn 暴露，TCP 不再对外监听（防 LAN 直连 8833 绕过 NAS 登录态）。
	go func() {
		addr := fmt.Sprintf("127.0.0.1:%s", webPort)
		srv := &http.Server{Addr: addr, Handler: handler}
		if e := srv.ListenAndServe(); e != nil && e != http.ErrServerClosed {
			logger.Error(context.Background(), "TCP listen error: "+e.Error())
		}
	}()

	// 网关 unix socket 通道
	socketPath := os.Getenv("SOCKET_PATH")
	if socketPath != "" {
		// 清理上一轮残留的 socket 文件（重启/升级后）
		os.Remove(socketPath)
		if dir := filepath.Dir(socketPath); dir != "" {
			os.MkdirAll(dir, 0755)
		}
		ul, e := net.Listen("unix", socketPath)
		if e != nil {
			logger.Error(context.Background(), "Unix socket listen error: "+e.Error())
		} else {
			// 允许网关进程（可能非 root）连接
			_ = os.Chmod(socketPath, 0666)
			logger.Info(context.Background(), "Listening on unix socket "+socketPath)
			srv := &http.Server{Handler: handler}
			go func() {
				if se := srv.Serve(ul); se != nil && se != http.ErrServerClosed {
					logger.Error(context.Background(), "Unix socket serve error: "+se.Error())
				}
			}()
			defer os.Remove(socketPath)
		}
	}

	// 阻塞主 goroutine，保持进程存活
	select {}
}

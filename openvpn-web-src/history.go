package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/gavintan/gopkg/tools"
	"github.com/spf13/viper"
)

// cst 为北京时间（Asia/Shanghai）时区。引入 time/tzdata 确保静态二进制内嵌时区库，
// 不依赖 NAS 本机 /usr/share/zoneinfo，避免 LoadLocation 在无 zoneinfo 时回退到 UTC。
var cst = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.UTC
	}
	return loc
}()

// todayStartCST 返回北京时间（Asia/Shanghai）今日 0 点的 Unix 秒级时间戳，
// 供「今日连接」统计与 connect 去重使用，与连接记录页显示时区一致。
func todayStartCST() int64 {
	now := time.Now().In(cst)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, cst).Unix()
}

type History struct {
	ID            uint      `gorm:"primarykey" json:"id" form:"id"`
	Vip           string    `gorm:"column:vip;comment:'VPN IP'" json:"vip" form:"vip"`
	Vip6          string    `gorm:"column:vip6;comment:'VPN IPV6'" json:"vip6" form:"vip6"`
	Rip           string    `gorm:"column:rip;comment:'用户 IP'" json:"rip" form:"rip"`
	Rip6          string    `gorm:"column:rip6;comment:'用户 IPV6'" json:"rip6" form:"rip6"`
	CommonName    string    `gorm:"column:common_name;comment:'客户端名称'" json:"common_name" form:"common_name"`
	Username      string    `gorm:"column:username;comment:'用户名'" json:"username" form:"username"`
	BytesReceived float64   `gorm:"comment:'下载流量'" form:"bytes_received" json:"bytes_received"`
	BytesSent     float64   `gorm:"comment:'上传流量'" json:"bytes_sent" form:"bytes_sent"`
	TimeUnix      int64     `gorm:"comment:'上线时间'" json:"time_unix" form:"time_unix"`
	TimeDuration  int64     `gorm:"column:time_duration;comment:'在线时长'" json:"time_duration" form:"time_duration"`
	CreatedAt     time.Time `json:"createdAt,omitempty" form:"createdAt,omitempty"`
	Action        string    `gorm:"-" json:"action,omitempty" form:"action,omitempty"`
}

type QueryData struct {
	Draw            int       `json:"draw"`
	RecordsTotal    int64     `json:"recordsTotal"`
	RecordsFiltered int64     `json:"recordsFiltered"`
	Data            []History `json:"data"`
}

func (h History) MarshalJSON() ([]byte, error) {
	type th History

	return json.Marshal(&struct {
		BytesReceived string `json:"bytes_received"`
		BytesSent     string `json:"bytes_sent"`
		TimeUnix      string `json:"time_unix"`
		TimeDuration  string `json:"time_duration"`
		CreatedAt     string `json:"createdAt"`
		th
	}{
		BytesReceived: tools.FormatBytes(h.BytesReceived),
		BytesSent:     tools.FormatBytes(h.BytesSent),
		TimeUnix:      time.Unix(h.TimeUnix, 0).In(cst).Format("2006-01-02 15:04:05"),
		CreatedAt:     h.CreatedAt.In(cst).Format("2006-01-02 15:04:05"),
		TimeDuration:  (time.Duration(h.TimeDuration) * time.Second).String(),
		th:            (th)(h),
	})
}

func (h History) All() []History {
	var historyList []History

	result := db.Table(h.TableName()).WithContext(context.Background()).Find(&historyList)
	if result.Error != nil {
		logger.Error(context.Background(), result.Error.Error())
		return []History{}
	}

	return historyList
}

func (h History) Create() error {
	result := db.Table(h.TableName()).WithContext(context.Background()).Create(&h)

	return result.Error
}

func (h History) Delete(id string) error {
	result := db.Table(h.TableName()).WithContext(context.Background()).Unscoped().Delete(&h, id)
	return result.Error
}

// RecordConnect 在客户端「上线」时落一条记录。
// 若该 common_name+vip 当天已有一条尚未完成（time_duration=0）的连接记录，
// 说明是重连触发的重复 client-connect，直接跳过，避免脏数据。
func (h History) RecordConnect() error {
	var cnt int64
	todayStart := todayStartCST()
	db.Table(h.TableName()).WithContext(context.Background()).
		Where("common_name = ? AND vip = ? AND time_unix >= ? AND time_duration = 0", h.CommonName, h.Vip, todayStart).
		Count(&cnt)
	if cnt > 0 {
		return nil
	}
	return h.Create()
}

// RecordDisconnect 在客户端「断开」时补全对应连接记录的流量与时长。
// 优先 UPDATE 最近一条同 common_name+vip 且 time_duration=0 的 connect 记录；
// 若找不到（如 web 重启丢失了上线记录），兜底 INSERT 一条完整记录。
func (h History) RecordDisconnect() error {
	var rec History
	err := db.Table(h.TableName()).WithContext(context.Background()).
		Where("common_name = ? AND vip = ? AND time_duration = 0", h.CommonName, h.Vip).
		Order("time_unix desc").Limit(1).Scan(&rec).Error
	if err == nil && rec.ID > 0 {
		updMap := map[string]interface{}{
			"bytes_received": h.BytesReceived,
			"bytes_sent":     h.BytesSent,
			"time_duration":  h.TimeDuration,
		}
		// 若 connect 时未记录 time_unix（存 0），disconnect 兜底补为当前时间，避免页面显示「—」
		if rec.TimeUnix == 0 {
			updMap["time_unix"] = time.Now().Unix()
		}
		upd := db.Table(h.TableName()).WithContext(context.Background()).
			Model(&History{}).Where("id = ?", rec.ID).
			Updates(updMap)
		return upd.Error
	}
	return h.Create()
}

// TodayCount 返回「今自然日（本地时区 0 点起）」的连接记录条数，
// 用于仪表盘「今日连接」卡片。time_unix 在库中为真·Unix 时间戳，
// 与连接记录页、time_unix 本地格式化口径保持一致。
func (h History) TodayCount() int64 {
	var cnt int64
	todayStart := todayStartCST()
	db.Table(h.TableName()).WithContext(context.Background()).
		Where("time_unix >= ?", todayStart).Count(&cnt)
	return cnt
}

func (h History) Query(p Params) QueryData {
	var qd QueryData
	var itmes []History
	var totalCount int64
	var filterCount int64

	db := db.Table(h.TableName())
	qdb := db.WithContext(context.Background())

	db.Count(&totalCount)

	if p.Qt != "" {
		qt := strings.Split(p.Qt, ",")
		qdb = qdb.Where("time_unix BETWEEN ? AND ?", qt[0], qt[1])
		qdb.Count(&totalCount)
	}

	if p.Search != "" {
		qdb = qdb.Where("vip LIKE @value OR rip LIKE @value OR common_name LIKE @value OR username LIKE @value", sql.Named("value", "%"+p.Search+"%"))
		qdb.Count(&filterCount)
	} else {
		filterCount = totalCount
	}

	// 排序字段兜底：前端（如 index.js 的 loadHistory）可能不传 orderColumn，
	// 直接拿空值拼 ORDER BY 会生成非法 SQL（ORDER BY  LIMIT 0）导致整页查询失败、记录显示为空。
	orderCol := p.OrderColumn
	if orderCol == "" {
		orderCol = "time_unix"
	}
	orderDir := p.Order
	if orderDir == "" {
		orderDir = "desc"
	}
	// 兼容前端两种分页命名：DataTables 默认用 start/length，
	// 而本控制台 loadHistory() 直接传 start/length；Params 同时认 offset/limit 与 start/length，
	// 否则 p.Limit 为 0 → LIMIT 0 → 查询返回 0 行（虽 recordsTotal 有值，列表空白）。
	offset := p.Offset
	if offset == 0 {
		offset = p.Start
	}
	limit := p.Limit
	if limit <= 0 {
		limit = p.Length
	}
	if limit <= 0 {
		limit = 50
	}
	result := qdb.Order(orderCol + " " + orderDir).Offset(offset).Limit(limit).Find(&itmes)
	if result.Error != nil {
		logger.Error(context.Background(), result.Error.Error())
		return QueryData{}
	}

	qd.Draw = p.Draw
	qd.Data = itmes
	qd.RecordsTotal = totalCount
	qd.RecordsFiltered = filterCount

	return qd
}

// Traffic 表示一个主体（客户端 CN 或用户名）的累计流量与最近一次会话信息。
type Traffic struct {
	Recv     float64
	Sent     float64
	Vip      string
	LastSeen int64
}

// trafficBy 按指定列聚合 history 表，得到「历史会话累计流量」。
// 在线会话的实时字节数不在 history 中（下线时才落库），调用方需自行叠加。
func (h History) trafficBy(column string) map[string]Traffic {
	out := make(map[string]Traffic)

	type row struct {
		K        string
		Recv     float64
		Sent     float64
		LastSeen int64
	}
	var rows []row

	result := db.Table(h.TableName()).WithContext(context.Background()).
		Select(column+" as k, COALESCE(SUM(bytes_received),0) as recv, COALESCE(SUM(bytes_sent),0) as sent, COALESCE(MAX(time_unix),0) as last_seen").
		Where(column+" != ''").
		Group(column).
		Scan(&rows)
	if result.Error != nil {
		logger.Error(context.Background(), result.Error.Error())
		return out
	}

	for _, r := range rows {
		out[r.K] = Traffic{Recv: r.Recv, Sent: r.Sent, LastSeen: r.LastSeen}
	}

	// 补一次最近会话的 VPN IP，便于离线客户端仍能展示上次分配的地址
	for k := range out {
		var last History
		if err := db.Table(h.TableName()).WithContext(context.Background()).
			Where(column+" = ?", k).Order("time_unix desc").Limit(1).Scan(&last).Error; err == nil {
			t := out[k]
			t.Vip = last.Vip
			out[k] = t
		}
	}

	return out
}

// TrafficByCommonName 按客户端证书 CN 聚合累计流量。
func (h History) TrafficByCommonName() map[string]Traffic {
	return h.trafficBy("common_name")
}

// TrafficByUsername 按登录用户名聚合累计流量。
func (h History) TrafficByUsername() map[string]Traffic {
	return h.trafficBy("username")
}

func (h History) Clear() error {
	result := db.Where("created_at < ?", time.Now().AddDate(0, 0, -viper.GetInt("system.base.history_max_days"))).Delete(&h)
	return result.Error
}

func (History) TableName() string {
	return "history"
}

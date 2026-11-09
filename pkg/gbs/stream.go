package gbs

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/gowvp/owl/pkg/gbs/sip"
)

// Streams Streams
type Streams struct {
	// 0  直播 1 历史
	T int `json:"t" gorm:"column:t"`
	// 设备ID
	DeviceID string `json:"deviceid" gorm:"column:deviceid"`
	// 通道ID
	ChannelID string `json:"channelid" gorm:"column:channelid"`
	//  pull 媒体服务器主动拉流，push 监控设备主动推流
	StreamType string `json:"streamtype" gorm:"column:streamtype"`
	// 0正常 1关闭 -1 尚未开始
	Status int `json:"status" gorm:"column:status"`
	// header from params
	// Ftag db.M `gorm:"column:ftag" sql:"type:json" json:"-"`
	// header to params
	// Ttag db.M `gorm:"column:ttag" sql:"type:json" json:"-"`
	// header callid
	CallID string `json:"callid" gorm:"column:callid"`
	// 是否停止
	Stop   bool   `json:"stop" gorm:"column:stop"`
	Msg    string `json:"msg" gorm:"column:msg"`
	CseqNo uint32 `json:"cseqno" gorm:"column:cseqno"`
	// 视频流ID gb28181的ssrc
	StreamID string `json:"streamid"  gorm:"column:streamid"`
	// m3u8播放地址
	HTTP string `json:"http" gorm:"column:http"`
	// rtmp 播放地址
	RTMP string `json:"rtmp" gorm:"column:rtmp"`
	// rtsp 播放地址
	RTSP string `json:"rtsp" gorm:"column:rtsp"`
	// flv 播放地址
	WSFLV string `json:"wsflv" gorm:"column:wsflv"`
	// zlm是否收到流
	Stream bool `json:"stream" gorm:"column:stream"`

	// ---
	S, E time.Time     `json:"-" gorm:"-"`
	ssrc string        // 国标ssrc 10进制字符串
	Ext  int64         `json:"-" gorm:"-"` // 流等待过期时间
	Resp *sip.Response `json:"-" gorm:"-"`
}

// 当前系统中存在的流列表
// type streamsList struct {
// 	// key=ssrc value=PlayParams  播放对应的PlayParams 用来发送bye获取tag，callid等数据
// 	Response *sync.Map
// 	// key=channelid value={Play}  当前设备直播信息，防止重复直播
// 	Succ *sync.Map
// 	ssrc atomic.Uint32
// }

// var StreamList streamsList

var ssrcCounter atomic.Uint32

func (g *GB28181API) getSSRC(t int) string {
	cfg := g.sipConfig()
	v := ssrcCounter.Add(1)
	ssrc := v % 9000
	key := fmt.Sprintf("%d%s%04d", t, cfg.GetDomain()[3:8], ssrc)
	return key
}

// // 定时检查未关闭的流
// // 检查规则：
// // 1. 数据库查询当前status=0在推流状态的所有流信息
// // 2. 比对当前streamlist中存在的流，如果不在streamlist或者ssrc与channelid不匹配则关闭
// func CheckStreams() {
// 	// logrus.Debugln("checkStreamWithCron")
// 	var skip int
// 	for {
// 		streams := []Streams{}
// 		// db.FindT(db.DBClient, new(Streams), &streams, db.M{"status=?": 0, "streamtype=?": "push"}, "", skip, 100, false)
// 		for _, stream := range streams {
// 			// logrus.Debugln("checkStreamStreamID", stream.StreamID, stream.DeviceID)
// 			if p, ok := StreamList.Response.Load(stream.StreamID); ok {
// 				streamActive := p.(*Streams)
// 				if streamActive.ChannelID == stream.ChannelID {
// 					// 此流在用
// 					// 查询media流是否仍然存在。不存在的需要关闭。
// 					rtpInfo := zlmGetMediaInfo(stream.StreamID)
// 					if rtpInfo.Exist {
// 						// 流仍然存在
// 						continue
// 					}
// 					if !stream.Stream && time.Now().Unix() < stream.Ext {
// 						// 推流尚未成功 未超时
// 						continue
// 					}
// 				}
// 			}
// 			// logrus.Debugln("checkStreamActiveDevice", stream.StreamID, stream.DeviceID)
// 			device, ok := _activeDevices.Get(stream.DeviceID)
// 			if !ok {
// 				continue
// 			}
// 			if device.source == nil {
// 				// logrus.Warningln("checkStreamDeviceSource is nil", stream.StreamID, stream.DeviceID)
// 				continue
// 			}
// 			// logrus.Debugln("checkStreamClosed", stream.StreamID, stream.DeviceID)
// 			// 关闭此流
// 			channel := Channels{ChannelID: stream.ChannelID}
// 			// if err := db.Get(db.DBClient, &channel); err != nil {
// 			// 	// logrus.Errorln("checkStreamGetchannelError", stream.StreamID, stream.ChannelID, err)
// 			// 	stream.Msg = err.Error()
// 			// 	db.Save(db.DBClient, stream)
// 			// 	channel = Channels{
// 			// 		ChannelID: stream.ChannelID,
// 			// 		DeviceID:  stream.DeviceID,
// 			// 		URIStr:    fmt.Sprintf("sip:%s@%s", stream.ChannelID, _serverDevices.Region),
// 			// 	}
// 			// }
// 			// channelURI, _ := sip.ParseURI(channel.URIStr)
// 			// channel.addr = &sip.Address{URI: channelURI, Params: sip.NewParams()}
// 			// for k, v := range stream.Ttag {
// 			// 	channel.addr.Params.Add(k, sip.String{Str: v.(string)})
// 			// }
// 			// for k, v := range stream.Ftag {
// 			// 	_serverDevices.addr.Params.Add(k, sip.String{Str: v.(string)})
// 			// }
// 			callid := sip.CallID(stream.CallID)
// 			stream.CseqNo++

// 			hb := sip.NewHeaderBuilder().SetToWithParam(channel.addr).SetFrom(_serverDevices.addr).AddVia(&sip.ViaHop{
// 				Params: sip.NewParams().Add("branch", sip.String{Str: sip.GenerateBranch()}),
// 			}).SetContentType(&sip.ContentTypeSDP).SetMethod(sip.MethodBYE).SetContact(_serverDevices.addr).SetCallID(&callid).SetSeqNo(uint(stream.CseqNo))
// 			req := sip.NewRequest("", sip.MethodBYE, channel.addr.URI, sip.DefaultSipVersion, hb.Build(), nil)
// 			req.SetDestination(device.source)
// 			req.SetRecipient(channel.addr.URI)

// 			// 不管成功不成功 程序都删除掉，后面开新流，关闭不成功的后面重试
// 			StreamList.Response.Delete(stream.StreamID)
// 			StreamList.Succ.Delete(stream.ChannelID)

// 			tx, err := svr.Request(req)
// 			if err != nil {
// 				// logrus.Warningln("checkStreamClosedFail", stream.StreamID, err)
// 				stream.Msg = err.Error()
// 				// db.Save(db.DBClient, stream)
// 				continue
// 			}
// 			response := tx.GetResponse()
// 			if response == nil {
// 				// logrus.Warningln("checkStreamClosedFail response is nil", channel.ChannelID, channel.DeviceID, stream.StreamID)
// 				continue
// 			}
// 			if response.StatusCode() != http.StatusOK {
// 				if response.StatusCode() == 481 {
// 					// logrus.Infoln("checkStreamClosedFail1", stream.StreamID, response.StatusCode())
// 					stream.Msg = response.Reason()
// 					stream.Status = 1
// 					stream.Stop = true
// 				} else {
// 					// logrus.Warningln("checkStreamClosedFail1", stream.StreamID, response.StatusCode())
// 					stream.Msg = response.Reason()
// 				}
// 			} else {
// 				stream.Status = 1
// 				stream.Stop = true
// 			}
// 			// db.Save(db.DBClient, stream)

// 		}
// 		if len(streams) != 100 {
// 			break
// 		}
// 		skip += 100
// 	}
// }
